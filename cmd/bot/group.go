package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/behringer24/freizone-server/pkg/group"
)

// Resolving a configured group, which may be written as a prefix.
//
// Every spelling of a Freizone address is accepted in configuration, including
// the short prefix the app displays in its compact `shortid*domain` form -- see
// internal/config/address.go for why. `config.Load` already reduces the
// hyphenated, upper-case and server-carrying forms to a bare id; what it cannot
// do is turn a *prefix* into the whole thing, because that needs the list of
// groups this bot holds, and that needs an open account.
//
// So the daemon does it, once, before the loops start. Everything downstream --
// the destination a message is queued for, the comparison that decides whether
// to accept an invitation -- uses the resolved value.

// configuredGroup is the group id to send to and compare against: the resolved
// full id once it is known, and the configured value until then.
func (d *daemon) configuredGroup() string {
	if d.groupID != "" {
		return d.groupID
	}
	return d.cfg.RouteGroup
}

// isConfiguredGroup reports whether an id names the configured group, allowing
// for the configured value being a prefix of it.
func (d *daemon) isConfiguredGroup(id string) bool {
	want := d.configuredGroup()
	return want != "" && strings.HasPrefix(id, want)
}

// resolveGroup turns a configured prefix into the full group id, using the
// groups this bot is already a member of.
//
// Three outcomes, and the third is the one worth having:
//
//   - exactly one match: that is the group, and the full id is used from here on.
//   - no match: the bot is not in that group yet, which is ordinary on a first
//     run. The configured value is kept and the invitation path resolves it,
//     since an invitation carries the full id.
//   - several matches: refused, naming both. This is the real risk a prefix
//     carries, and it is the only part of accepting prefixes that needs
//     guarding -- silently picking one would send somebody's messages to a
//     group they did not mean.
func (d *daemon) resolveGroup() error {
	want := d.cfg.RouteGroup
	if want == "" || d.knownGroups == nil {
		return nil
	}
	ids, err := d.knownGroups()
	if err != nil {
		// Not fatal: the invitation path will resolve it, and refusing to start
		// over a read that may simply have found nothing yet would be worse.
		d.logger.Warn("could not list the groups this bot holds", "error", err)
		return nil
	}

	var matches []string
	for _, id := range ids {
		if strings.HasPrefix(id, want) {
			matches = append(matches, id)
		}
	}

	switch len(matches) {
	case 0:
		// Warn rather than inform, and name what *was* found. Nothing else
		// prints the groups this bot is in, so an operator whose configured id
		// is simply wrong had no way to see the right one -- and the symptom is
		// every send failing with "no facts about group <prefix>", which reads
		// like a broken group rather than a mistyped setting.
		d.logger.Warn("the configured group is not one this bot is in: nothing will be delivered to it",
			"configured", want, "groups_this_bot_is_in", ids)
	case 1:
		d.groupID = matches[0]
		if matches[0] != want {
			d.logger.Info("resolved the configured group",
				"configured", want, "group", matches[0])
		}
	default:
		return fmt.Errorf(
			"%q matches more than one group this bot is in (%s): "+
				"configure the whole group id rather than a prefix",
			want, strings.Join(matches, ", "))
	}
	return nil
}

// Forgetting invitations this bot was never going to answer.
//
// An invitation to a group the operator did not configure is ignored -- but the
// *facts* behind it are already on disk by then, folded by pkg/client when the
// envelope was handled. Nothing ever removed them, so a bot whose address is
// known accumulates one fact set per unsolicited invitation, for ever.
//
// # Why this waits rather than forgetting at once
//
// An invitation is announced exactly once. `joinConfiguredGroupIfInvited` exists
// because of that: on a later start it reads the facts already held rather than
// waiting to be told again, which is what makes "invite first, configure
// afterwards" work -- and that is the order the first run leads an operator
// into, since it prints the address to invite before anybody has looked up a
// group id.
//
// Forgetting an ignored invitation immediately would undo that, and not
// recoverably: from the group's side this bot is *already* invited, so inviting
// it again is idempotent and produces no new announcement. The operator would
// have to remove it and invite it afresh, having been given no reason to think
// either was necessary.
//
// So an invitation is kept for a good while and then dropped, which bounds the
// growth without closing the window. The age comes from the facts themselves --
// `Member.AddedAt` is when this account was added -- so nothing has to be
// written down to know it.

// forgetStaleInvitations drops the facts of groups this bot was invited to,
// never joined, and was not configured for, once the invitation is older than
// the retention. Never touches a group it has joined: the others keep sending to
// those, and an arriving message would rebuild a chat whose facts are gone.
func (d *daemon) forgetStaleInvitations(now time.Time) {
	if d.cfg.ForgetInvitesAfter <= 0 || d.knownGroups == nil || d.forgetGroup == nil {
		return
	}
	ids, err := d.knownGroups()
	if err != nil {
		d.logger.Warn("could not list groups to tidy up", "error", err)
		return
	}

	me := d.id.AccountID
	for _, groupID := range ids {
		if d.isConfiguredGroup(groupID) {
			continue
		}
		membership, err := d.membershipOf(groupID)
		if err != nil || membership == nil {
			continue
		}
		var mine *group.Member
		for i := range membership.Members {
			if membership.Members[i].AccountID == me {
				mine = &membership.Members[i]
				break
			}
		}
		// Not in the member list, or in it as a member: neither is an ignored
		// invitation. The first is a group this bot was removed from and the
		// second one it belongs to -- both are somebody else's decision to
		// clean up, and forgetting a joined group is the one thing ForgetGroup
		// says never to do.
		if mine == nil || mine.Joined {
			continue
		}
		age := now.Sub(mine.AddedAt)
		if age < d.cfg.ForgetInvitesAfter {
			continue
		}

		if err := d.forgetGroup(groupID); err != nil {
			d.logger.Warn("could not forget an old invitation",
				"group", groupID, "error", err)
			continue
		}
		// Said out loud, because this is the bot throwing something away. If an
		// operator was about to configure that group, this line is the only
		// thing that will tell them why nothing was waiting for them.
		d.logger.Info("forgot an invitation nobody answered",
			"group", groupID, "invited", mine.AddedAt.UTC().Format(time.RFC3339),
			"age", age.Round(time.Hour).String())
	}
}
