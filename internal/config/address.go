package config

import (
	"fmt"
	"strings"

	"github.com/behringer24/freizone-server/pkg/address"
)

// Reading recipients out of configuration.
//
// The address format itself -- `id*server`, the local form, scheme defaulting,
// what counts as one server -- lives in `pkg/address` (SRV-31). This file holds
// only what is the *bot's* rule rather than the format's, and the difference is
// worth keeping visible: this package once had its own parser, written from the
// format's description, and it disagreed with the app's in four places inside a
// day. One of those disagreements silently routed `*local` nowhere.
//
// What belongs here: recipients must be complete (never a prefix), an account
// where an account is meant, a group where a group is meant, no duplicates, and
// a list that is all or nothing.

// ParsePeer reads one configured recipient.
//
// Strict on purpose. `pkg/address.Parse` accepts a prefix because interactive
// completion needs it; configuration is not typed under time pressure, and a
// truncated id in an environment file resolving to whoever happens to match is
// how a message reaches a stranger.
func ParsePeer(raw string) (address.Address, error) {
	peer, err := address.ParseFull(raw)
	if err != nil {
		return address.Address{}, fmt.Errorf("%q is not a Freizone address: %w", strings.TrimSpace(raw), err)
	}

	// An account id and a group id differ by a single version marker, so a
	// group listed as a peer would be addressed as a person and fail somewhere
	// far away from the line that got it wrong.
	version, err := address.VersionOf(peer.ID)
	if err != nil {
		return address.Address{}, fmt.Errorf("%q has no readable version marker: %w", raw, err)
	}
	if version == address.VersionGroup {
		return address.Address{}, fmt.Errorf("%q is a group id, not an account -- a group belongs in the group route", peer.ID)
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
		// Refused rather than quietly collapsed: a repeated recipient would be
		// sent to twice, and the likelier reading of a duplicate is that one of
		// the two entries was meant to name somebody else.
		//
		// Compared with SameServer rather than by rendered string, because the
		// scheme is how this bot happens to reach a server and not part of its
		// identity -- so `id*example.org` and `id*http://example.org` are one
		// recipient, and a string comparison would send to them twice.
		for _, seen := range out {
			if seen.ID == peer.ID && address.SameServer(seen.Server, peer.Server) {
				return nil, fmt.Errorf("%s is listed twice", peer)
			}
		}
		out = append(out, peer)
	}
	return out, nil
}

// ParseGroupID checks a configured group id.
//
// A group is not reached through a server the way an account is -- its id is
// derived from its own root key -- so this takes an id and not an address, and
// says so if it is given one.
func ParseGroupID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.Contains(raw, "*") {
		return "", fmt.Errorf("%q looks like an address: a group is not reached through a server, so its id stands alone", raw)
	}
	id, err := address.Normalize(raw)
	if err != nil {
		return "", fmt.Errorf("%q is not a Freizone id: %w", raw, err)
	}
	version, err := address.VersionOf(id)
	if err != nil {
		return "", fmt.Errorf("%q has no readable version marker: %w", raw, err)
	}
	// The likelier of the two mistakes: the ids look alike and only the first
	// character differs.
	if version != address.VersionGroup {
		return "", fmt.Errorf("%q is an account id, not a group id -- a group route needs a group", raw)
	}
	return id, nil
}
