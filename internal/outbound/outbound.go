// Package outbound turns a message into destinations and delivers it.
//
// Named routes rather than "the alert group": a source addresses a route, and
// several capabilities share one delivery path. When the webhook receiver lands
// it becomes a second producer for the same queue, which is why none of this
// knows anything about alerts.
package outbound

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/behringer24/freizone-bot/internal/config"
	"github.com/behringer24/freizone-server/pkg/client"
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
}

func (d Destination) String() string { return string(d.Kind) + ":" + d.ID }

// Route names a configured set of destinations.
const (
	RouteGroup = "group"
	RoutePeers = "peers"
)

// Message is what gets delivered. Deliberately not called an alert: the same
// shape carries a scheduled report, a command's answer, and whatever a webhook
// brings later.
type Message struct {
	Title    string    `json:"title,omitempty"`
	Text     string    `json:"text,omitempty"`
	Severity string    `json:"severity,omitempty"`
	Source   string    `json:"source,omitempty"`
	At       time.Time `json:"at"`
}

// Render is what actually reaches a chat.
//
// One plain-text block, because that is all a Freizone message is -- there is no
// markup on the wire and inventing one here would only look like markup on the
// receiving side. The timestamp is included only when the message is being
// delivered late, so an ordinary message does not carry a line every reader can
// already see from the chat itself.
func (m Message) Render(now time.Time) string {
	var b strings.Builder

	if m.Severity != "" {
		b.WriteString("[" + strings.ToUpper(m.Severity) + "] ")
	}
	if m.Title != "" {
		b.WriteString(m.Title)
	}
	if m.Source != "" {
		if m.Title != "" {
			b.WriteString(" ")
		}
		b.WriteString("(" + m.Source + ")")
	}
	if m.Text != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(m.Text)
	}

	// Late is anything more than a couple of minutes old by the time it goes.
	// Said plainly rather than as a bare timestamp: a reader seeing an old alert
	// needs to know it is old, not to do arithmetic.
	if !m.At.IsZero() && now.Sub(m.At) > 2*time.Minute {
		b.WriteString(fmt.Sprintf("\n\n(from %s, delivered late)", m.At.UTC().Format("2006-01-02 15:04:05 UTC")))
	}
	return b.String()
}

// Resolve is where a message goes, given the configuration and an optional
// named route.
//
// The group and the peers are independent rather than alternatives: with both
// configured a message goes to both, which is how escalation is expressed --
// the team channel *and* whoever is carrying the pager.
func Resolve(cfg *config.Config, route string) ([]Destination, error) {
	var out []Destination

	wantGroup := route == "" || route == RouteGroup
	wantPeers := route == "" || route == RoutePeers

	if route != "" && !wantGroup && !wantPeers {
		return nil, fmt.Errorf("unknown route %q (known: %s, %s)", route, RouteGroup, RoutePeers)
	}
	if wantGroup && cfg.RouteGroup != "" {
		out = append(out, Destination{Kind: KindGroup, ID: cfg.RouteGroup})
	}
	if wantPeers {
		for _, peer := range cfg.RoutePeers {
			out = append(out, Destination{Kind: KindPeer, ID: peer})
		}
	}
	if len(out) == 0 {
		if route != "" {
			return nil, fmt.Errorf("route %q is not configured", route)
		}
		return nil, fmt.Errorf("no route configured")
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
		if _, err := s.c.StartConversation(ctx, d.ID, ""); err != nil {
			return fmt.Errorf("reaching %s: %w", d.ID, err)
		}
		_, err := s.c.SendText(ctx, d.ID, text, client.SendOptions{})
		return err
	default:
		return fmt.Errorf("unknown destination kind %q", d.Kind)
	}
}
