// Package command turns a message into an intention. It cannot carry one out.
//
// That split is the whole design. An Interpreter is handed four strings and a
// clock and returns a name and some more strings. It holds no client, no action
// registry, no configuration and no connection -- so whatever it is, and
// however it was persuaded, the most it can produce is a value somebody else
// then checks.
//
// Today the only implementation is a parser, and the seam looks like
// ceremony. It is what makes a model-driven interpreter a drop-in later
// (BOT-10): one constructor call changes, and every check downstream is
// unchanged because none of them ever trusted this layer.
package command

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/behringer24/freizone-bot/internal/action"
)

// Input is one message to make sense of.
type Input struct {
	Text string

	// SenderAccountID and ChatID come from the receive path, never from the
	// text: a sender cannot claim to be somebody else.
	SenderAccountID string
	ChatID          string
	IsGroup         bool

	Now time.Time
}

// Intent is what a message turned out to mean.
type Intent struct {
	// Action names a registered action, or is empty.
	Action string
	Params map[string]string

	// Reply is free text for when there is nothing to do -- an interpreter
	// saying "I did not understand that", or a model answering conversationally.
	// It is only ever sent back to whoever asked, in the chat they asked from.
	Reply string
}

// Interpreter makes sense of a message.
type Interpreter interface {
	Interpret(ctx context.Context, in Input) (Intent, error)
}

// builtin is the deterministic interpreter: a slash command, its name, and
// positional arguments filling the spec's parameters in order.
type builtin struct {
	specs []action.Spec
}

// NewBuiltin builds a parser from the same specs a model-driven interpreter
// would turn into tool definitions.
//
// Taking the specs rather than the registry is deliberate: this cannot execute
// anything, and being unable to is easier to see when it never had the means.
func NewBuiltin(specs []action.Spec) Interpreter { return &builtin{specs: specs} }

// Interpret reads `/name arg arg`, or a bare `name arg arg`.
//
// The leading slash is optional because a one-to-one chat with a bot has no
// other traffic to disambiguate from -- and required nowhere, because a group
// does: a plain word at the start of a sentence should not command anything.
// That distinction is the caller's to make via [Input.IsGroup], and it does.
func (b *builtin) Interpret(_ context.Context, in Input) (Intent, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return Intent{}, nil
	}

	// In a group, only an explicit slash counts. Anything else is people
	// talking to each other, and a bot answering that would be noise at best.
	if in.IsGroup && !strings.HasPrefix(text, "/") {
		return Intent{}, nil
	}
	text = strings.TrimPrefix(text, "/")

	fields := strings.Fields(text)
	if len(fields) == 0 {
		return Intent{}, nil
	}
	name := strings.ToLower(fields[0])
	args := fields[1:]

	spec, ok := b.specFor(name)
	if !ok {
		// Deliberately a reply rather than an error: somebody typed something
		// at the bot and deserves to know it went nowhere. Naming what *is*
		// available beats a bare refusal, and this is a chat -- there is room.
		return Intent{Reply: fmt.Sprintf(
			"I do not know %q. Try /help for what I can do.", name)}, nil
	}

	// Positional, in the order the spec lists them, with the last parameter
	// taking whatever is left. That last part matters: an action with a
	// free-text parameter would otherwise only ever see its first word.
	params := map[string]string{}
	for i, p := range spec.Params {
		if i >= len(args) {
			break
		}
		if i == len(spec.Params)-1 {
			params[p.Name] = strings.Join(args[i:], " ")
			break
		}
		params[p.Name] = args[i]
	}
	return Intent{Action: spec.Name, Params: params}, nil
}

func (b *builtin) specFor(name string) (action.Spec, bool) {
	for _, s := range b.specs {
		if s.Name == name {
			return s, true
		}
	}
	return action.Spec{}, false
}
