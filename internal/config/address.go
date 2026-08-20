package config

import (
	"fmt"
	"strings"

	"github.com/behringer24/freizone-server/pkg/address"
)

// Reading a recipient out of configuration.
//
// # Why this is here at all
//
// A Freizone address is written `id*server` -- that is the form a person copies
// out of the app and pastes into a configuration file. Nothing in the Go core
// parses it: `pkg/address` normalises the *id* half (stripping the hyphens a
// display form inserts, checking the checksum) and knows nothing about the
// server. Only freizone-app's Dart side ever split on the star.
//
// So a bot that took a bare id and asked its own server for it could reach
// nobody else's -- which quietly made every recipient local, in a product whose
// whole point is that servers federate.

// Peer is one configured recipient: which account, and where it lives.
type Peer struct {
	AccountID string

	// Server is empty for an account on this bot's own server. That emptiness is
	// load-bearing rather than a convenience -- it is what selects local versus
	// federated delivery and authentication throughout pkg/client's send path.
	Server string
}

func (p Peer) String() string {
	if p.Server == "" {
		return p.AccountID
	}
	return p.AccountID + "*" + p.Server
}

// ParsePeer reads a configured recipient.
//
// Accepts:
//
//	q9zk4…                        an account on this bot's own server
//	q9zk4-…-…                     the same, in the hyphenated display form
//	q9zk4…*chat.example.org       an account on another server
//	q9zk4…*https://chat.example.org
//
// A bare host is given `https://`, because that is the only scheme a Freizone
// server is reachable over and requiring it in configuration would be a papercut
// with no upside. A scheme that is spelled out is left alone, so a local test
// server can be named `http://…` deliberately -- which is the one case where the
// difference matters and guessing would be wrong.
func ParsePeer(raw string) (Peer, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Peer{}, fmt.Errorf("empty recipient")
	}

	id, server, hasServer := strings.Cut(raw, "*")

	normalised, err := address.Normalize(id)
	if err != nil {
		// Deliberately strict here, unlike ResolvePeer -- which tolerates a
		// prefix because a person typing into a search box wants completion.
		// Configuration is not typed under time pressure, and a truncated id in
		// an environment file should fail at startup rather than resolve to
		// whoever happens to match.
		return Peer{}, fmt.Errorf("%q is not a Freizone account id: %w", id, err)
	}

	// An account id and a group id differ by a single version marker, so a group
	// listed as a peer would be addressed as a person and fail somewhere far
	// away from the line that got it wrong.
	version, err := address.VersionOf(normalised)
	if err != nil {
		return Peer{}, fmt.Errorf("%q has no readable version marker: %w", id, err)
	}
	if version == address.VersionGroup {
		return Peer{}, fmt.Errorf("%q is a group id, not an account -- a group belongs in the group route", id)
	}

	if !hasServer {
		return Peer{AccountID: normalised}, nil
	}
	// `*local`, and a bare trailing `*`, both mean "whatever server this is
	// resolved against" -- the format says so, and treating them as equivalent
	// to no star at all is why a parser can always split on the first star
	// rather than special-casing the absence of one. Read literally instead,
	// `*local` becomes the host `https://local` and quietly routes nowhere.
	server = strings.TrimSpace(server)
	if server == "" || strings.EqualFold(server, "local") {
		return Peer{AccountID: normalised}, nil
	}
	if !strings.Contains(server, "://") {
		server = "https://" + server
	}
	return Peer{AccountID: normalised, Server: strings.TrimSuffix(server, "/")}, nil
}

// ParsePeers reads a whole configured list, reporting the first bad entry.
//
// All-or-nothing on purpose: a bot that started with three of four recipients
// would page three people and look like it was working.
func ParsePeers(raw []string) ([]Peer, error) {
	out := make([]Peer, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, entry := range raw {
		p, err := ParsePeer(entry)
		if err != nil {
			return nil, err
		}
		// Refused rather than quietly collapsed: a repeated recipient would be
		// paged twice, and the likelier reading of a duplicate is that one of
		// the two entries was meant to name somebody else.
		if _, dup := seen[p.String()]; dup {
			return nil, fmt.Errorf("%s is listed twice", p)
		}
		seen[p.String()] = struct{}{}
		out = append(out, p)
	}
	return out, nil
}

// ParseGroupID checks a configured group id.
//
// Validated for the same reason a recipient is, and with one addition: a group
// id and an account id differ only by a version marker, so an account id put in
// the group route would otherwise fail at send time with something obscure
// about a group nobody can find. Said here instead, at startup, in words.
func ParseGroupID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	normalised, err := address.Normalize(raw)
	if err != nil {
		return "", fmt.Errorf("%q is not a Freizone id: %w", raw, err)
	}
	version, err := address.VersionOf(normalised)
	if err != nil {
		return "", fmt.Errorf("%q has no readable version marker: %w", raw, err)
	}
	if version != address.VersionGroup {
		return "", fmt.Errorf("%q is an account id, not a group id -- a group route needs a group", raw)
	}
	return normalised, nil
}
