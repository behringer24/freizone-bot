package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/behringer24/freizone-bot/internal/account"
	"github.com/behringer24/freizone-bot/internal/action"
	"github.com/behringer24/freizone-bot/internal/authz"
	"github.com/behringer24/freizone-bot/internal/command"
	"github.com/behringer24/freizone-bot/internal/config"
	"github.com/behringer24/freizone-bot/internal/control"
	"github.com/behringer24/freizone-bot/internal/declared"
	"github.com/behringer24/freizone-bot/internal/ipc"
	"github.com/behringer24/freizone-bot/internal/outbound"
	"github.com/behringer24/freizone-bot/internal/outbox"
	"github.com/behringer24/freizone-server/pkg/client"
	"github.com/behringer24/freizone-server/pkg/group"
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
	// Only the daemon needs a server: the CLI subcommands talk to the local
	// socket or read a file.
	if err := cfg.RequireServer(); err != nil {
		return err
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
	ln, err := ipc.Listen(cfg.ControlSocket, cfg.ControlGroup)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Buffered and depth one: several reconnects while upkeep is running should
	// collapse into one further round, not queue up N of them.
	upkeepWanted := make(chan struct{}, 1)

	d := &daemon{
		cfg:              cfg,
		client:           c,
		logger:           logger,
		box:              box,
		sender:           outbound.NewSender(c),
		limiter:          outbound.NewLimiter(cfg.RatePerMinute, time.Now),
		deduper:          outbound.NewDeduper(cfg.DedupWindow, time.Now),
		started:          time.Now(),
		acceptInvitation: c.AcceptGroupInvitation,
		membershipOf:     c.GroupMembership,
		id:               id,
	}

	// The command surface, if one is configured. Built after d exists because
	// the status action reads from it -- and left entirely absent otherwise, so
	// there is no half-wired path for a message to wander into.
	d.policy = authz.New(cfg.Commanders, cfg.AllowGroupCommands)
	if d.policy.Enabled() {
		jokes, err := action.LoadJokes(cfg.JokesFile)
		if err != nil {
			return err
		}
		// Read before anything is registered, so a typo in the file stops the
		// daemon here rather than surfacing as one broken command the first time
		// somebody in a chat tries it.
		declarations, err := declared.Load(cfg.ActionsFile)
		if err != nil {
			return err
		}
		d.actions = action.NewRegistry()
		action.RegisterBuiltins(d.actions, action.Builtins{
			Status:     d.statusLine,
			Recipients: d.recipientLines,
			Routes:     d.routeLines,
			Jokes:      jokes,
		})
		if err := declared.Register(d.actions, declarations, declared.Client()); err != nil {
			return err
		}
		// The parser is built from the same specs a model-driven interpreter
		// would render as tool definitions -- which is the check that those
		// specs are a sufficient description of an action.
		d.interpreter = command.NewBuiltin(d.actions.Specs())
		logger.Info("command surface enabled",
			"commanders", d.policy.Commanders(),
			"group_commands", cfg.AllowGroupCommands,
			"actions", len(d.actions.Specs()),
			"declared", len(declarations))
	} else {
		logger.Info("command surface disabled: no FREIZONE_BOT_COMMANDERS configured")
		// A file full of declared actions and no allow-list is a configuration
		// that does exactly nothing, and does it quietly. Said out loud, because
		// the person who wrote that file is expecting the opposite.
		if cfg.ActionsFile != "" {
			logger.Warn("declared actions will not be reachable: "+
				"the command surface is off, so nobody can call them",
				"file", cfg.ActionsFile)
		}
	}

	ctrl := control.New(ln, version, logger, map[string]control.Handler{
		ipc.OpSend:   d.handleSend,
		ipc.OpStatus: d.handleStatus,
	})
	stopWebhook, err := serveWebhook(d)
	if err != nil {
		return err
	}

	ctrlErr := make(chan error, 1)
	go func() { ctrlErr <- ctrl.Serve() }()

	// Before the loops: an invitation may have been waiting on disk since a run
	// that had no group configured, and nothing will announce it again.
	d.joinConfiguredGroupIfInvited(ctx)

	upkeepDone := runUpkeep(ctx, d, upkeepWanted)
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
	// Both ingresses close *first*, so nothing new is accepted. Accepting a
	// message after deciding to shut down would mean telling a caller it is
	// safely queued when it will not be delivered until after the restart --
	// which is the one thing an alerting tool must not do quietly.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	stopWebhook()
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

	// acceptInvitation joins a group. A field rather than a direct call so the
	// *decision* about which invitations to answer is testable without a server
	// and an account -- the protocol call itself is pkg/client's, and has its
	// own tests there.
	acceptInvitation func(ctx context.Context, groupID string) error

	// membershipOf reads a group's resolved fact set, injectable for the same
	// reason: what is worth testing is the catch-up decision, not the fold.
	membershipOf func(groupID string) (*group.Resolved, error)

	// The inbound half. Nil interpreter means no command surface is configured,
	// which is the default -- see internal/authz on why that fails closed.
	policy      *authz.Policy
	interpreter command.Interpreter
	actions     *action.Registry

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

	accepted, err := d.accept(outbound.Message{
		Title:  body.Title,
		Text:   body.Text,
		Labels: body.Labels,
		At:     body.At,
	}, body.Route, body.DedupKey)
	switch {
	case errors.Is(err, outbound.ErrNoRoute):
		return nil, &ipc.Error{Code: ipc.CodeNoRoute, Message: err.Error()}
	case errors.Is(err, outbox.ErrFull):
		return nil, &ipc.Error{Code: ipc.CodeOutboxFull, Message: err.Error()}
	case err != nil:
		return nil, err
	}
	if accepted.SuppressedBy != "" {
		return ipc.SendResponse{Suppressed: true, SuppressedBy: accepted.SuppressedBy}, nil
	}

	if !body.Wait {
		return ipc.SendResponse{Queued: len(accepted.IDs)}, nil
	}

	delivered := d.deliverNow(ctx, accepted.IDs)
	return ipc.SendResponse{Queued: len(accepted.IDs), Delivered: delivered}, nil
}

// accepted is the outcome of taking a message in.
type accepted struct {
	// IDs is one outbox entry per destination, empty when suppressed.
	IDs []string

	// SuppressedBy names the cap that swallowed this message, or is empty.
	SuppressedBy string
}

// accept is every decision between "something produced a message" and "it is
// durably queued": routing, deduplication, the rate cap, the timestamp.
//
// One function for every producer, and that is the point rather than tidiness.
// The control socket and the webhook receiver are two ways in, and three times
// in this project a second path has quietly drifted from the first -- the group
// join, the command dispatch, the invitation catch-up. A webhook that resolved
// its own routes, or skipped the deduplicator because nobody remembered it,
// would be the fourth. There is nothing to keep in step because there is one
// path.
func (d *daemon) accept(msg outbound.Message, route, dedupKey string) (accepted, error) {
	dests, err := outbound.Resolve(d.cfg, route, msg.Labels)
	if err != nil {
		return accepted{}, err
	}

	// Deduplication before the rate cap, on purpose. They answer different
	// questions -- "is this the same thing again" and "is too much leaving at
	// once" -- and a repeat that the deduper would swallow should not consume a
	// slot in the rate window on its way to being dropped. The other order would
	// let one flapping producer exhaust the budget for everything else.
	if allowed, note := d.deduper.Allow(msg, dedupKey); !allowed {
		d.logger.Info("message suppressed as a duplicate",
			"title", msg.Title, "labels", labelPairs(msg.Labels))
		return accepted{SuppressedBy: "duplicate"}, nil
	} else if note != "" {
		msg.Text += note
	}

	allowed, note := d.limiter.Allow()
	if !allowed {
		// Refused, not queued, and reported as such: a cap that silently
		// swallows is indistinguishable from a bot that has died.
		d.logger.Warn("message suppressed by the rate limit",
			"title", msg.Title, "labels", labelPairs(msg.Labels))
		return accepted{SuppressedBy: "rate"}, nil
	}
	msg.Text += note
	if msg.At.IsZero() {
		msg.At = time.Now().UTC()
	}

	ids, err := d.box.Enqueue(msg, dests, time.Now().UTC())
	if err != nil {
		return accepted{}, err
	}
	d.logger.Info("message queued",
		"destinations", len(ids), "title", msg.Title, "labels", labelPairs(msg.Labels))
	return accepted{IDs: ids}, nil
}

// onReceived is everything the bot itself owes an envelope, once the core has
// finished with it.
//
// One function, called from both the live stream and the queue drain, and that
// is the whole reason it exists. Those two paths had drifted: the command
// dispatch hung off the stream alone, so anything that arrived while the bot
// was down was stored and then never answered -- and a group invitation that
// came in the same window was never seen at all. SRV-30 put the *envelope*
// handling in one place for exactly this reason; this is the same discipline one
// layer up, where the bot's own follow-up lives.
func (d *daemon) onReceived(ctx context.Context, res client.ReceiveResult) {
	d.maybeJoinGroup(ctx, res)
	d.dispatch(ctx, res)
}

// joinConfiguredGroupIfInvited answers an invitation that is already sitting on
// disk, at startup.
//
// This exists because an invitation is only *announced* once. The receive path
// reports it when the facts are new to this device, and never again -- so an
// invitation that arrived while the group was not configured is folded, ignored,
// and never mentioned by anything afterwards. Configuring the group later and
// restarting would do nothing at all.
//
// Which is exactly the order an operator is led into: the first run prints
// "invite that address to the group it should post in", so of course the
// invitation comes before anybody has looked up the group id. Reading the facts
// we already hold, rather than waiting to be told again, is what makes the two
// orders equivalent.
func (d *daemon) joinConfiguredGroupIfInvited(ctx context.Context) {
	if d.cfg.RouteGroup == "" {
		return
	}
	membership, err := d.membershipOf(d.cfg.RouteGroup)
	if err != nil {
		d.logger.Warn("could not read the configured group's facts", "group", d.cfg.RouteGroup, "error", err)
		return
	}
	if membership == nil {
		// No facts for it yet -- either nobody has invited the bot, or the
		// invitation has not arrived. Both are ordinary, and the live path
		// handles the invitation when it comes.
		return
	}

	me := d.id.AccountID
	for _, m := range membership.Members {
		if m.AccountID != me {
			continue
		}
		if m.Joined {
			return // already a member; nothing to do
		}
		d.logger.Info("finishing an invitation that was waiting on disk", "group", d.cfg.RouteGroup)
		if err := d.acceptInvitation(ctx, d.cfg.RouteGroup); err != nil {
			d.logger.Error("could not accept the waiting invitation",
				"group", d.cfg.RouteGroup, "error", err)
			return
		}
		d.logger.Info("joined a group", "group", d.cfg.RouteGroup)
		return
	}
	// Facts held, but this bot is not in the member list: it was removed, or it
	// only ever received a snapshot of somebody else's group. Nothing to accept.
	d.logger.Info("the configured group's facts do not list this bot as a member",
		"group", d.cfg.RouteGroup)
}

// maybeJoinGroup answers an invitation, when it is one the operator asked for.
//
// A group membership is not real until the invited account says so -- the fact
// set records the invitation, and `join_accept` is what turns it into
// membership (see freizone-server's design/01-groups.md). Nothing else in this
// bot ever sent that, which meant a configured group route could not work at
// all: the bot sat invited forever and its messages went to a group it was not
// in.
//
// Which invitations to answer is deliberately narrow. The configured route
// group is accepted because the operator named it -- they cannot have named it
// by accident. Anything else needs FREIZONE_BOT_ACCEPT_GROUP_INVITES, because
// accepting freely means anyone who knows this bot's address can pull it into
// a group of theirs, and from then on it holds that group's facts and receives
// its traffic.
func (d *daemon) maybeJoinGroup(ctx context.Context, res client.ReceiveResult) {
	if res.Group == nil || !res.Group.Invited {
		return
	}
	groupID := res.Group.GroupID

	switch {
	case groupID == d.cfg.RouteGroup:
		// The one the operator configured. Joining is the whole point.
	case d.cfg.AcceptGroupInvites:
		// Explicitly opted in to being invitable.
	default:
		// Left unanswered rather than declined: declining is a signed fact that
		// says something, and the honest state here is "nobody asked this bot
		// to be in that group". Said at info, because the person who sent the
		// invitation is waiting for something that will not happen, and the
		// operator is the only one who can explain why.
		d.logger.Info("ignoring an invitation to a group this bot was not configured for",
			"group", groupID, "invited_by", res.PeerAccountID)
		return
	}

	if err := d.acceptInvitation(ctx, groupID); err != nil {
		d.logger.Error("could not accept a group invitation", "group", groupID, "error", err)
		return
	}
	d.logger.Info("joined a group", "group", groupID, "invited_by", res.PeerAccountID)
}

// dispatch is the inbound half: a message somebody sent the bot, possibly
// meaning something.
//
// The order of the checks here is the most important thing in this file, and it
// is not an optimisation. **Authorization comes before interpretation.** A
// sender who is not allow-listed has their text dropped before the interpreter
// ever sees it.
//
// Today the interpreter is a parser and the ordering looks academic. It stops
// being academic the moment a model sits there (BOT-10): an interpreter that
// sees everything is one that anyone knowing this bot's address can write
// prompts for. Doing the check first is what makes that impossible rather than
// merely unlikely.
func (d *daemon) dispatch(ctx context.Context, res client.ReceiveResult) {
	if d.interpreter == nil {
		return // no command surface configured
	}
	// A duplicate has already been acted on, and a blocked sender is somebody
	// the operator cut off -- neither should reach anything below.
	if res.Duplicate || res.Blocked {
		return
	}
	if res.Content.Kind != client.ContentText && res.Content.Kind != client.ContentGroupText {
		return
	}

	chatID, isGroup := res.PeerAccountID, false
	if res.Group != nil {
		chatID, isGroup = res.Group.GroupID, true
	}

	if !d.policy.MayCommand(res.PeerAccountID, isGroup) {
		// Silence, not a refusal. A refusal is an oracle -- it confirms to
		// whoever asked that something is here and listening -- and it is an
		// amplification vector besides. Logged at debug so an operator
		// wondering why their own command did nothing can find out.
		d.logger.Debug("ignoring a message from somebody who may not command this bot",
			"sender", res.PeerAccountID, "group", isGroup)
		return
	}

	intent, err := d.interpreter.Interpret(ctx, command.Input{
		Text:            res.Content.Text,
		SenderAccountID: res.PeerAccountID,
		ChatID:          chatID,
		IsGroup:         isGroup,
		Now:             time.Now(),
	})
	if err != nil {
		d.logger.Warn("could not interpret a command", "sender", res.PeerAccountID, "error", err)
		return
	}

	switch {
	case intent.Action != "":
		out, err := d.actions.Execute(ctx, intent.Action, intent.Params, res.PeerAccountID, chatID)
		if err != nil {
			// Named actions that do not exist are the interpreter's mistake
			// rather than the sender's, and the sender still gets something
			// they can act on. The detail goes to the log.
			d.logger.Warn("action failed", "action", intent.Action, "sender", res.PeerAccountID, "error", err)
			d.reply(ctx, chatID, "That did not work: "+err.Error())
			return
		}
		d.logger.Info("action carried out", "action", intent.Action, "sender", res.PeerAccountID)
		d.reply(ctx, chatID, out.Reply)
	case intent.Reply != "":
		d.reply(ctx, chatID, intent.Reply)
	}
}

// reply answers in the chat the request came from, and nowhere else.
//
// Deliberately not through a route: a route is where the bot *announces* things,
// and an answer belongs to the conversation that asked. Routing an answer would
// mean one person's question reaching a channel they never addressed.
func (d *daemon) reply(ctx context.Context, chatID, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	dest := outbound.Destination{Kind: outbound.KindPeer, ID: chatID}
	if client.IsGroupID(chatID) {
		dest.Kind = outbound.KindGroup
	}
	if err := d.sender.Deliver(ctx, dest, text); err != nil {
		// Not queued through the outbox: an answer that arrives an hour after
		// the question is worse than none, and the person asking is right there
		// to ask again.
		d.logger.Warn("could not answer", "chat", chatID, "error", err)
	}
}

// labelPairs renders labels for a log line: sorted, so the same message logs
// the same way twice rather than reshuffling with Go's map order.
func labelPairs(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, 0, len(labels))
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, " ")
}

// statusLine is what the `status` action answers with -- the same facts the
// control socket reports, in a shape somebody reads in a chat rather than
// parses.
func (d *daemon) statusLine() string {
	waiting, err := d.box.Len()
	if err != nil {
		waiting = -1
	}
	connected := "no"
	if d.isConnected() {
		connected = "yes"
	}
	return fmt.Sprintf("connected: %s\nqueued: %d\nup for: %s\nversion: %s",
		connected, waiting, time.Since(d.started).Round(time.Second), version)
}

func (d *daemon) handleStatus(context.Context, ipc.Request) (any, error) {
	waiting, err := d.box.Len()
	if err != nil {
		return nil, err
	}
	return ipc.StatusResponse{
		Address:    d.id.Address().String(),
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
		logger.Info("account ready", "address", id.Address().String())
		return
	}
	// Deliberately also on stderr, unstructured: a first start is often watched
	// by a person, and a JSON log line is the wrong shape for the one thing
	// they have to copy somewhere else.
	// Hyphenated here and only here: this is the one rendering a person reads off
	// a screen and types somewhere else, which is what the groups are for. The
	// address file and the logs keep the canonical form.
	fmt.Fprintf(os.Stderr, "\n  This bot registered as:\n\n      %s\n\n"+
		"  Invite that address to the group it should post in.\n"+
		"  It is also in %s.\n\n", id.Address().Display(), cfg.AddressFile())
	logger.Info("registered", "address", id.Address().String())
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

				d.onReceived(ctx, res)

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
func runUpkeep(ctx context.Context, d *daemon, wanted <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(d.cfg.MaintenanceInterval)
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
				d.logger.Debug("upkeep skipped, ran recently", "since", time.Since(last))
				continue
			}
			last = time.Now()
			d.upkeep(ctx)
		}
	}()
	return done
}

func (d *daemon) upkeep(ctx context.Context) {
	// The queue first: it holds everything that arrived while nothing was
	// listening, and leaving it would let it grow to the server's per-device
	// cap -- past which every sender to this bot starts being refused.
	report, err := d.client.Drain(ctx, client.ReceiveOptions{})
	if err != nil {
		d.logger.Warn("could not drain the queue", "error", err)
	} else {
		if len(report.Results) > 0 {
			d.logger.Info("queue drained", "handled", len(report.Results))
		}
		// Every drained envelope goes through the same follow-up the live
		// stream gives one. Counting them and moving on -- which is what this
		// did before -- meant a command sent while the bot was down was stored
		// and never answered, and an invitation that arrived in that window was
		// never seen.
		for _, res := range report.Results {
			d.onReceived(ctx, res)
		}
		for _, f := range report.Failures {
			d.logger.Warn("could not read a queued envelope",
				"message_id", f.MessageID, "sender", f.SenderAccountID,
				"acknowledged", f.Acknowledged, "error", f.Err)
		}
	}

	m := d.client.Maintain(ctx)
	d.logger.Debug("maintenance done",
		"prekeys_topped_up", m.PrekeysToppedUp,
		"debts_paid", m.DebtsPaid,
		"sessions_recovered", len(m.Recovered),
		"receipts_resent", m.ReceiptsResent,
	)
	for _, p := range m.Problems {
		d.logger.Warn("maintenance problem", "error", p)
	}
	// Said here because nothing else ever will: a member whose account is gone
	// keeps their row in the group until a moderator removes it, and this is
	// the only moment anything found out.
	for _, gone := range m.GoneMembers {
		d.logger.Warn("group member's account no longer exists", "account", gone)
	}
}
