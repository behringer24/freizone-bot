package outbound

import (
	"strings"
	"testing"
	"time"

	"github.com/behringer24/freizone-bot/internal/config"
)

func cfgWith(t *testing.T, group string, peers string) *config.Config {
	t.Helper()
	cfg, err := config.Load(func(k string) string {
		switch k {
		case "FREIZONE_BOT_SERVER":
			return "https://chat.example.org"
		case "FREIZONE_BOT_ROUTE_GROUP":
			return group
		case "FREIZONE_BOT_ROUTE_PEERS":
			return peers
		}
		return ""
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// The two routes are independent rather than alternatives: with both configured
// a message goes to both, which is how escalation is expressed -- the team
// channel *and* whoever is carrying the pager.
func TestBothRoutesReceiveByDefault(t *testing.T) {
	dests, err := resolve(cfgWith(t, "plfxcdsa42x4xe4zr2mju", "qlfxcdsa42x4xe4gwjcnu,qu0qmxckqmum0dv77pndv"), "", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(dests) != 3 {
		t.Fatalf("want group plus two peers, got %d: %+v", len(dests), dests)
	}
	if dests[0].Kind != KindGroup {
		t.Errorf("the group should come first, got %+v", dests[0])
	}
}

func TestOneRouteCanBeNamed(t *testing.T) {
	cfg := cfgWith(t, "plfxcdsa42x4xe4zr2mju", "qlfxcdsa42x4xe4gwjcnu,qu0qmxckqmum0dv77pndv")

	onlyGroup, err := resolve(cfg, RouteGroup, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(onlyGroup) != 1 || onlyGroup[0].Kind != KindGroup {
		t.Errorf("want just the group, got %+v", onlyGroup)
	}

	onlyPeers, err := resolve(cfg, RoutePeers, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(onlyPeers) != 2 {
		t.Errorf("want just the peers, got %+v", onlyPeers)
	}
}

// A typo in a route name has to be refused rather than quietly sending
// everywhere, or nowhere.
func TestAnUnknownRouteIsRefused(t *testing.T) {
	_, err := resolve(cfgWith(t, "plfxcdsa42x4xe4zr2mju", ""), "oncall", nil)
	if err == nil {
		t.Fatal("an unknown route must be refused")
	}
	if !strings.Contains(err.Error(), "oncall") {
		t.Errorf("the error should name what was asked for, got %q", err)
	}
}

// Naming a route that exists as a name but has nothing configured is its own
// case: the operator asked for something specific and it is not set up.
func TestANamedRouteWithNothingConfiguredIsRefused(t *testing.T) {
	_, err := resolve(cfgWith(t, "plfxcdsa42x4xe4zr2mju", ""), RoutePeers, nil)
	if err == nil {
		t.Fatal("asking for the peer route with no peers configured must be refused")
	}
}

func TestNoRouteAtAllIsRefused(t *testing.T) {
	if _, err := resolve(cfgWith(t, "", ""), "", nil); err == nil {
		t.Fatal("a message with nowhere to go must be refused")
	}
}

// What actually reaches a chat. One plain-text block, because that is all a
// Freizone message is -- there is no markup on the wire.
func TestRenderReadsAsOneBlock(t *testing.T) {
	now := time.Now().UTC()
	got := Message{
		Title:  "disk full on web01",
		Text:   "/ at 98%",
		Labels: map[string]string{LabelSeverity: "critical", LabelSource: "web01"},
		At:     now,
	}.Render(now)

	for _, want := range []string{"CRITICAL", "disk full on web01", "web01", "/ at 98%"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered message is missing %q:\n%s", want, got)
		}
	}
	// A fresh message must not carry a timestamp line: the chat already shows
	// when it arrived, and repeating it is noise on every single alert.
	if strings.Contains(got, "delivered late") {
		t.Errorf("a message sent immediately must not claim to be late:\n%s", got)
	}
}

// One that waited out a retry has to say so, or a reader takes an hour-old
// alert for a current one.
func TestALateMessageSaysSo(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour)
	got := Message{Title: "was queued a while", At: at}.Render(time.Now().UTC())
	if !strings.Contains(got, "delivered late") {
		t.Errorf("a message delivered an hour after the fact must say so:\n%s", got)
	}
}

func TestRenderNeedsNeitherTitleNorSeverity(t *testing.T) {
	got := Message{Text: "just the text"}.Render(time.Now().UTC())
	if got != "just the text" {
		t.Errorf("got %q", got)
	}
}

// The cap exists because one flapping service would otherwise page somebody a
// hundred times -- and a pager that cries wolf is the one people mute.
func TestTheRateCapStopsAtItsLimit(t *testing.T) {
	now := time.Now()
	l := NewLimiter(2, func() time.Time { return now })

	for i := range 2 {
		if ok, _ := l.Allow(); !ok {
			t.Fatalf("message %d should have been allowed", i+1)
		}
	}
	if ok, _ := l.Allow(); ok {
		t.Error("the third message in the window must be suppressed")
	}
	if l.Suppressed() != 1 {
		t.Errorf("suppressed count: want 1, got %d", l.Suppressed())
	}
}

// A cap that hides its own effect is indistinguishable from a bot that has
// died, so the next message through carries the count.
func TestWhatWasSuppressedIsReportedOnTheNextMessage(t *testing.T) {
	now := time.Now()
	l := NewLimiter(1, func() time.Time { return now })

	if ok, note := l.Allow(); !ok || note != "" {
		t.Fatalf("the first message goes with no note, got ok=%t note=%q", ok, note)
	}
	for range 3 {
		if ok, _ := l.Allow(); ok {
			t.Fatal("these must be suppressed")
		}
	}

	// Next window.
	now = now.Add(time.Minute)
	ok, note := l.Allow()
	if !ok {
		t.Fatal("a new window has to allow again")
	}
	if !strings.Contains(note, "3 further messages were suppressed") {
		t.Errorf("the note should say how many were missed, got %q", note)
	}
	// And it is reported once, not on every message afterwards.
	if _, again := l.Allow(); again != "" {
		t.Errorf("the count must not repeat, got %q", again)
	}
}

// Singular reads badly as "1 further messages", and an alerting tool's output is
// read under stress.
func TestOneSuppressedMessageReadsAsSingular(t *testing.T) {
	now := time.Now()
	l := NewLimiter(1, func() time.Time { return now })
	l.Allow()
	l.Allow() // suppressed

	now = now.Add(time.Minute)
	_, note := l.Allow()
	if !strings.Contains(note, "1 further message was suppressed") {
		t.Errorf("got %q", note)
	}
}

// resolve is Resolve with the group id already resolved, which is what the
// daemon passes. These tests configure whole ids, so a resolved id is the
// configured one -- the prefix case is the daemon's and is tested there.
func resolve(cfg *config.Config, route string, labels map[string]string) ([]Destination, error) {
	return Resolve(cfg, cfg.RouteGroup, route, labels)
}
