package outbound

import (
	"strings"
	"testing"
	"time"

	"github.com/behringer24/freizone-bot/internal/config"
)

func cfgRouted(t *testing.T, severityRoutes string) *config.Config {
	t.Helper()
	cfg, err := config.Load(func(k string) string {
		switch k {
		case "FREIZONE_BOT_SERVER":
			return "https://chat.example.org"
		case "FREIZONE_BOT_ROUTE_GROUP":
			return "qgroup"
		case "FREIZONE_BOT_ROUTE_PEERS":
			return "qpeer1,qpeer2"
		case "FREIZONE_BOT_SEVERITY_ROUTES":
			return severityRoutes
		}
		return ""
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// The escalation case this exists for: everything lands in the team channel,
// and only the serious thing also wakes whoever is carrying the pager.
func TestSeverityDecidesWhoIsWoken(t *testing.T) {
	cfg := cfgRouted(t, "critical=group+peers,warning=group")

	critical, err := Resolve(cfg, "", "critical")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(critical) != 3 {
		t.Errorf("critical should reach the group and both peers, got %+v", critical)
	}

	warning, err := Resolve(cfg, "", "warning")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(warning) != 1 || warning[0].Kind != KindGroup {
		t.Errorf("warning should reach the group only, got %+v", warning)
	}
}

// A severity nobody mapped goes everywhere, which is the behaviour without any
// mapping at all. Silently dropping an unmapped severity would be the worst
// possible reading of a partial configuration.
func TestAnUnmappedSeverityStillGoesEverywhere(t *testing.T) {
	dests, err := Resolve(cfgRouted(t, "critical=peers"), "", "notice")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(dests) != 3 {
		t.Errorf("an unmapped severity must not be narrowed, got %+v", dests)
	}
}

// Case should not decide whether somebody gets woken up.
func TestSeverityMatchingIgnoresCase(t *testing.T) {
	dests, err := Resolve(cfgRouted(t, "critical=peers"), "", "CRITICAL")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(dests) != 2 {
		t.Errorf("CRITICAL should match the critical rule, got %+v", dests)
	}
}

// An explicit route beats the mapping: somebody typing -route is doing something
// out of the ordinary on purpose, and configuration overriding that would make
// the flag a suggestion.
func TestAnExplicitRouteOverridesTheMapping(t *testing.T) {
	dests, err := Resolve(cfgRouted(t, "critical=group"), RoutePeers, "critical")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(dests) != 2 || dests[0].Kind != KindPeer {
		t.Errorf("the flag should win over the mapping, got %+v", dests)
	}
}

// A mapping pointing at a route that is not configured is a real
// misconfiguration, and the error has to say which of the two to change.
func TestAMappingToAnUnconfiguredRouteSaysWhichToFix(t *testing.T) {
	cfg, err := config.Load(func(k string) string {
		switch k {
		case "FREIZONE_BOT_SERVER":
			return "https://chat.example.org"
		case "FREIZONE_BOT_ROUTE_GROUP":
			return "qgroup" // no peers configured
		case "FREIZONE_BOT_SEVERITY_ROUTES":
			return "critical=peers"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	_, err = Resolve(cfg, "", "critical")
	if err == nil {
		t.Fatal("a severity routed nowhere reachable must be refused")
	}
	if !strings.Contains(err.Error(), "SEVERITY_ROUTES") {
		t.Errorf("the error should name the setting to check, got %q", err)
	}
}

// A typo in a route name has to be caught at load time, not during an incident.
func TestABadSeverityMappingIsRefusedAtLoad(t *testing.T) {
	for _, raw := range []string{"critical", "critical=", "=group", "critical=oncall"} {
		_, err := config.Load(func(k string) string {
			switch k {
			case "FREIZONE_BOT_SERVER":
				return "https://chat.example.org"
			case "FREIZONE_BOT_SEVERITY_ROUTES":
				return raw
			}
			return ""
		})
		if err == nil {
			t.Errorf("%q should have been refused", raw)
		}
	}
}

// --- deduplication ---------------------------------------------------------

func TestDedupIsOffByDefault(t *testing.T) {
	d := NewDeduper(0, time.Now)
	if d.Enabled() {
		t.Fatal("a zero window means off")
	}
	m := Message{Title: "same"}
	for range 5 {
		if ok, _ := d.Allow(m, ""); !ok {
			t.Fatal("with dedup off nothing may be suppressed")
		}
	}
}

// The flapping-check case: one page, then a count, instead of a page every
// thirty seconds.
func TestARepeatInsideTheWindowIsSuppressed(t *testing.T) {
	now := time.Now()
	d := NewDeduper(5*time.Minute, func() time.Time { return now })
	m := Message{Severity: "warning", Title: "nginx flapping", Source: "web01"}

	if ok, _ := d.Allow(m, ""); !ok {
		t.Fatal("the first one has to go")
	}
	for range 4 {
		now = now.Add(30 * time.Second)
		if ok, _ := d.Allow(m, ""); ok {
			t.Fatal("a repeat inside the window must be suppressed")
		}
	}
	if d.Suppressed() != 4 {
		t.Errorf("suppressed: want 4, got %d", d.Suppressed())
	}

	// Once the window has passed it goes again, carrying what was swallowed.
	now = now.Add(5 * time.Minute)
	ok, note := d.Allow(m, "")
	if !ok {
		t.Fatal("past the window it has to go again")
	}
	if !strings.Contains(note, "4 identical messages were suppressed") {
		t.Errorf("the note should report the count, got %q", note)
	}
	if !strings.Contains(note, "since") {
		t.Errorf("the note should say since when, got %q", note)
	}
}

// The body is deliberately not part of the key: it routinely carries a
// timestamp or a load average that differs every time while the alert is
// plainly the same one. Hashing it would make this do nothing in exactly the
// cases it exists for.
func TestTheBodyDoesNotMakeAMessageDifferent(t *testing.T) {
	now := time.Now()
	d := NewDeduper(time.Minute, func() time.Time { return now })

	first := Message{Severity: "warning", Title: "load high", Source: "web01", Text: "load 8.1"}
	second := first
	second.Text = "load 8.4"

	if ok, _ := d.Allow(first, ""); !ok {
		t.Fatal("the first one has to go")
	}
	if ok, _ := d.Allow(second, ""); ok {
		t.Error("a differing body must not make it a new alert")
	}
}

// Different alerts are not each other's duplicates, however close together.
func TestDifferentAlertsAreIndependent(t *testing.T) {
	now := time.Now()
	d := NewDeduper(time.Minute, func() time.Time { return now })

	if ok, _ := d.Allow(Message{Title: "disk full"}, ""); !ok {
		t.Fatal("first")
	}
	if ok, _ := d.Allow(Message{Title: "nginx down"}, ""); !ok {
		t.Error("a different alert must not be suppressed by an unrelated one")
	}
}

// Only the caller knows whether two alerts are the same incident: identical text
// can be separate events, and differing text can be one.
func TestAnExplicitKeyDecides(t *testing.T) {
	now := time.Now()
	d := NewDeduper(time.Minute, func() time.Time { return now })

	// Different titles, same incident.
	if ok, _ := d.Allow(Message{Title: "disk 91%"}, "disk-web01"); !ok {
		t.Fatal("first")
	}
	if ok, _ := d.Allow(Message{Title: "disk 94%"}, "disk-web01"); ok {
		t.Error("the same key means the same incident, whatever the title")
	}

	// Same title, different incidents.
	if ok, _ := d.Allow(Message{Title: "disk full"}, "disk-web02"); !ok {
		t.Error("a different key means a different incident")
	}
}

func TestOneSuppressedRepeatReadsAsSingular(t *testing.T) {
	now := time.Now()
	d := NewDeduper(time.Minute, func() time.Time { return now })
	m := Message{Title: "once"}

	d.Allow(m, "")
	now = now.Add(10 * time.Second)
	d.Allow(m, "") // suppressed

	now = now.Add(time.Minute)
	_, note := d.Allow(m, "")
	if !strings.Contains(note, "1 identical message was suppressed") {
		t.Errorf("got %q", note)
	}
}

// A long-running daemon seeing many distinct alerts must not grow a map for
// ever -- but an entry still owing a count is never forgotten, because that
// number belongs to somebody.
func TestQuietKeysAreForgottenButOwedCountsAreNot(t *testing.T) {
	now := time.Now()
	d := NewDeduper(time.Minute, func() time.Time { return now })

	d.Allow(Message{Title: "quiet"}, "")
	d.Allow(Message{Title: "noisy"}, "")
	now = now.Add(10 * time.Second)
	d.Allow(Message{Title: "noisy"}, "") // suppressed: this one owes a count

	// Well past the eviction horizon for the quiet one.
	now = now.Add(30 * time.Minute)
	d.Allow(Message{Title: "trigger eviction"}, "")

	if d.Suppressed() != 1 {
		t.Errorf("the owed count must survive eviction, got %d", d.Suppressed())
	}
	// And the quiet key having been forgotten shows as it being allowed afresh
	// with no note.
	if ok, note := d.Allow(Message{Title: "quiet"}, ""); !ok || note != "" {
		t.Errorf("a forgotten key should look new, got ok=%t note=%q", ok, note)
	}
}
