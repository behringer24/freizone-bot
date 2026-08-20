package config

import (
	"strings"
	"testing"

	"github.com/behringer24/freizone-server/pkg/address"
)

// Real, checksum-valid ids. A made-up string would make these pass for the
// wrong reason, which is how this package first shipped a peer route that could
// not reach another server.
const (
	acctA  = "qlfxcdsa42x4xe4gwjcnu"
	acctB  = "qu0qmxckqmum0dv77pndv"
	groupA = "plfxcdsa42x4xe4zr2mju"
)

// The address *format* -- the star, the local form, scheme defaulting, what
// counts as one server -- is pkg/address's, and is tested there. What is tested
// here is only what this package decides on top of it. One case for the wiring,
// so a parser that was never actually called still shows up:
func TestTheFormatReachesUs(t *testing.T) {
	got, err := ParsePeer("  " + address.FormatForDisplay(acctA) + "*chat.example.org  ")
	if err != nil {
		t.Fatalf("ParsePeer: %v", err)
	}
	if got.ID != acctA || got.Server != "https://chat.example.org" {
		t.Errorf("got %+v", got)
	}
}

// The bot's own rule, and the reason this is not just address.Parse: that one
// accepts a prefix, because interactive completion needs it. A configuration
// file is not typed under time pressure, and a truncated id resolving to
// whoever happens to match is how a message reaches a stranger.
func TestARecipientMustBeComplete(t *testing.T) {
	for _, raw := range []string{acctA[:5], acctA[:12], acctA[:5] + "*chat.example.org"} {
		if _, err := ParsePeer(raw); err == nil {
			t.Errorf("ParsePeer(%q) should have been refused", raw)
		}
	}
}

func TestARouteWantsTheRightKindOfID(t *testing.T) {
	// A group listed as a peer would be addressed as a person and fail
	// somewhere far from the line that got it wrong.
	for _, raw := range []string{groupA, groupA + "*chat.example.org"} {
		_, err := ParsePeer(raw)
		if err == nil {
			t.Errorf("ParsePeer(%q) should have been refused", raw)
			continue
		}
		if !strings.Contains(err.Error(), "group belongs in the group route") {
			t.Errorf("ParsePeer(%q) = %q, want it to say where a group goes", raw, err)
		}
	}

	// And the mirror, which is the likelier mistake of the two: the ids look
	// alike and only the first character differs.
	if _, err := ParseGroupID(acctA); err == nil || !strings.Contains(err.Error(), "not a group id") {
		t.Errorf("an account id in the group route: got %v", err)
	}

	// A group is not reached through a server -- its id comes from its own root
	// key -- so an address here is a misunderstanding worth naming rather than a
	// server silently ignored.
	if _, err := ParseGroupID(groupA + "*chat.example.org"); err == nil {
		t.Error("an address in the group route should be refused")
	}

	got, err := ParseGroupID(address.FormatForDisplay(groupA))
	if err != nil || got != groupA {
		t.Errorf("ParseGroupID = %q, %v", got, err)
	}
	if _, err := ParseGroupID(""); err != nil {
		t.Errorf("no group route configured is not an error: %v", err)
	}
}

// All-or-nothing: a bot that came up with three of four recipients would deliver
// to three people and look like it was working.
func TestOneBadEntrySpoilsTheWholeList(t *testing.T) {
	_, err := ParsePeers([]string{acctA, "nonsense", acctB})
	if err == nil {
		t.Fatal("want a failure")
	}
	if !strings.Contains(err.Error(), "nonsense") {
		t.Errorf("the error should name the bad entry, got %q", err)
	}
}

func TestADuplicateRecipientIsRefused(t *testing.T) {
	for _, pair := range [][2]string{
		{acctA, acctA},
		// Same account, two spellings of the id.
		{acctA, address.FormatForDisplay(acctA)},
		// Same account, two spellings of "our own server".
		{acctA, acctA + "*local"},
		// Same account and server, differing only in the scheme -- which is how
		// this bot happens to reach that server, not part of its identity. A
		// check comparing rendered strings would let this one through and
		// deliver twice.
		{acctA + "*chat.example.org", acctA + "*http://chat.example.org"},
	} {
		if _, err := ParsePeers(pair[:]); err == nil {
			t.Errorf("%q and %q are one recipient and should have been refused", pair[0], pair[1])
		}
	}

	// The same account on two genuinely different servers is two recipients --
	// a federated namespace means the id alone does not identify anybody.
	if _, err := ParsePeers([]string{acctA, acctA + "*chat.example.org"}); err != nil {
		t.Errorf("different servers are different recipients: %v", err)
	}
	if _, err := ParsePeers([]string{acctA + "*box.lan:18080", acctA + "*box.lan:18081"}); err != nil {
		t.Errorf("different ports are different recipients: %v", err)
	}
}

func TestAnEmptyListIsFine(t *testing.T) {
	// A bot that only routes to a group has no peers, and that is not an error.
	got, err := ParsePeers(nil)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}
