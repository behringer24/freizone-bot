package main

import (
	"context"
	"fmt"

	"github.com/behringer24/freizone-server/pkg/address"
)

// Turning the configured allow-list into exact account ids.
//
// A commander may be written in any spelling, including the short prefix from
// the app's compact display -- see internal/config/address.go. What arrives
// here is therefore possibly a prefix, and what the authorization check needs is
// an exact id, because it compares against whoever sent something.
//
// So the prefix is resolved once, here, rather than compared many times later.
// The server completes it and pkg/client verifies the answer against the
// returned root key, so the result is one account, cryptographically tied to the
// id it claims. Two things make that sound rather than convenient: the server
// refuses to register an account whose id starts like an existing one, so a
// prefix names at most one account there and no collision can be minted; and the
// verification means a server cannot answer with somebody else's id.
//
// A full id needs no network, so a bot configured with whole ids still starts
// with its server unreachable. Only a prefix costs a lookup.

// resolveCommanders turns the configured allow-list into exact account ids.
//
// A failure is fatal. An allow-list that quietly lost an entry is a bot that
// silently answers nobody -- and since a sender who is not on the list gets no
// reply by design, that would be invisible from the outside. Better to refuse to
// start and say which entry could not be resolved.
func (d *daemon) resolveCommanders(ctx context.Context) ([]string, error) {
	out := make([]string, 0, len(d.cfg.Commanders))
	for _, commander := range d.cfg.Commanders {
		if _, err := address.Normalize(commander.ID); err == nil {
			out = append(out, commander.ID) // already whole; nothing to ask
			continue
		}

		endpoint, err := d.resolvePeer(ctx, commander.ID, commander.Server)
		if err != nil {
			return nil, fmt.Errorf(
				"the commander %s could not be resolved: %w -- "+
					"a short id has to be looked up, so either it names no account on that "+
					"server or the server could not be reached; configuring the whole id "+
					"avoids the lookup entirely",
				commander, err)
		}
		d.logger.Info("resolved a commander", "configured", commander.String(), "account", endpoint.AccountID)
		out = append(out, endpoint.AccountID)
	}
	return out, nil
}
