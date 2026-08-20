package command

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/behringer24/freizone-bot/internal/action"
)

var specs = []action.Spec{
	{Name: "help", Summary: "list what I can do"},
	{Name: "joke", Summary: "the joke of the day"},
	{Name: "echo", Summary: "say something back", Params: []action.Param{
		{Name: "what", Required: true},
	}},
	{Name: "note", Summary: "two parameters", Params: []action.Param{
		{Name: "topic", Required: true},
		{Name: "body", Required: true},
	}},
}

func interpret(t *testing.T, text string, isGroup bool) Intent {
	t.Helper()
	in := Input{Text: text, SenderAccountID: "qalice", ChatID: "qchat", IsGroup: isGroup, Now: time.Now()}
	intent, err := NewBuiltin(specs).Interpret(context.Background(), in)
	if err != nil {
		t.Fatalf("Interpret(%q): %v", text, err)
	}
	return intent
}

func TestASlashCommandNamesItsAction(t *testing.T) {
	if got := interpret(t, "/joke", false); got.Action != "joke" {
		t.Errorf("want the joke action, got %+v", got)
	}
	// The slash is optional one-to-one: there is no other traffic in that chat
	// to disambiguate from.
	if got := interpret(t, "joke", false); got.Action != "joke" {
		t.Errorf("a bare name should work one-to-one, got %+v", got)
	}
	if got := interpret(t, "  /JOKE  ", false); got.Action != "joke" {
		t.Errorf("case and surrounding space must not matter, got %+v", got)
	}
}

// In a group, only an explicit slash counts. People talk to each other there,
// and a bot answering an ordinary sentence would be noise at best.
func TestInAGroupOnlyASlashCounts(t *testing.T) {
	if got := interpret(t, "joke", true); got.Action != "" || got.Reply != "" {
		t.Errorf("a bare word in a group must mean nothing, got %+v", got)
	}
	if got := interpret(t, "does anyone know the status of this", true); got.Action != "" || got.Reply != "" {
		t.Errorf("conversation in a group must not be interpreted, got %+v", got)
	}
	if got := interpret(t, "/joke", true); got.Action != "joke" {
		t.Errorf("an explicit slash in a group is a command, got %+v", got)
	}
}

func TestParametersFillInOrder(t *testing.T) {
	got := interpret(t, "/note deploys we rolled back to 0.22", false)
	if got.Action != "note" {
		t.Fatalf("want the note action, got %+v", got)
	}
	if got.Params["topic"] != "deploys" {
		t.Errorf("topic: got %q", got.Params["topic"])
	}
	// The last parameter takes the rest, or an action with a free-text
	// parameter would only ever see its first word.
	if got.Params["body"] != "we rolled back to 0.22" {
		t.Errorf("body: got %q", got.Params["body"])
	}
}

func TestASingleParameterTakesEverything(t *testing.T) {
	got := interpret(t, "/echo hello there world", false)
	if got.Params["what"] != "hello there world" {
		t.Errorf("got %q", got.Params["what"])
	}
}

// A missing parameter is not the interpreter's business to refuse: the registry
// validates against the spec, and duplicating that here would mean two places
// to keep in agreement.
func TestAMissingParameterIsLeftToTheRegistry(t *testing.T) {
	got := interpret(t, "/echo", false)
	if got.Action != "echo" {
		t.Fatalf("the action should still be named, got %+v", got)
	}
	if len(got.Params) != 0 {
		t.Errorf("nothing should have been invented, got %+v", got.Params)
	}
}

// Somebody typed something at the bot and deserves to know it went nowhere --
// and naming what is available beats a bare refusal in a chat, where there is
// room for it.
func TestAnUnknownCommandGetsAHelpfulReply(t *testing.T) {
	got := interpret(t, "/teleport", false)
	if got.Action != "" {
		t.Errorf("nothing should have been named, got %q", got.Action)
	}
	if !strings.Contains(got.Reply, "teleport") || !strings.Contains(got.Reply, "/help") {
		t.Errorf("the reply should name what was tried and where to look, got %q", got.Reply)
	}
}

func TestEmptyTextMeansNothing(t *testing.T) {
	for _, text := range []string{"", "   ", "/", "  /  "} {
		got := interpret(t, text, false)
		if got.Action != "" || got.Reply != "" {
			t.Errorf("%q should mean nothing, got %+v", text, got)
		}
	}
}

// The seam that makes BOT-10 a drop-in: an interpreter is handed strings and
// returns strings. This test exists to fail if that ever stops being true --
// if Interpret grew access to a registry or a client, it could act rather than
// describe, and every check downstream would be trusting it.
func TestAnInterpreterCanOnlyDescribe(t *testing.T) {
	// The compiler is the real assertion here: NewBuiltin takes specs, not a
	// registry, so there is nothing to execute with even by mistake.
	var _ Interpreter = NewBuiltin(specs)

	intent := interpret(t, "/joke", false)
	// An Intent names an action; it does not carry one.
	if intent.Action != "joke" {
		t.Fatalf("got %+v", intent)
	}
	if intent.Params == nil {
		// Not a failure -- just pinning that params are plain strings, so
		// nothing executable can travel in one.
		intent.Params = map[string]string{}
	}
	for k, v := range intent.Params {
		if strings.TrimSpace(k) == "" {
			t.Errorf("an unnamed parameter reached the caller: %q", v)
		}
	}
}
