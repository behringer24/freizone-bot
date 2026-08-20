package config

import (
	"strings"
	"testing"
)

// Real, checksum-valid ids -- a made-up string would make every one of these
// tests pass for the wrong reason, which is how the first version of this
// package shipped a peer route that could not reach another server.
const (
	acctA  = "qlfxcdsa42x4xe4gwjcnu"
	acctB  = "qu0qmxckqmum0dv77pndv"
	groupA = "plfxcdsa42x4xe4zr2mju"
)

func TestABareIdMeansOurOwnServer(t *testing.T) {
	got, err := ParsePeer(acctA)
	if err != nil {
		t.Fatalf("ParsePeer: %v", err)
	}
	if got.AccountID != acctA {
		t.Errorf("id: got %q", got.AccountID)
	}
	// Not "our hostname filled in for convenience": empty is what pkg/client
	// reads as local, and inventing a value here would change the send path.
	if got.Server != "" {
		t.Errorf("server should stay empty, got %q", got.Server)
	}
}

// The app shows an address hyphenated so it can be read aloud, and that display
// form is what a person copies. Refusing it would mean the one thing an operator
// is most likely to paste is the one thing that does not work.
func TestTheHyphenatedDisplayFormIsAccepted(t *testing.T) {
	hyphenated := acctA[:5] + "-" + acctA[5:13] + "-" + acctA[13:]
	got, err := ParsePeer("  " + hyphenated + "  ")
	if err != nil {
		t.Fatalf("ParsePeer(%q): %v", hyphenated, err)
	}
	if got.AccountID != acctA {
		t.Errorf("got %q, want the separators stripped", got.AccountID)
	}
}

func TestAStarNamesAnotherServer(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		server string
	}{
		// A bare host gets https, the only scheme a server is reachable over.
		{acctA + "*chat.example.org", "https://chat.example.org"},
		{acctA + "*https://chat.example.org", "https://chat.example.org"},
		// A spelled-out scheme is left alone, so a local test server can be
		// named http:// deliberately. This is the case where guessing would be
		// actively wrong rather than merely verbose.
		{acctA + "*http://aff-abe:18081", "http://aff-abe:18081"},
		// A trailing slash is what a browser address bar hands you.
		{acctA + "*https://chat.example.org/", "https://chat.example.org"},
		{acctA + "* chat.example.org ", "https://chat.example.org"},
	} {
		got, err := ParsePeer(tc.raw)
		if err != nil {
			t.Errorf("ParsePeer(%q): %v", tc.raw, err)
			continue
		}
		if got.AccountID != acctA || got.Server != tc.server {
			t.Errorf("ParsePeer(%q) = %+v, want server %q", tc.raw, got, tc.server)
		}
	}
}

// Strict on purpose, and this is the test that says why: ResolvePeer in the core
// completes a prefix because a person is typing into a search box. Configuration
// is not typed under time pressure, and a truncated id in an environment file
// resolving to whoever happens to match is how an alert reaches a stranger.
func TestATruncatedIdIsRefusedRatherThanCompleted(t *testing.T) {
	if _, err := ParsePeer(acctA[:12]); err == nil {
		t.Fatal("a prefix must not be accepted in configuration")
	}
}

func TestWhatIsRefused(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string // a fragment the operator needs to see
	}{
		{"", "empty"},
		{"   ", "empty"},
		{acctA + "*", "no server"},
		{acctA + "*   ", "no server"},
		{"not-an-address", "not a Freizone account id"},
		// A group in the peer route: rejected here, with the fix named, rather
		// than failing later as a person who cannot be found.
		{groupA, "group route"},
		{groupA + "*chat.example.org", "group route"},
	} {
		_, err := ParsePeer(tc.raw)
		if err == nil {
			t.Errorf("ParsePeer(%q) should have failed", tc.raw)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParsePeer(%q) = %q, want it to mention %q", tc.raw, err, tc.want)
		}
	}
}

// All-or-nothing: a bot that came up with three of four recipients would page
// three people and look like it was working.
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
	// Same account written two ways -- the check has to see past the spelling,
	// or the hyphenated form would slip a second page through.
	hyphenated := acctA[:5] + "-" + acctA[5:]
	if _, err := ParsePeers([]string{acctA, hyphenated}); err == nil {
		t.Error("the same account twice should be refused")
	}
	// The same account on two different servers is two different people.
	if _, err := ParsePeers([]string{acctA, acctA + "*chat.example.org"}); err != nil {
		t.Errorf("different servers are different recipients: %v", err)
	}
}

func TestAnEmptyListIsFine(t *testing.T) {
	// A bot that only routes to a group has no peers, and that is not an error.
	got, err := ParsePeers(nil)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestAGroupRouteWantsAGroup(t *testing.T) {
	got, err := ParseGroupID(groupA[:5] + "-" + groupA[5:])
	if err != nil {
		t.Fatalf("ParseGroupID: %v", err)
	}
	if got != groupA {
		t.Errorf("got %q", got)
	}
	if _, err := ParseGroupID(""); err != nil {
		t.Errorf("no group route configured is not an error: %v", err)
	}
	// The mirror of the peer-route check, and the more likely mistake of the
	// two: the ids look alike, and only the first character differs.
	if _, err := ParseGroupID(acctA); err == nil {
		t.Error("an account id in the group route should be refused")
	}
}

// Round-trip, because String() is what the outbox writes into a queue entry and
// what the status line shows. If it lost the server, a federated recipient would
// come back local after a restart.
func TestStringRoundTripsThroughParsePeer(t *testing.T) {
	for _, raw := range []string{acctA, acctA + "*https://chat.example.org", acctA + "*http://aff-abe:18081"} {
		first, err := ParsePeer(raw)
		if err != nil {
			t.Fatalf("ParsePeer(%q): %v", raw, err)
		}
		second, err := ParsePeer(first.String())
		if err != nil {
			t.Fatalf("ParsePeer(%q): %v", first, err)
		}
		if first != second {
			t.Errorf("%q: %+v became %+v", raw, first, second)
		}
	}
}
