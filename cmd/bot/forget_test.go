package main

import (
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/behringer24/freizone-server/pkg/group"
)

const myID = "qlfxcdsa42x4xe4gwjcnu"

var now = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

// A group this bot holds facts for, with this account in it as described.
func held(groupID string, joined bool, invitedAgo time.Duration) *group.Resolved {
	return &group.Resolved{
		GroupID: groupID,
		Members: []group.Member{
			{AccountID: "qu0qmxckqmum0dv77pndv", Joined: true, AddedAt: now.Add(-invitedAgo)},
			{AccountID: myID, Joined: joined, AddedAt: now.Add(-invitedAgo)},
		},
	}
}

type forgetHarness struct {
	d         *daemon
	forgotten []string
}

func harness(t *testing.T, configuredGroup string, groups map[string]*group.Resolved) *forgetHarness {
	t.Helper()
	env := map[string]string{}
	if configuredGroup != "" {
		env["FREIZONE_BOT_ROUTE_GROUP"] = configuredGroup
	}
	d := daemonWith(t, env)
	d.logger = slog.New(slog.DiscardHandler)
	d.id.AccountID = myID

	h := &forgetHarness{d: d}
	d.knownGroups = func() ([]string, error) {
		ids := make([]string, 0, len(groups))
		for id := range groups {
			ids = append(ids, id)
		}
		return ids, nil
	}
	d.membershipOf = func(id string) (*group.Resolved, error) { return groups[id], nil }
	d.forgetGroup = func(id string) error {
		h.forgotten = append(h.forgotten, id)
		return nil
	}
	return h
}

func TestAnOldUnansweredInvitationIsForgotten(t *testing.T) {
	h := harness(t, "", map[string]*group.Resolved{
		someGroup: held(someGroup, false, 40*24*time.Hour),
	})
	h.d.forgetStaleInvitations(now)

	if len(h.forgotten) != 1 || h.forgotten[0] != someGroup {
		t.Errorf("forgot %v, want the stale invitation", h.forgotten)
	}
}

// Everything this must not touch, and why each one would be a different bug.
func TestWhatIsNeverForgotten(t *testing.T) {
	for _, tc := range []struct {
		what       string
		configured string
		groups     map[string]*group.Resolved
	}{
		{
			// The window this whole design exists to protect: an invitation is
			// announced once, so forgetting a young one closes the door on
			// "invite first, configure the group afterwards" -- and inviting
			// again does not reopen it, since from the group's side this bot is
			// already invited.
			"an invitation younger than the retention",
			"",
			map[string]*group.Resolved{someGroup: held(someGroup, false, 2*24*time.Hour)},
		},
		{
			// Old, but it is the one the operator asked for. Nothing about age
			// makes a configured group stale.
			"the configured group, however old the invitation",
			someGroup,
			map[string]*group.Resolved{someGroup: held(someGroup, false, 400*24*time.Hour)},
		},
		{
			// The one thing ForgetGroup says never to do: the others keep
			// sending to a group this bot is in, and an arriving message would
			// rebuild a chat whose facts are gone -- no name, no member list,
			// and a send that fails with "no group".
			"a group this bot has joined",
			"",
			map[string]*group.Resolved{someGroup: held(someGroup, true, 400*24*time.Hour)},
		},
		{
			// Facts held but this account is not in the member list: removed, or
			// they arrived some other way. Not an unanswered invitation, so not
			// this function's business to decide about.
			"a group this bot is not a member of at all",
			"",
			map[string]*group.Resolved{someGroup: {
				GroupID: someGroup,
				Members: []group.Member{{AccountID: "qu0qmxckqmum0dv77pndv", Joined: true}},
			}},
		},
	} {
		h := harness(t, tc.configured, tc.groups)
		h.d.forgetStaleInvitations(now)
		if len(h.forgotten) != 0 {
			t.Errorf("%s: forgot %v", tc.what, h.forgotten)
		}
	}
}

// Zero means keep everything. An operator who turns this off has said what they
// want, and "off" cannot quietly mean "after a while".
func TestZeroRetentionForgetsNothing(t *testing.T) {
	h := harness(t, "", map[string]*group.Resolved{
		someGroup: held(someGroup, false, 400*24*time.Hour),
	})
	h.d.cfg.ForgetInvitesAfter = 0
	h.d.forgetStaleInvitations(now)

	if len(h.forgotten) != 0 {
		t.Errorf("forgot %v with retention off", h.forgotten)
	}
}

// The configured group may be written as a prefix, and it is still the
// configured group. Comparing it as a whole id here would forget the very group
// the bot is waiting to join.
func TestAPrefixStillNamesTheConfiguredGroup(t *testing.T) {
	h := harness(t, someGroup[:5], map[string]*group.Resolved{
		someGroup: held(someGroup, false, 400*24*time.Hour),
	})
	h.d.forgetStaleInvitations(now)

	if len(h.forgotten) != 0 {
		t.Errorf("forgot %v, which the configured prefix names", h.forgotten)
	}
}

// A listing that fails is not a licence to forget nothing quietly and not a
// reason to stop the bot: it is a local read that can be retried next round.
func TestAFailedListingForgetsNothing(t *testing.T) {
	h := harness(t, "", map[string]*group.Resolved{
		someGroup: held(someGroup, false, 400*24*time.Hour),
	})
	h.d.knownGroups = func() ([]string, error) { return nil, errors.New("disk on fire") }
	h.d.forgetStaleInvitations(now)

	if len(h.forgotten) != 0 {
		t.Errorf("forgot %v", h.forgotten)
	}
}
