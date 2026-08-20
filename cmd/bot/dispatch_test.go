package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/behringer24/freizone-bot/internal/action"
	"github.com/behringer24/freizone-bot/internal/authz"
	"github.com/behringer24/freizone-bot/internal/command"
	"github.com/behringer24/freizone-bot/internal/config"
	"github.com/behringer24/freizone-bot/internal/outbound"
	"github.com/behringer24/freizone-server/pkg/client"
)

// recordingInterpreter answers nothing and remembers whether it was asked.
//
// That is the whole point of it: the invariant under test is not what the
// interpreter *does* with a stranger's text, it is that the interpreter never
// sees it.
type recordingInterpreter struct {
	mu     sync.Mutex
	called int
	texts  []string
}

func (r *recordingInterpreter) Interpret(_ context.Context, in command.Input) (command.Intent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called++
	r.texts = append(r.texts, in.Text)
	return command.Intent{}, nil
}

func (r *recordingInterpreter) seen() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.called
}

// recordingSender remembers what was sent where, instead of sending it.
type recordingSender struct {
	mu   sync.Mutex
	sent []outbound.Destination
	text []string
}

func (s *recordingSender) Deliver(_ context.Context, d outbound.Destination, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, d)
	s.text = append(s.text, text)
	return nil
}

func (s *recordingSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

// testDaemon is the smallest daemon dispatch needs.
func testDaemon(t *testing.T, commanders []string, allowGroup bool) (*daemon, *recordingInterpreter, *recordingSender) {
	t.Helper()

	cfg, err := config.Load(func(k string) string {
		switch k {
		case "FREIZONE_BOT_SERVER":
			return "https://chat.example.org"
		case "FREIZONE_BOT_STATE_DIR":
			return t.TempDir()
		}
		return ""
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	interp := &recordingInterpreter{}
	sender := &recordingSender{}
	reg := action.NewRegistry()
	reg.Register(action.Spec{Name: "ping"}, func(context.Context, action.Request) (action.Result, error) {
		return action.Result{Reply: "pong"}, nil
	})

	return &daemon{
		cfg:         cfg,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		sender:      sender,
		policy:      authz.New(commanders, allowGroup),
		interpreter: interp,
		actions:     reg,
	}, interp, sender
}

func textFrom(sender string) client.ReceiveResult {
	return client.ReceiveResult{
		PeerAccountID: sender,
		Content:       client.Content{Kind: client.ContentText, Text: "/ping"},
	}
}

// **The most important test in this repository.**
//
// A sender who may not command the bot must have their text never reach the
// interpreter -- not "reach it and be refused afterwards". Today the
// interpreter is a parser and the difference is invisible; the moment a model
// sits there, an interpreter that sees everything is one that anyone knowing
// this bot's address can write prompts for.
func TestUnauthorisedTextNeverReachesTheInterpreter(t *testing.T) {
	d, interp, sender := testDaemon(t, []string{"qalice"}, false)

	d.dispatch(context.Background(), textFrom("qstranger"))

	if interp.seen() != 0 {
		t.Fatalf("a stranger's text reached the interpreter %d times", interp.seen())
	}
	// And silence, not a refusal: a refusal is an oracle confirming something is
	// here and listening, and an amplification vector besides.
	if sender.count() != 0 {
		t.Errorf("a stranger must get no answer at all, got %d", sender.count())
	}
}

func TestAnAuthorisedSenderIsInterpretedAndAnswered(t *testing.T) {
	d, interp, sender := testDaemon(t, []string{"qalice"}, false)

	// The recording interpreter returns an empty intent, so drive the action
	// path with one that names something.
	d.interpreter = command.NewBuiltin(d.actions.Specs())
	d.dispatch(context.Background(), textFrom("qalice"))

	if sender.count() != 1 {
		t.Fatalf("want one answer, got %d", sender.count())
	}
	if sender.text[0] != "pong" {
		t.Errorf("answer: got %q", sender.text[0])
	}
	// The answer goes to the chat that asked, and nowhere else -- not through a
	// route, which is where the bot announces things.
	if sender.sent[0].ID != "qalice" || sender.sent[0].Kind != outbound.KindPeer {
		t.Errorf("answered the wrong place: %+v", sender.sent[0])
	}
	_ = interp
}

// With no commanders configured there is no command surface at all, and the
// interpreter is not even built -- so nothing here should run.
func TestWithNoCommandersNothingIsInterpreted(t *testing.T) {
	d, interp, sender := testDaemon(t, nil, false)
	// This is what runDaemon does: no policy means no interpreter.
	d.interpreter = nil

	d.dispatch(context.Background(), textFrom("qalice"))

	if interp.seen() != 0 || sender.count() != 0 {
		t.Error("with commands disabled nothing may be interpreted or answered")
	}
}

// Group commands are off by default: a command in a group is visible to
// everyone in it, and the membership drifts without the operator being told.
func TestAGroupMessageIsIgnoredUnlessGroupCommandsAreOn(t *testing.T) {
	fromGroup := func(sender string) client.ReceiveResult {
		return client.ReceiveResult{
			PeerAccountID: sender,
			Content:       client.Content{Kind: client.ContentGroupText, Text: "/ping"},
			Group:         &client.GroupOutcome{GroupID: "qgroup1"},
		}
	}

	off, interp, sender := testDaemon(t, []string{"qalice"}, false)
	off.dispatch(context.Background(), fromGroup("qalice"))
	if interp.seen() != 0 || sender.count() != 0 {
		t.Error("a group command must be ignored by default, even from a listed account")
	}

	on, onInterp, _ := testDaemon(t, []string{"qalice"}, true)
	on.dispatch(context.Background(), fromGroup("qalice"))
	if onInterp.seen() != 1 {
		t.Errorf("with group commands on it should be interpreted, saw %d", onInterp.seen())
	}
}

// A duplicate has already been acted on, and a blocked sender is somebody the
// operator cut off. Neither may reach the interpreter -- the second especially,
// since blocking that only stops *some* paths is not blocking.
func TestDuplicatesAndBlockedSendersAreDroppedBeforeInterpretation(t *testing.T) {
	d, interp, _ := testDaemon(t, []string{"qalice"}, false)

	dup := textFrom("qalice")
	dup.Duplicate = true
	d.dispatch(context.Background(), dup)

	blocked := textFrom("qalice")
	blocked.Blocked = true
	d.dispatch(context.Background(), blocked)

	if interp.seen() != 0 {
		t.Errorf("neither a duplicate nor a blocked sender may be interpreted, saw %d", interp.seen())
	}
}

// Receipts, re-key envelopes and group control traffic are machinery, not
// something anybody typed.
func TestNonTextContentIsNotInterpreted(t *testing.T) {
	d, interp, _ := testDaemon(t, []string{"qalice"}, false)

	for _, kind := range []client.ContentKind{
		client.ContentReceipt, client.ContentRekey, client.ContentGroupControl,
	} {
		res := textFrom("qalice")
		res.Content.Kind = kind
		d.dispatch(context.Background(), res)
	}
	if interp.seen() != 0 {
		t.Errorf("machinery must not be read as commands, saw %d", interp.seen())
	}
}

// --- group invitations -----------------------------------------------------

// joinRecorder stands in for the core's AcceptGroupInvitation, which needs a
// real account and a server. What is under test is the *decision*, not the
// protocol call -- the protocol call is pkg/client's and has its own tests.
type joinRecorder struct {
	mu     sync.Mutex
	joined []string
}

func (j *joinRecorder) accept(_ context.Context, groupID string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.joined = append(j.joined, groupID)
	return nil
}

func (j *joinRecorder) count() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.joined)
}

func invitation(groupID, from string) client.ReceiveResult {
	return client.ReceiveResult{
		PeerAccountID: from,
		Content:       client.Content{Kind: client.ContentGroupControl},
		Group:         &client.GroupOutcome{GroupID: groupID, Invited: true},
	}
}

// A group membership is not real until the invited account says so. Nothing in
// this bot ever said it before this item, which meant a configured group route
// could not work at all: the bot sat invited for ever and its messages went to a
// group it was not in.
func TestAnInvitationToTheConfiguredGroupIsAccepted(t *testing.T) {
	d, _, _ := testDaemon(t, nil, false)
	d.cfg.RouteGroup = "pgroup1"
	rec := &joinRecorder{}
	d.acceptInvitation = rec.accept

	d.maybeJoinGroup(context.Background(), invitation("pgroup1", "qfounder"))

	if rec.count() != 1 || rec.joined[0] != "pgroup1" {
		t.Fatalf("the configured group should have been joined, got %+v", rec.joined)
	}
}

// Anything else is a stranger deciding what this bot is a member of -- and from
// then on it holds that group's facts and receives its traffic.
func TestAnInvitationToAnotherGroupIsIgnored(t *testing.T) {
	d, _, _ := testDaemon(t, nil, false)
	d.cfg.RouteGroup = "pgroup1"
	rec := &joinRecorder{}
	d.acceptInvitation = rec.accept

	d.maybeJoinGroup(context.Background(), invitation("psomebodyelse", "qstranger"))

	if rec.count() != 0 {
		t.Errorf("an unconfigured group must not be joined, got %+v", rec.joined)
	}
}

func TestWithTheOptInAnyInvitationIsAccepted(t *testing.T) {
	d, _, _ := testDaemon(t, nil, false)
	d.cfg.RouteGroup = "pgroup1"
	d.cfg.AcceptGroupInvites = true
	rec := &joinRecorder{}
	d.acceptInvitation = rec.accept

	d.maybeJoinGroup(context.Background(), invitation("psomebodyelse", "qstranger"))

	if rec.count() != 1 {
		t.Errorf("with the opt-in it should have joined, got %+v", rec.joined)
	}
}

// Ordinary group traffic is not an invitation, and re-joining on every message
// would be a signed fact per message.
func TestOnlyAnActualInvitationTriggersAJoin(t *testing.T) {
	d, _, _ := testDaemon(t, nil, false)
	d.cfg.RouteGroup = "pgroup1"
	rec := &joinRecorder{}
	d.acceptInvitation = rec.accept

	notAnInvite := invitation("pgroup1", "qfounder")
	notAnInvite.Group.Invited = false
	d.maybeJoinGroup(context.Background(), notAnInvite)

	// And a one-to-one message carries no group at all.
	d.maybeJoinGroup(context.Background(), textFrom("qalice"))

	if rec.count() != 0 {
		t.Errorf("nothing here was an invitation, got %+v", rec.joined)
	}
}

// An invitation must be answered whether it came down the live stream or was
// found in the queue on reconnect. It arrives while the bot is down at least as
// often as while it is up.
func TestAnInvitationFoundInTheQueueIsAlsoAnswered(t *testing.T) {
	d, _, _ := testDaemon(t, nil, false)
	d.cfg.RouteGroup = "pgroup1"
	rec := &joinRecorder{}
	d.acceptInvitation = rec.accept

	// onReceived is what both paths call.
	d.onReceived(context.Background(), invitation("pgroup1", "qfounder"))

	if rec.count() != 1 {
		t.Errorf("a queued invitation must be answered too, got %+v", rec.joined)
	}
}
