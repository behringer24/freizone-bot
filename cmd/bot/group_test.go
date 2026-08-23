package main

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/behringer24/freizone-server/pkg/address"
)

const otherGroup = "pu0qmxckqmum0dv5nesvv"

func daemonForGroup(t *testing.T, configured string, held ...string) *daemon {
	t.Helper()
	d := daemonWith(t, map[string]string{"FREIZONE_BOT_ROUTE_GROUP": configured})
	d.logger = slog.New(slog.DiscardHandler)
	d.knownGroups = func() ([]string, error) { return held, nil }
	return d
}

// The case that started this: the compact form the app displays, pasted into
// the configuration. config.Load reduces it to a bare prefix, and this turns
// the prefix into the group.
func TestAConfiguredPrefixResolvesToTheGroup(t *testing.T) {
	d := daemonForGroup(t, someGroup[:address.PrefixLength]+"*chatcentral.de", otherGroup, someGroup)
	if err := d.resolveGroup(); err != nil {
		t.Fatalf("resolveGroup: %v", err)
	}
	if got := d.configuredGroup(); got != someGroup {
		t.Errorf("configuredGroup() = %q, want the whole id", got)
	}
}

func TestAWholeIDResolvesToItself(t *testing.T) {
	d := daemonForGroup(t, someGroup, someGroup)
	if err := d.resolveGroup(); err != nil {
		t.Fatalf("resolveGroup: %v", err)
	}
	if got := d.configuredGroup(); got != someGroup {
		t.Errorf("got %q", got)
	}
}

// Ordinary on a first run: the operator configures the group before the bot has
// been invited to it, or before the invitation has arrived. The configured value
// is kept and the invitation path finishes the job -- refusing to start here
// would break the very order the first run leads people into.
func TestNoMatchIsNotAnError(t *testing.T) {
	prefix := someGroup[:address.PrefixLength]
	d := daemonForGroup(t, prefix) // holds no groups at all
	if err := d.resolveGroup(); err != nil {
		t.Fatalf("resolveGroup: %v", err)
	}
	if got := d.configuredGroup(); got != prefix {
		t.Errorf("configuredGroup() = %q, want the configured prefix kept", got)
	}
	// And the invitation, which carries the whole id, is recognised as this one.
	if !d.isConfiguredGroup(someGroup) {
		t.Error("an invitation to the configured group should be recognised by its prefix")
	}
	if d.isConfiguredGroup(otherGroup) {
		t.Error("a different group must not match")
	}
}

// The one thing accepting prefixes genuinely needs guarding, and the reason it
// is an error rather than a choice: picking one silently would send somebody's
// messages to a group they did not mean.
func TestAnAmbiguousPrefixIsRefusedAndNamesBoth(t *testing.T) {
	// Two ids sharing a first character: "p" alone is a legal prefix to write,
	// and it matches every group.
	d := daemonForGroup(t, "p", someGroup, otherGroup)
	err := d.resolveGroup()
	if err == nil {
		t.Fatal("an ambiguous prefix must not be resolved to one of the matches")
	}
	for _, want := range []string{someGroup, otherGroup} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should name %q so the operator can choose: %q", want, err)
		}
	}
}

// A failure to read the group list is not a reason to refuse to start: the
// invitation path resolves the group anyway, and a bot that will not come up
// because one local read failed is worse than one that resolves a moment later.
func TestAFailedListingIsNotFatal(t *testing.T) {
	prefix := someGroup[:address.PrefixLength]
	d := daemonForGroup(t, prefix)
	d.knownGroups = func() ([]string, error) { return nil, errors.New("disk on fire") }

	if err := d.resolveGroup(); err != nil {
		t.Errorf("resolveGroup: %v", err)
	}
	if got := d.configuredGroup(); got != prefix {
		t.Errorf("got %q", got)
	}
}

func TestNoGroupConfiguredIsNothingToResolve(t *testing.T) {
	d := daemonForGroup(t, "")
	if err := d.resolveGroup(); err != nil {
		t.Errorf("resolveGroup: %v", err)
	}
	if got := d.configuredGroup(); got != "" {
		t.Errorf("got %q", got)
	}
	// And nothing matches, so an unsolicited invitation is not mistaken for the
	// configured one.
	if d.isConfiguredGroup(someGroup) {
		t.Error("with no group configured, nothing is the configured group")
	}
}
