package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/behringer24/freizone-bot/internal/account"
	"github.com/behringer24/freizone-bot/internal/config"
	"github.com/behringer24/freizone-server/pkg/client"
)

// minUpkeepSpacing bounds how often upkeep may run however often it is asked
// for. A flapping network produces a burst of reconnects, and answering twenty
// of them with twenty rounds of requests against an already-struggling server
// is the wrong response to it.
const minUpkeepSpacing = 5 * time.Minute

// shutdownGrace is how long the loops get to finish what they are in the middle
// of once a signal arrives.
const shutdownGrace = 15 * time.Second

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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Buffered and depth one: several reconnects while upkeep is running should
	// collapse into one further round, not queue up N of them.
	upkeepWanted := make(chan struct{}, 1)

	upkeepDone := runUpkeep(ctx, c, cfg, logger, upkeepWanted)
	streamDone := runStream(ctx, c, logger, upkeepWanted)

	logger.Info("bot started",
		"account", id.AccountID, "server", id.Server,
		"route_group", cfg.RouteGroup, "route_peers", len(cfg.RoutePeers),
	)

	<-ctx.Done()
	logger.Info("shutting down")

	<-streamDone
	<-upkeepDone
	return nil
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
func runStream(ctx context.Context, c *client.Client, logger *slog.Logger, upkeepWanted chan<- struct{}) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range c.Stream(ctx, client.StreamPolicy{}) {
			switch ev.Kind {
			case client.StreamConnected:
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
				logger.Debug("stream dropped", "error", ev.Err)

			case client.StreamFailed:
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
