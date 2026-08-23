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

// short is the compact form the app displays -- and therefore the form somebody
// is most likely to paste.
func short(id string) string { return id[:address.PrefixLength] }

// Every spelling the app shows is a spelling somebody will copy, so every one
// of them has to work. This is the test that says so.
func TestEverySpellingOfARecipient(t *testing.T) {
	for _, raw := range []string{
		acctA,
		address.FormatForDisplay(acctA),
		strings.ToUpper(acctA),
		"  " + acctA + "  ",
		short(acctA),
		short(acctA) + "*chat.example.org",
		acctA + "*chat.example.org",
		acctA + "*https://chat.example.org",
		acctA + "*http://box.lan:18081",
		acctA + "*local",
		acctA + "*",
	} {
		got, err := ParsePeer(raw)
		if err != nil {
			t.Errorf("ParsePeer(%q): %v", raw, err)
			continue
		}
		if !strings.HasPrefix(acctA, got.ID) && got.ID != acctA {
			t.Errorf("ParsePeer(%q).ID = %q, which is not this account", raw, got.ID)
		}
	}
}

func TestEverySpellingOfAGroup(t *testing.T) {
	for _, raw := range []string{
		groupA,
		address.FormatForDisplay(groupA),
		strings.ToUpper(groupA),
		short(groupA),
		// The one that started this: the compact form, with a server, for a
		// group. A group is not reached through a server -- but that form is
		// what the app displays, so it is accepted and the server discarded.
		short(groupA) + "*chatcentral.de",
		groupA + "*chatcentral.de",
		groupA + "*local",
		groupA + "*",
	} {
		got, err := ParseGroupID(raw)
		if err != nil {
			t.Errorf("ParseGroupID(%q): %v", raw, err)
			continue
		}
		if !strings.HasPrefix(groupA, got) {
			t.Errorf("ParseGroupID(%q) = %q, which is not this group", raw, got)
		}
	}

	if got, err := ParseGroupID(""); err != nil || got != "" {
		t.Errorf("no group route configured is not an error: %q, %v", got, err)
	}
}

// The version marker is the first character, so this one mistake is catchable
// however short the id is -- and it is the likely mistake, the two kinds of id
// differing in exactly that character.
func TestARouteStillWantsTheRightKindOfID(t *testing.T) {
	for _, raw := range []string{groupA, address.FormatForDisplay(groupA), groupA + "*chat.example.org"} {
		_, err := ParsePeer(raw)
		if err == nil {
			t.Errorf("ParsePeer(%q) should have been refused", raw)
			continue
		}
		if !strings.Contains(err.Error(), "group belongs in the group route") {
			t.Errorf("ParsePeer(%q) = %q, want it to say where a group goes", raw, err)
		}
	}
	if _, err := ParseGroupID(acctA); err == nil || !strings.Contains(err.Error(), "not a group id") {
		t.Errorf("ParseGroupID(%q) = %v", acctA, err)
	}
}

// A prefix passes the kind check and is caught at resolution instead, because
// address.VersionOf normalises before reading the marker and so needs the whole
// id. Pinned rather than left implicit: when pkg/address grows a
// VersionMarkerOf, this test should start failing and be replaced by the
// prefixes moving into the test above.
func TestAPrefixIsNotYetKindChecked(t *testing.T) {
	if _, err := ParsePeer(short(groupA)); err != nil {
		t.Errorf("a group prefix in the peer route is currently accepted here: %v", err)
	}
	if _, err := ParseGroupID(short(acctA)); err != nil {
		t.Errorf("an account prefix in the group route is currently accepted here: %v", err)
	}
}

func TestWhatIsStillRefused(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"", "empty"},
		{"   ", "empty"},
		{"nonsense!", "not a Freizone address"},
		{"*chat.example.org", "empty"},
		{acctA + "*https://", "names no server"},
	} {
		if _, err := ParsePeer(tc.raw); err == nil {
			t.Errorf("ParsePeer(%q) should have been refused", tc.raw)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParsePeer(%q) = %q, want it to mention %q", tc.raw, err, tc.want)
		}
	}
}

// All-or-nothing: a bot that came up with three of four recipients would deliver
// to three people and look like it was working.
func TestOneBadEntrySpoilsTheWholeList(t *testing.T) {
	_, err := ParsePeers([]string{acctA, "nonsense!", acctB})
	if err == nil {
		t.Fatal("want a failure")
	}
	if !strings.Contains(err.Error(), "nonsense!") {
		t.Errorf("the error should name the bad entry, got %q", err)
	}
}

// Now that a prefix is accepted, "the same recipient twice" has more ways to be
// written -- and every one of them would deliver twice.
func TestADuplicateRecipientIsRefusedHoweverSpelled(t *testing.T) {
	for _, pair := range [][2]string{
		{acctA, acctA},
		{acctA, address.FormatForDisplay(acctA)},
		{acctA, strings.ToUpper(acctA)},
		{acctA, acctA + "*local"},
		// The compact form and the full id are one person.
		{acctA, short(acctA)},
		{short(acctA) + "*chat.example.org", acctA + "*chat.example.org"},
		// Differing only in the scheme, which is how this bot reaches that
		// server rather than part of who lives there.
		{acctA + "*chat.example.org", acctA + "*http://chat.example.org"},
	} {
		if _, err := ParsePeers(pair[:]); err == nil {
			t.Errorf("%q and %q are one recipient and should have been refused", pair[0], pair[1])
		}
	}

	// Genuinely different recipients stay two.
	for _, pair := range [][2]string{
		{acctA, acctB},
		{acctA, acctA + "*chat.example.org"},
		{acctA + "*box.lan:18080", acctA + "*box.lan:18081"},
	} {
		if _, err := ParsePeers(pair[:]); err != nil {
			t.Errorf("%q and %q are two recipients: %v", pair[0], pair[1], err)
		}
	}
}

func TestAnEmptyListIsFine(t *testing.T) {
	got, err := ParsePeers(nil)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}
