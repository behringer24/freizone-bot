package config

import (
	"fmt"
	"strings"

	"github.com/behringer24/freizone-server/pkg/address"
)

// Reading recipients out of configuration.
//
// # Every spelling, everywhere
//
// A Freizone address has several written forms, and **all of them are accepted
// here** -- the full 21-character id, the hyphenated form the app displays, the
// short prefix from the app's compact `shortid*domain` rendering, any of those
// with `*server`, with `*local`, with a bare trailing `*`, or with no server
// part at all. For accounts and for groups alike.
//
// This file used to require the full checksummed id, on the reasoning that
// configuration is not typed under pressure so a truncated id should fail rather
// than resolve to whoever happens to match. That was wrong twice over. It is
// wrong in principle, because every one of those forms is one the *app displays*
// and therefore one a person will copy -- so refusing any of them means the form
// most likely to be pasted is the one that does not work. And it is wrong in
// fact, because completing a prefix is not guessing: `Client.ResolvePeer` has the
// server complete it and then verifies the returned id against the returned root
// key, and a group prefix resolves against the groups this bot already holds.
//
// What genuinely needs refusing is an **ambiguous** prefix -- two things
// matching -- and that refusal has to name both. Resolution happens where the
// information is, which is in the daemon with an open account, not here.
//
// # What this file still decides
//
// Only what remains the bot's own rule: an account belongs in the peer route and
// a group in the group route (checkable on a prefix too, since the first
// character is the version marker), no duplicate recipients, and a list is
// all-or-nothing.

// ParsePeer reads one configured recipient, in any spelling. The returned ID may
// be a prefix; the core resolves it when the message is sent.
func ParsePeer(raw string) (address.Address, error) {
	peer, err := address.Parse(raw)
	if err != nil {
		return address.Address{}, fmt.Errorf("%q is not a Freizone address: %w", strings.TrimSpace(raw), err)
	}
	if err := mustBe(peer.ID, false); err != nil {
		return address.Address{}, err
	}
	return peer, nil
}

// ParsePeers reads a whole configured list.
//
// All-or-nothing on purpose: a bot that came up with three of four recipients
// would deliver to three people and look like it was working.
func ParsePeers(raw []string) ([]address.Address, error) {
	out := make([]address.Address, 0, len(raw))
	for _, entry := range raw {
		peer, err := ParsePeer(entry)
		if err != nil {
			return nil, err
		}
		for _, seen := range out {
			if sameRecipient(seen, peer) {
				// Refused rather than quietly collapsed: a repeated recipient
				// would be delivered to twice, and the likelier reading of a
				// duplicate is that one of the two entries was meant to name
				// somebody else.
				return nil, fmt.Errorf("%s and %s are the same recipient", seen, peer)
			}
		}
		out = append(out, peer)
	}
	return out, nil
}

// sameRecipient compares two configured recipients across spellings.
//
// One id being a prefix of the other counts as the same, because that is what it
// is -- a comparison by equality would let the compact form and the full id
// through as two people and deliver twice. SameServer rather than a string
// comparison for the same reason on the other half: the scheme is how this bot
// reaches a server, not part of who lives there.
func sameRecipient(a, b address.Address) bool {
	if !address.SameServer(a.Server, b.Server) {
		return false
	}
	return strings.HasPrefix(a.ID, b.ID) || strings.HasPrefix(b.ID, a.ID)
}

// ParseGroupID reads a configured group, in any spelling.
//
// A server part is accepted and discarded rather than refused: a group is not
// reached through a server -- its id derives from its own root key -- but the
// compact form the app displays carries one, and that form is exactly what
// somebody copies. Refusing it taught nothing and cost a restart.
//
// The returned id may be a prefix. The daemon resolves it against the groups it
// holds; see cmd/bot.
func ParseGroupID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := address.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%q is not a Freizone id: %w", raw, err)
	}
	if err := mustBe(parsed.ID, true); err != nil {
		return "", err
	}
	return parsed.ID, nil
}

// mustBe checks the kind of thing an id names: an account belongs in the peer
// route, a group in the group route, and they differ in one character.
//
// Only a **full** id is checked. The version marker is the first character, so a
// prefix could be checked too -- but `address.VersionOf` normalises before
// reading it and therefore insists on all 21. Re-deriving the marker here would
// mean copying the charset into this repository, which is precisely what SRV-31
// stopped: the address format has one home. So a prefix passes here and is
// caught when it is resolved instead -- at startup for a group, at the first
// send for a peer.
//
// The proper fix is a `VersionMarkerOf` in `pkg/address` that reads the marker
// without validating the rest. Then this becomes one call again and the check
// covers every spelling.
func mustBe(id string, group bool) error {
	version, err := address.VersionOf(id)
	if err != nil {
		return nil //nolint:nilerr // a prefix, checked at resolution -- see above
	}
	isGroup := version == address.VersionGroup
	switch {
	case group && !isGroup:
		return fmt.Errorf("%q is an account id, not a group id -- a group route needs a group", id)
	case !group && isGroup:
		return fmt.Errorf("%q is a group id, not an account -- a group belongs in the group route", id)
	}
	return nil
}

// ParseCommanders reads the allow-list of accounts that may command the bot.
//
// Every spelling is accepted and reduced to the canonical id -- hyphenated,
// upper case, with a `*server` part -- because these are the forms the app
// displays and therefore the forms somebody pastes. Without this the list held
// whatever was written and compared it against the canonical id the receive path
// reports, so a hyphenated entry matched nobody. And it failed *silently*: a
// sender who is not on the list gets no reply at all, deliberately, since a
// refusal tells whoever asked that something is here and listening. A setting
// that quietly authorises nobody is the worst shape that mistake can take.
//
// The server part is dropped rather than kept: an account id identifies the
// account wherever it lives, and the sender of a message is reported as an id.
//
// # Why a prefix is refused here, having been accepted everywhere else
//
// A recipient prefix is *completed* -- the server resolves it and the client
// verifies the result against the returned root key, or it is matched against
// the groups this bot holds. An allow-list entry has nothing to complete it
// against: it is checked against whoever happens to send something, so a prefix
// would authorise *everyone* whose id begins with it. Five characters of a
// bech32 id is not an authorisation decision anybody means to make.
func ParseCommanders(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, entry := range raw {
		parsed, err := address.Parse(entry)
		if err != nil {
			return nil, fmt.Errorf("%q is not a Freizone address: %w", strings.TrimSpace(entry), err)
		}
		id, err := address.Normalize(parsed.ID)
		if err != nil {
			return nil, fmt.Errorf(
				"%q is not a whole account id: %w. An allow-list entry has to name one account -- "+
					"a short prefix would authorise everybody whose id starts with it",
				strings.TrimSpace(entry), err)
		}
		if err := mustBe(id, false); err != nil {
			return nil, err
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("%s appears twice", id)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}
