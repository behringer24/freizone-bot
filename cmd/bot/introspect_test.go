package main

import (
	"strings"
	"testing"

	"github.com/behringer24/freizone-bot/internal/config"
)

const (
	someGroup = "plfxcdsa42x4xe4zr2mju"
	somePeer  = "qlfxcdsa42x4xe4gwjcnu"
	otherPeer = "qu0qmxckqmum0dv77pndv"
)

func daemonWith(t *testing.T, env map[string]string) *daemon {
	t.Helper()
	cfg, err := config.Load(func(k string) string {
		switch k {
		case "FREIZONE_BOT_SERVER":
			return "https://chat.example.org"
		case "FREIZONE_BOT_STATE_DIR":
			return t.TempDir()
		}
		return env[k]
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return &daemon{cfg: cfg}
}

func TestListRecipientsNamesTheGroupAndEachPerson(t *testing.T) {
	d := daemonWith(t, map[string]string{
		"FREIZONE_BOT_ROUTE_GROUP": someGroup,
		"FREIZONE_BOT_ROUTE_PEERS": somePeer + "," + otherPeer + "*chat.example.org",
	})
	got := d.recipientLines()

	// Grouped for reading, since this answer is read off a screen and compared
	// against another one.
	if !strings.Contains(got, "plfxc-dsa42") {
		t.Errorf("the group should be there, hyphenated:\n%s", got)
	}
	if !strings.Contains(got, "qlfxc-dsa42") || !strings.Contains(got, "qu0qm-xckqm") {
		t.Errorf("both people should be there:\n%s", got)
	}
	// A federated recipient without its server is a different person, so the
	// server has to survive into the answer.
	if !strings.Contains(got, "*chat.example.org") {
		t.Errorf("the server of the federated recipient is missing:\n%s", got)
	}
	// No rules configured, so no caveat -- a caveat that is always there stops
	// being read.
	if strings.Contains(got, "/routes") {
		t.Errorf("nothing narrows this, so nothing should say it does:\n%s", got)
	}
}

// With rules configured, the list is who *can* receive rather than who does.
// Saying so is the difference between an answer and a misleading one.
func TestListRecipientsSaysWhenARuleCanNarrowIt(t *testing.T) {
	d := daemonWith(t, map[string]string{
		"FREIZONE_BOT_ROUTE_GROUP": someGroup,
		"FREIZONE_BOT_ROUTE_RULES": "severity:critical=group+peers",
	})
	if got := d.recipientLines(); !strings.Contains(got, "/routes") {
		t.Errorf("want a pointer at the rules:\n%s", got)
	}
}

func TestRoutesWithoutRules(t *testing.T) {
	d := daemonWith(t, map[string]string{"FREIZONE_BOT_ROUTE_GROUP": someGroup})
	got := d.routeLines()
	if !strings.Contains(got, "group") {
		t.Errorf("the configured route should be named:\n%s", got)
	}
	// The default is worth stating: somebody with no rules should not have to
	// infer what happens.
	if !strings.Contains(got, "everything goes to every route") {
		t.Errorf("want the no-rules default spelled out:\n%s", got)
	}
}

func TestRoutesListsTheRulesInOrder(t *testing.T) {
	d := daemonWith(t, map[string]string{
		"FREIZONE_BOT_ROUTE_GROUP": someGroup,
		"FREIZONE_BOT_ROUTE_PEERS": somePeer,
		"FREIZONE_BOT_ROUTE_RULES": "severity:critical=group+peers,kind:digest=group",
	})
	got := d.routeLines()

	if !strings.Contains(got, "first match wins") {
		t.Errorf("order is the whole semantics and should be said:\n%s", got)
	}
	critical := strings.Index(got, "severity=critical")
	digest := strings.Index(got, "kind=digest")
	if critical < 0 || digest < 0 {
		t.Fatalf("both rules should be listed:\n%s", got)
	}
	if critical > digest {
		// Printed in configured order, because the first match decides -- a
		// re-sorted list would describe a different bot.
		t.Errorf("the rules are out of order:\n%s", got)
	}
	// The two surprises, both stated rather than left to be found during an
	// incident.
	if !strings.Contains(got, "matching no rule goes everywhere") {
		t.Errorf("want the unmatched-message rule:\n%s", got)
	}
	if !strings.Contains(got, "explicit route beats the rules") {
		t.Errorf("want the precedence rule:\n%s", got)
	}
}

// Neither answer may leak a token, a path or anything else about the host. They
// are disclosure gated only by the commander allow-list, so what they disclose
// has to stay exactly the routing configuration.
func TestNeitherAnswerLeaksAnythingElse(t *testing.T) {
	d := daemonWith(t, map[string]string{
		"FREIZONE_BOT_ROUTE_GROUP":         someGroup,
		"FREIZONE_BOT_ROUTE_PEERS":         somePeer,
		"FREIZONE_BOT_COMMANDERS":          somePeer,
		"FREIZONE_BOT_WEBHOOK_ADDR":        "127.0.0.1:9095",
		"FREIZONE_BOT_WEBHOOK_TOKENS_FILE": "/etc/freizone-bot/senders",
	})
	for _, answer := range []string{d.recipientLines(), d.routeLines()} {
		for _, forbidden := range []string{"senders", "9095", d.cfg.StateDir, d.cfg.ControlSocket} {
			if forbidden != "" && strings.Contains(answer, forbidden) {
				t.Errorf("the answer mentions %q:\n%s", forbidden, answer)
			}
		}
	}
}
