package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/behringer24/freizone-bot/internal/account"
	"github.com/behringer24/freizone-bot/internal/config"
	"github.com/behringer24/freizone-bot/internal/control"
	"github.com/behringer24/freizone-bot/internal/ipc"
	"github.com/behringer24/freizone-bot/internal/outbound"
	"github.com/behringer24/freizone-bot/internal/outbox"
	"github.com/behringer24/freizone-server/pkg/client"
)

// version is what a `status` answer reports, so a CLI from a different build can
// say which two versions disagree. Set at build time via -ldflags in a release;
// "dev" is honest for everything else.
var version = "dev"

// minUpkeepSpacing bounds how often upkeep may run however often it is asked
// for. A flapping network produces a burst of reconnects, and answering twenty
// of them with twenty rounds of requests against an already-struggling server
// is the wrong response to it.
const minUpkeepSpacing = 5 * time.Minute

// shutdownGrace is how long the loops get to finish what they are in the middle
// of once a signal arrives.
const shutdownGrace = 15 * time.Second

// outboxRetryInterval is how often the delivery loop looks for entries whose
// backoff has elapsed. Short, because it only reads a directory -- the actual
// waiting is per entry, in the outbox.
const outboxRetryInterval = 5 * time.Second

func runDaemon(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	c, err := account.Open(cfg)
	if err != nil {
		return err
	}
	// Released last, after every loop has stopped: while this is held nothing
	// else can take the account, and giving it up early would let a second
	// process in while this one is still finishing.
	defer func() {
		if err := c.Close(); err != nil {
			logger.Error("releasing the account", "error", err)
		}
	}()

	// A context for the work that has to happen before the loops start. Not the
	// signal context: a Ctrl-C during registration should abandon it, and it
	// does, but registration is resumable so that is safe.
	startCtx, cancelStart := context.WithTimeout(context.Background(), time.Minute)
	id, fresh, err := account.EnsureRegistered(startCtx, c, cfg, logger)
	cancelStart()
	if err != nil {
		return err
	}
	announce(cfg, id, fresh, logger)

	if !cfg.RoutesConfigured() {
		// Deliberately a refusal rather than a warning: a daemon with nowhere
		// to send is a daemon that will silently accept messages and drop them.
		// Registering with no route configured is a legitimate *first* run,
		// which is why the address was printed above before this returns.
		return fmt.Errorf(
			"no route configured: set FREIZONE_BOT_ROUTE_GROUP or FREIZONE_BOT_ROUTE_PEERS " +
				"so there is somewhere for messages to go")
	}

	box, err := outbox.Open(cfg.OutboxDir(), cfg.OutboxMax)
	if err != nil {
		return err
	}
	// Anything a previous run accepted but never delivered is still on disk and
	// is retried -- which is the whole reason it is on disk.
	if pending, err := box.Len(); err == nil && pending > 0 {
		logger.Info("outbox carried over from a previous run", "waiting", pending)
	}

	// Opened only now, after the account lock is held: a leftover socket file
	// may be cleared only once something proves no daemon is running, and the
	// lock is that proof.
	ln, err := ipc.Listen(cfg.ControlSocket)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Buffered and depth one: several reconnects while upkeep is running should
	// collapse into one further round, not queue up N of them.
	upkeepWanted := make(chan struct{}, 1)

	d := &daemon{
		cfg:     cfg,
		client:  c,
		logger:  logger,
		box:     box,
		sender:  outbound.NewSender(c),
		limiter: outbound.NewLimiter(cfg.RatePerMinute, time.Now),
		deduper: outbound.NewDeduper(cfg.DedupWindow, time.Now),
		started: time.Now(),
		id:      id,
	}

	ctrl := control.New(ln, version, logger, map[string]control.Handler{
		ipc.OpSend:   d.handleSend,
		ipc.OpStatus: d.handleStatus,
	})
	ctrlErr := make(chan error, 1)
	go func() { ctrlErr <- ctrl.Serve() }()

	upkeepDone := runUpkeep(ctx, c, cfg, logger, upkeepWanted)
	streamDone := runStream(ctx, c, logger, upkeepWanted, d)
	outboxDone := runOutbox(ctx, d)

	logger.Info("bot started",
		"account", id.AccountID, "server", id.Server,
		"route_group", cfg.RouteGroup, "route_peers", len(cfg.RoutePeers),
		"control_socket", cfg.ControlSocket,
	)

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-ctrlErr:
		if err != nil {
			return fmt.Errorf("control socket: %w", err)
		}
	}

	// The order is deliberately not the reverse of startup.
	//
	// The control socket closes *first*, so nothing new is accepted. Accepting a
	// message after deciding to shut down would mean telling a caller it is
	// safely queued when it will not be delivered until after the restart --
	// which is the one thing an alerting tool must not do quietly.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := ctrl.Shutdown(shutdownCtx); err != nil {
		logger.Warn("control socket did not shut down cleanly", "error", err)
	}

	// Then a bounded chance for the outbox to get out what it already holds.
	d.flush(shutdownCtx)

	<-streamDone
	<-upkeepDone
	<-outboxDone
	// The account lock is released last, by the deferred Close: nothing else may
	// take the account while this process is still finishing.
	return nil
}

// daemon is the state the request handlers and the delivery loop share.
type daemon struct {
	cfg     *config.Config
	client  *client.Client
	logger  *slog.Logger
	box     *outbox.Outbox
	sender  outbound.Sender
	limiter *outbound.Limiter
	deduper *outbound.Deduper
	started time.Time
	id      client.Identity

	// connected is what the stream last reported. Only ever read for a status
	// answer, so a plain mutex is the right amount of machinery.
	mu        sync.Mutex
	connected bool
}

func (d *daemon) setConnected(v bool) {
	d.mu.Lock()
	d.connected = v
	d.mu.Unlock()
}

func (d *daemon) isConnected() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.connected
}

// handleSend accepts a message, resolves where it goes, and puts it in the
// outbox -- then answers.
//
// The acknowledgement point is deliberate: durably queued, not delivered. A
// cron job wants a fast exit code, but `exit 0` must never mean "accepted into
// a buffer a restart discards". A caller that wants delivery asks to wait.
func (d *daemon) handleSend(ctx context.Context, req ipc.Request) (any, error) {
	var body ipc.SendRequest
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: "malformed send request"}
	}
	if strings.TrimSpace(body.Title) == "" && strings.TrimSpace(body.Text) == "" {
		return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: "nothing to send"}
	}

	dests, err := outbound.Resolve(d.cfg, body.Route, body.Severity)
	if err != nil {
		return nil, &ipc.Error{Code: ipc.CodeNoRoute, Message: err.Error()}
	}

	msg := outbound.Message{
		Title:    body.Title,
		Text:     body.Text,
		Severity: body.Severity,
		Source:   body.Source,
		At:       body.At,
	}

	// Deduplication before the rate cap, on purpose. They answer different
	// questions -- "is this the same alert again" and "is too much leaving at
	// once" -- and a repeat that the deduper would swallow should not consume a
	// slot in the rate window on its way to being dropped. The other order would
	// let one flapping check exhaust the budget for everything else.
	if allowed, note := d.deduper.Allow(msg, body.DedupKey); !allowed {
		d.logger.Info("message suppressed as a duplicate",
			"severity", body.Severity, "source", body.Source, "title", body.Title)
		return ipc.SendResponse{Suppressed: true, SuppressedBy: "duplicate"}, nil
	} else if note != "" {
		msg.Text += note
	}

	allowed, note := d.limiter.Allow()
	if !allowed {
		// Refused, not queued, and reported as such: a cap that silently
		// swallows is indistinguishable from a bot that has died.
		d.logger.Warn("message suppressed by the rate limit",
			"source", body.Source, "severity", body.Severity)
		return ipc.SendResponse{Suppressed: true, SuppressedBy: "rate"}, nil
	}
	msg.Text += note
	if msg.At.IsZero() {
		msg.At = time.Now().UTC()
	}

	ids, err := d.box.Enqueue(msg, dests, time.Now().UTC())
	if err != nil {
		if errors.Is(err, outbox.ErrFull) {
			return nil, &ipc.Error{Code: ipc.CodeOutboxFull, Message: err.Error()}
		}
		return nil, err
	}
	d.logger.Info("message queued",
		"destinations", len(ids), "severity", body.Severity, "source", body.Source)

	if !body.Wait {
		return ipc.SendResponse{Queued: len(ids)}, nil
	}

	delivered := d.deliverNow(ctx, ids)
	return ipc.SendResponse{Queued: len(ids), Delivered: delivered}, nil
}

func (d *daemon) handleStatus(context.Context, ipc.Request) (any, error) {
	waiting, err := d.box.Len()
	if err != nil {
		return nil, err
	}
	return ipc.StatusResponse{
		Address:    d.id.AccountID + "*" + d.id.Server,
		Connected:  d.isConnected(),
		Outbox:     waiting,
		RouteGroup: d.cfg.RouteGroup,
		RoutePeers: len(d.cfg.RoutePeers),
		Uptime:     time.Since(d.started).Round(time.Second).String(),
	}, nil
}

// deliverNow tries the named entries once and reports how many got through. Used
// only by a caller that asked to wait; everything else goes through the loop.
func (d *daemon) deliverNow(ctx context.Context, ids []string) int {
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	due, err := d.box.Due(time.Now().UTC())
	if err != nil {
		d.logger.Warn("could not read the outbox", "error", err)
		return 0
	}
	var delivered int
	for _, e := range due {
		if !wanted[e.ID] {
			continue
		}
		if d.attempt(ctx, e) {
			delivered++
		}
	}
	return delivered
}

// attempt delivers one entry, and records what happened. Reports success.
func (d *daemon) attempt(ctx context.Context, e outbox.Entry) bool {
	text := e.Message.Render(time.Now().UTC())
	if err := d.sender.Deliver(ctx, e.Destination, text); err != nil {
		dropped, ferr := d.box.Fail(e.ID, err, time.Now().UTC(), d.cfg.MaxAge)
		if ferr != nil {
			d.logger.Error("could not record a failed delivery", "id", e.ID, "error", ferr)
		}
		if dropped {
			// Loud, and naming both the message and where it was going: a
			// silently dropped alert is a lie about a channel somebody is
			// relying on.
			d.logger.Error("giving up on a message",
				"destination", e.Destination.String(),
				"title", e.Message.Title,
				"age", time.Since(e.FirstSeen).Round(time.Second),
				"attempts", e.Attempts+1,
				"error", err)
		} else {
			d.logger.Warn("delivery failed, will retry",
				"destination", e.Destination.String(), "attempts", e.Attempts+1, "error", err)
		}
		return false
	}
	if err := d.box.Done(e.ID); err != nil {
		// Delivered but not removed: it will be delivered again on the next
		// pass, which is worth saying out loud since a duplicate page is
		// confusing in its own right.
		d.logger.Error("delivered but could not clear the outbox entry",
			"id", e.ID, "destination", e.Destination.String(), "error", err)
	}
	d.logger.Debug("delivered", "destination", e.Destination.String())
	return true
}

// flush gives the outbox one bounded chance to empty on the way out.
func (d *daemon) flush(ctx context.Context) {
	due, err := d.box.Due(time.Now().UTC())
	if err != nil || len(due) == 0 {
		return
	}
	d.logger.Info("flushing the outbox before shutting down", "waiting", len(due))
	for _, e := range due {
		if ctx.Err() != nil {
			d.logger.Warn("shutdown grace ran out with messages still queued", "waiting", len(due))
			return
		}
		d.attempt(ctx, e)
	}
}

// runOutbox delivers what is due, woken by a new message and by a retry ticker.
func runOutbox(ctx context.Context, d *daemon) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(outboxRetryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-d.box.Notify():
			case <-ticker.C:
			}

			due, err := d.box.Due(time.Now().UTC())
			if err != nil {
				d.logger.Warn("could not read the outbox", "error", err)
				continue
			}
			for _, e := range due {
				if ctx.Err() != nil {
					return
				}
				d.attempt(ctx, e)
			}
		}
	}()
	return done
}

// announce puts the bot's address where an operator will find it. On a fresh
// registration it is the one thing they must act on -- the bot cannot be
// invited anywhere without it.
func announce(cfg *config.Config, id client.Identity, fresh bool, logger *slog.Logger) {
	if err := account.WriteAddress(cfg, id); err != nil {
		logger.Warn("could not write the address file", "error", err)
	}
	if !fresh {
		logger.Info("account ready", "address", id.AccountID+"*"+id.Server)
		return
	}
	// Deliberately also on stderr, unstructured: a first start is often watched
	// by a person, and a JSON log line is the wrong shape for the one thing
	// they have to copy somewhere else.
	fmt.Fprintf(os.Stderr, "\n  This bot registered as:\n\n      %s*%s\n\n"+
		"  Invite that address to the group it should post in.\n"+
		"  It is also in %s.\n\n", id.AccountID, id.Server, cfg.AddressFile())
	logger.Info("registered", "address", id.AccountID+"*"+id.Server)
}

// runStream keeps the live message stream up and handles what arrives on it.
//
// Deliberately thin: everything an envelope is owed -- acknowledging it,
// confirming it, answering a group peer whose view drifted -- is
// HandleAndAck's, in the core, so the live path and the queue drain cannot
// drift on what "handled" means.
func runStream(ctx context.Context, c *client.Client, logger *slog.Logger, upkeepWanted chan<- struct{}, d *daemon) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer d.setConnected(false)
		for ev := range c.Stream(ctx, client.StreamPolicy{}) {
			switch ev.Kind {
			case client.StreamConnected:
				d.setConnected(true)
				logger.Info("stream connected")
				// Ask for upkeep rather than doing it here: draining a queue is
				// a round of requests, and doing it on this goroutine would
				// stop reading events for as long as it takes -- with the
				// stream's buffer filling behind it.
				select {
				case upkeepWanted <- struct{}{}:
				default: // one is already pending, which is enough
				}

			case client.StreamMessage:
				// OpenChatID stays empty: it means "the user is looking at this
				// chat", and a bot has no screen.
				res, err := c.HandleAndAck(ctx, ev.Message, client.ReceiveOptions{})
				if err != nil {
					// Not fatal, and not the whole message: a failure names a
					// sender and a reason, and the body is somebody's private
					// text that has no business in an operations log.
					logger.Warn("could not read an envelope",
						"message_id", ev.Message.MessageID,
						"sender", ev.Message.SenderAccountID,
						"error", err)
					continue
				}
				// Said at debug, and said at all: a daemon that handles
				// messages in complete silence gives an operator no way to tell
				// a live stream from one that is merely connected. Never the
				// text -- that is somebody's private message, and it is already
				// in the transcript.
				logger.Debug("envelope handled",
					"sender", res.PeerAccountID,
					"duplicate", res.Duplicate,
					"group", res.Group != nil)

			case client.StreamDisconnected:
				// Routine, not a failure: the core reconnects on its own, and a
				// warning here would train an operator to ignore the log.
				d.setConnected(false)
				logger.Debug("stream dropped", "error", ev.Err)

			case client.StreamFailed:
				d.setConnected(false)
				logger.Warn("stream could not connect", "error", ev.Err)
			}
		}
	}()
	return done
}

// runUpkeep drains the queue and does the periodic maintenance, on every
// reconnect and on a timer.
//
// The timer is the part that differs from the app, and it matters here. A phone
// reconnects constantly -- screen off, network handover, resume -- so
// connect-triggered upkeep is enough for it. A server bot can hold one stream
// for weeks, and then none of this would ever run again: the one-time prekey
// pool would drain toward empty with nothing to notice, and a group snapshot
// debt incurred in hour two would still be owed in week three.
func runUpkeep(ctx context.Context, c *client.Client, cfg *config.Config, logger *slog.Logger, wanted <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(cfg.MaintenanceInterval)
		defer ticker.Stop()

		var last time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-wanted:
			case <-ticker.C:
			}

			if !last.IsZero() && time.Since(last) < minUpkeepSpacing {
				logger.Debug("upkeep skipped, ran recently", "since", time.Since(last))
				continue
			}
			last = time.Now()
			upkeep(ctx, c, logger)
		}
	}()
	return done
}

func upkeep(ctx context.Context, c *client.Client, logger *slog.Logger) {
	// The queue first: it holds everything that arrived while nothing was
	// listening, and leaving it would let it grow to the server's per-device
	// cap -- past which every sender to this bot starts being refused.
	report, err := c.Drain(ctx, client.ReceiveOptions{})
	if err != nil {
		logger.Warn("could not drain the queue", "error", err)
	} else {
		if len(report.Results) > 0 {
			logger.Info("queue drained", "handled", len(report.Results))
		}
		for _, f := range report.Failures {
			logger.Warn("could not read a queued envelope",
				"message_id", f.MessageID, "sender", f.SenderAccountID,
				"acknowledged", f.Acknowledged, "error", f.Err)
		}
	}

	m := c.Maintain(ctx)
	logger.Debug("maintenance done",
		"prekeys_topped_up", m.PrekeysToppedUp,
		"debts_paid", m.DebtsPaid,
		"sessions_recovered", len(m.Recovered),
		"receipts_resent", m.ReceiptsResent,
	)
	for _, p := range m.Problems {
		logger.Warn("maintenance problem", "error", p)
	}
	// Said here because nothing else ever will: a member whose account is gone
	// keeps their row in the group until a moderator removes it, and this is
	// the only moment anything found out.
	for _, gone := range m.GoneMembers {
		logger.Warn("group member's account no longer exists", "account", gone)
	}
}
