// Package outbound turns a message into destinations and delivers it.
//
// Named routes rather than "the alert group": a source addresses a route, and
// several capabilities share one delivery path. When the webhook receiver lands
// it becomes a second producer for the same queue, which is why none of this
// knows anything about alerts.
//
// # Why labels rather than fields
//
// A message carries a title, a body, and labels. It deliberately does *not*
// carry a severity, a source, a host or a job as fields of their own, even
// though the first thing built on top of this was operations alerting and those
// are exactly what an alert has.
//
// Fields would have quietly made this an alerting tool. Everything else the bot
// is meant to carry -- a build result, a scheduled digest, an answer to a
// command, a reading from a device -- would have had to bend itself into
// alerting vocabulary, or grow a second set of fields beside it. Labels cost
// nothing and describe all of them: `severity=critical` and `repo=freizone-app`
// and `device=sensor-3` are the same shape.
//
// `severity` and `source` are *conventions* rather than special cases: the
// renderer gives them prominence because that is what people put there, and
// nothing breaks when they are absent or when something else is used instead.
package outbound

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/behringer24/freizone-bot/internal/config"
	"github.com/behringer24/freizone-server/pkg/address"
	"github.com/behringer24/freizone-server/pkg/client"
)

// Conventional label names. Not privileged in the data model -- only in how the
// renderer lays a message out, because a reader scanning a notification wants
// the urgency first and the origin next to the headline.
const (
	LabelSeverity = "severity"
	LabelSource   = "source"
)

// Kind says how a destination is addressed. A group and a person take different
// calls in the core, and the id alone does not always say which -- so it is
// recorded rather than guessed.
type Kind string

const (
	KindGroup Kind = "group"
	KindPeer  Kind = "peer"
)

// Destination is one place a message goes.
type Destination struct {
	Kind Kind   `json:"kind"`
	ID   string `json:"id"`

	// Server is where a peer lives, empty for this bot's own server. Carried on
	// the destination because an outbox entry outlives the configuration that
	// produced it: a message queued for a federated peer has to still know where
	// that peer is after a restart.
	Server string `json:"server,omitempty"`
}

// String identifies a destination in a log line, and is the key an outbox entry
// and the deduplicator are held under -- so it has to be the canonical form of
// an address rather than a rendering of one, or the same recipient spelled two
// ways would be two destinations.
func (d Destination) String() string {
	return string(d.Kind) + ":" + address.Address{ID: d.ID, Server: d.Server}.String()
}

// How long a message reaching a chat may be.
//
// One answer for every producer -- the CLI, the webhook, a command's reply --
// because "how much text belongs in a chat message" is one question however the
// text got here, and two answers to it would only differ by which code path
// somebody happened to look at.
//
// The protocol is not what bounds this: a Freizone server caps a request body at
// 512 KiB, which is orders of magnitude more than anything here. The bound is a
// person holding a phone. Generous enough for the case the README documents --
// piping twenty lines of a log in -- and no more.
const (
	MaxMessageChars = 4000
	MaxMessageLines = 60
)

// TrimToChatSize shortens text to something readable, and says when it did.
//
// Saying so is the part that matters. Silent truncation reads as a complete
// message, which is worse than a short one: somebody checking a list against
// reality has no way to know the list ended early.
func TrimToChatSize(s string) string {
	cutLines := false
	if lines := strings.Split(s, "\n"); len(lines) > MaxMessageLines {
		s = strings.Join(lines[:MaxMessageLines], "\n")
		cutLines = true
	}
	if len(s) > MaxMessageChars {
		// Back off to a rune boundary, so a truncated message is not invalid
		// UTF-8 -- which renders as a replacement character on somebody's phone.
		cut := MaxMessageChars
		for cut > 0 && s[cut]&0xC0 == 0x80 {
			cut--
		}
		return strings.TrimSpace(s[:cut]) + "\n\n(cut short)"
	}
	if cutLines {
		return s + "\n\n(cut short)"
	}
	return s
}

// ErrNoRoute reports that a message has nowhere to go -- an unknown route name,
// a route that is not configured, or no route at all. A sentinel because two
// producers now map it onto their own vocabulary: an exit code for the CLI, a
// status code for the webhook.
var ErrNoRoute = errors.New("no route")

// Route names a configured set of destinations.
const (
	RouteGroup = "group"
	RoutePeers = "peers"
)

// Message is what gets delivered.
type Message struct {
	// Title is the headline: what happened, in one line.
	Title string `json:"title,omitempty"`

	// Text is the detail underneath it.
	Text string `json:"text,omitempty"`

	// Labels describe the message for routing, deduplication and display.
	// Free-form on purpose -- whatever produced this message already has its own
	// vocabulary, and making it match one invented here would be work for
	// nobody's benefit.
	Labels map[string]string `json:"labels,omitempty"`

	// At is when the thing being reported happened, which is not necessarily
	// when it is delivered. A message that waited out a retry says so rather
	// than looking current.
	At time.Time `json:"at"`
}

// Label returns one label, or the empty string.
func (m Message) Label(name string) string {
	if m.Labels == nil {
		return ""
	}
	return m.Labels[name]
}

// Render is what actually reaches a chat.
//
// One plain-text block, because that is all a Freizone message is -- there is no
// markup on the wire, and inventing some here would only look like markup on the
// receiving side.
//
// The layout follows the two conventional labels and then lists the rest:
//
//	[CRITICAL] disk full on web01 (web01)
//
//	/ at 98%
//
//	job=disk-check region=eu
//
// Nothing here is required. A message with no labels renders as a title and a
// body, which is what a command's answer or a digest looks like.
func (m Message) Render(now time.Time) string {
	var b strings.Builder

	if sev := m.Label(LabelSeverity); sev != "" {
		b.WriteString("[" + strings.ToUpper(sev) + "] ")
	}
	if m.Title != "" {
		b.WriteString(m.Title)
	}
	if src := m.Label(LabelSource); src != "" {
		if m.Title != "" {
			b.WriteString(" ")
		}
		b.WriteString("(" + src + ")")
	}
	if m.Text != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(m.Text)
	}

	// The remaining labels, sorted so the same message always reads the same
	// way -- Go's map order would otherwise reshuffle them on every send and
	// make two identical messages look different.
	if rest := m.otherLabels(); len(rest) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.Join(rest, " "))
	}

	// Trimmed here, before the late note is appended, so that a message long
	// enough to be cut does not lose the one line explaining why it is old.
	out := TrimToChatSize(b.String())

	// Late is anything more than a couple of minutes old by the time it goes.
	// Said plainly rather than as a bare timestamp: a reader seeing something
	// old needs to know it is old, not to do arithmetic.
	if !m.At.IsZero() && now.Sub(m.At) > 2*time.Minute {
		out += fmt.Sprintf("\n\n(from %s, delivered late)", m.At.UTC().Format("2006-01-02 15:04:05 UTC"))
	}
	return out
}

// otherLabels is every label except the two the layout already showed, as
// sorted `key=value` strings.
func (m Message) otherLabels() []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(m.Labels)) {
		if k == LabelSeverity || k == LabelSource {
			continue
		}
		out = append(out, k+"="+m.Labels[k])
	}
	return out
}

// Resolve is where a message goes, given the configuration, an optional named
// route and the message's labels.
//
// The group and the peers are independent rather than alternatives: with both
// configured a message goes to both, which is how escalation is expressed --
// the team channel *and* whoever is carrying the pager.
//
// Three things can narrow that, in this order of authority:
//
//  1. An explicit route on the request. The caller asked for one thing.
//  2. A matching routing rule, which is the standing decision about what goes
//     where.
//  3. Nothing, meaning every configured route.
//
// The explicit route wins over the rules deliberately: somebody naming a route
// is doing something out of the ordinary on purpose, and having configuration
// override that would make the flag a suggestion.
func Resolve(cfg *config.Config, route string, labels map[string]string) ([]Destination, error) {
	var out []Destination

	wantGroup := route == "" || route == RouteGroup
	wantPeers := route == "" || route == RoutePeers

	if route != "" && !wantGroup && !wantPeers {
		return nil, fmt.Errorf("%w: unknown route %q (known: %s, %s)", ErrNoRoute, route, RouteGroup, RoutePeers)
	}

	var matched *config.RouteRule
	if route == "" {
		matched = cfg.MatchRoute(labels)
		if matched != nil {
			wantGroup = slices.Contains(matched.Routes, RouteGroup)
			wantPeers = slices.Contains(matched.Routes, RoutePeers)
		}
	}
	if wantGroup && cfg.RouteGroup != "" {
		out = append(out, Destination{Kind: KindGroup, ID: cfg.RouteGroup})
	}
	if wantPeers {
		peers, err := config.ParsePeers(cfg.RoutePeers)
		if err != nil {
			return nil, err
		}
		for _, p := range peers {
			out = append(out, Destination{Kind: KindPeer, ID: p.ID, Server: p.Server})
		}
	}
	if len(out) == 0 {
		switch {
		case route != "":
			return nil, fmt.Errorf("%w: route %q is not configured", ErrNoRoute, route)
		case matched != nil:
			// Distinguished from "nothing configured at all", because the fix is
			// different: here a routing rule and the configured routes disagree,
			// and an operator needs to know which of the two to change.
			return nil, fmt.Errorf(
				"%w: the rule %s=%s sends to a route this bot has no destination for -- "+
					"check FREIZONE_BOT_ROUTE_RULES against the configured routes",
				ErrNoRoute, matched.Label, matched.Value)
		default:
			return nil, fmt.Errorf("%w: none configured", ErrNoRoute)
		}
	}
	return out, nil
}

// Sender delivers one rendered message to one destination.
type Sender interface {
	Deliver(ctx context.Context, d Destination, text string) error
}

type coreSender struct{ c *client.Client }

// NewSender delivers through the shared client core.
func NewSender(c *client.Client) Sender { return &coreSender{c: c} }

func (s *coreSender) Deliver(ctx context.Context, d Destination, text string) error {
	switch d.Kind {
	case KindGroup:
		_, err := s.c.SendGroupText(ctx, d.ID, text, client.SendOptions{})
		return err
	case KindPeer:
		// StartConversation first for a peer this bot has never written to:
		// without it the send has no cached device and no session, and the core
		// would have to invent both mid-send. Cheap and idempotent once the
		// conversation exists.
		// The server is passed rather than left empty: empty means "our own", and
		// a federated recipient resolved against the wrong server is a 404 that
		// reads like a deleted account.
		if _, err := s.c.StartConversation(ctx, d.ID, d.Server); err != nil {
			return fmt.Errorf("reaching %s: %w", d.ID, err)
		}
		_, err := s.c.SendText(ctx, d.ID, text, client.SendOptions{})
		return err
	default:
		return fmt.Errorf("unknown destination kind %q", d.Kind)
	}
}
