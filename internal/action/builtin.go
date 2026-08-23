package action

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// The actions that ship. Every one of them only reads, and none of them touches
// anything outside this process -- which is the whole of BOT-05's remit: prove
// the command path end to end without giving anybody a way to change the world
// through a chat message.
//
// Nothing here runs a command on the host, and there is deliberately no
// configuration that would let an operator add one. Somebody will want
// `ACTION_restart=systemctl restart nginx`; that is remote code execution for
// whoever can get a message past the allow-list, and if it is ever built it
// needs the confirmation and audit machinery Spec.Sensitive exists for.

// StatusFunc reports what the daemon knows about itself, for the status action.
// A function rather than a struct so this package needs to know nothing about
// the daemon.
type StatusFunc func() string

// Builtins is what the daemon lends this package: functions that answer a
// question, and the jokes.
//
// Functions rather than the configuration itself, so this package stays unable
// to reach anything -- it can ask, and what comes back is a string. Any of them
// may be nil, and the action it feeds is then simply not registered: an action
// that exists and answers "not available" is worse than one that never appears
// in /help.
type Builtins struct {
	Status     StatusFunc
	Recipients StatusFunc
	Routes     StatusFunc
	Jokes      []string
}

// RegisterBuiltins adds the read-only action set.
func RegisterBuiltins(r *Registry, b Builtins) {
	r.Register(Spec{
		Name:    "help",
		Summary: "list what I can do",
	}, func(_ context.Context, _ Request) (Result, error) {
		return Result{Reply: helpText(r)}, nil
	})

	r.Register(Spec{
		Name:    "ping",
		Summary: "check that I am listening",
	}, func(_ context.Context, _ Request) (Result, error) {
		return Result{Reply: "pong"}, nil
	})

	// Three answers rather than one, because they are three different
	// questions: how am I, who gets what I send, and what decides which of them.
	for _, ask := range []struct {
		name, summary string
		answer        StatusFunc
	}{
		{"status", "how I am doing", b.Status},
		// Reading, not changing. There is deliberately no `/addrecipient` to go
		// with this -- see the rejected-work section of docs/ROADMAP.md: the
		// recipient list is configuration, and a chat command that edited it
		// would route around the review that configuration exists to have, while
		// turning "may message this bot" into "receives everything it will ever
		// say".
		{"listrecipients", "who I send to", b.Recipients},
		{"routes", "what decides where a message goes", b.Routes},
	} {
		if ask.answer == nil {
			continue
		}
		answer := ask.answer
		r.Register(Spec{Name: ask.name, Summary: ask.summary},
			func(_ context.Context, _ Request) (Result, error) {
				return Result{Reply: answer()}, nil
			})
	}

	if len(b.Jokes) > 0 {
		jokes := b.Jokes
		r.Register(Spec{
			Name:    "joke",
			Summary: "the joke of the day",
		}, func(_ context.Context, _ Request) (Result, error) {
			return Result{Reply: jokeOfTheDay(jokes, time.Now())}, nil
		})
	}
}

// helpText lists the registered actions, which is the only sensible thing for
// help to do: written by hand it would drift from what is actually registered,
// and the first time somebody noticed would be when they tried the action it
// promised.
func helpText(r *Registry) string {
	var b strings.Builder
	b.WriteString("I understand:\n")
	for _, s := range r.Specs() {
		b.WriteString("\n/" + s.Name)
		for _, p := range s.Params {
			if p.Required {
				b.WriteString(" <" + p.Name + ">")
			} else {
				b.WriteString(" [" + p.Name + "]")
			}
		}
		if s.Summary != "" {
			b.WriteString(" -- " + s.Summary)
		}
	}
	return b.String()
}

// jokeOfTheDay picks by the day rather than at random, because "of the day" is
// a promise: asking twice in an afternoon should not produce two different
// answers, and a person showing somebody else should get the same one.
func jokeOfTheDay(jokes []string, now time.Time) string {
	if len(jokes) == 0 {
		return ""
	}
	return jokes[now.UTC().YearDay()%len(jokes)]
}

// DefaultJokes is a small built-in set, so the action exists without anybody
// having to configure it.
//
// Deliberately short and deliberately dull. It is here to prove the command
// path carries something that is not an operations alert -- which is the whole
// argument for this bot not being a monitoring tool -- and a long list baked
// into a binary would be somebody else's taste shipped as a feature. Point
// FREIZONE_BOT_JOKES_FILE at your own.
var DefaultJokes = []string{
	"Why do programmers prefer dark mode? Because light attracts bugs.",
	"There are two hard problems in computing: cache invalidation, naming things, and off-by-one errors.",
	"A QA engineer walks into a bar. Orders a beer. Orders 0 beers. Orders -1 beers. Orders a lizard.",
	"It works on my machine, and my machine is going to production.",
	"I would tell you a joke about UDP, but you might not get it.",
	"The best thing about a boolean is that even if you are wrong, you are only off by a bit.",
	"Debugging: being the detective in a crime film where you are also the murderer.",
}

// LoadJokes reads one per line from path, ignoring blanks and # comments.
// An empty path returns the built-in set.
func LoadJokes(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return DefaultJokes, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var out []string
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s holds no lines to say", path)
	}
	return out, nil
}
