// Package authz decides who may command this bot.
//
// It is one small file and it carries the most important invariant in the
// repository, so the reasoning lives here rather than being spread across the
// call sites.
//
// # Anyone can send this bot a message
//
// A Freizone address is reachable by whoever knows it, and every member of a
// group the bot is in knows it -- the group's own facts name every member. So
// incoming text is untrusted input from an unbounded set of people, in exactly
// the way a public HTTP endpoint is.
//
// # The check runs before interpretation, not after
//
// Not "interpret, then refuse". A sender who is not allow-listed must have
// their text never reach the interpreter at all.
//
// Today the interpreter is a parser and the difference looks academic. It stops
// being academic the moment an LLM sits there: an interpreter that sees
// everything is an interpreter anyone who knows the address can write prompts
// for. Ordering the check first is what makes that impossible rather than
// merely unlikely, and it costs nothing to do now while it would be a rewrite
// later.
//
// # Fail closed
//
// No allow-list means no command surface at all, not "anyone". An operator who
// has not thought about who may command their bot has not implicitly decided
// that everyone may.
//
// # The list is configuration, never learned
//
// Specifically not "whoever is in the alert group". Group membership changes
// without the operator being told -- a moderator adds somebody, and that
// somebody can now drive the bot. A configured list changes when a person
// changes it.
package authz

import (
	"strings"
)

// Policy is who may command the bot, and where.
type Policy struct {
	commanders map[string]bool
	allowGroup bool
}

// New builds a policy from configuration. An empty list of commanders disables
// commands entirely.
func New(commanders []string, allowGroupCommands bool) *Policy {
	set := make(map[string]bool, len(commanders))
	for _, c := range commanders {
		if c = strings.TrimSpace(c); c != "" {
			set[c] = true
		}
	}
	return &Policy{commanders: set, allowGroup: allowGroupCommands}
}

// Enabled reports whether any command surface exists at all.
//
// Worth asking before doing anything else: with no commanders configured the
// bot should not even look at incoming text, let alone decide about it.
func (p *Policy) Enabled() bool { return len(p.commanders) > 0 }

// MayCommand reports whether this sender may command the bot from this kind of
// chat.
//
// Group commands are off unless the operator turned them on. A command in a
// group is visible to everyone in it, its answer is too, and the membership
// drifts -- so the safe default is that the bot only takes instructions in a
// one-to-one chat, where there is exactly one person on the other side and the
// allow-list names them.
func (p *Policy) MayCommand(senderAccountID string, inGroup bool) bool {
	if !p.Enabled() {
		return false
	}
	if inGroup && !p.allowGroup {
		return false
	}
	return p.commanders[senderAccountID]
}

// Commanders is how many accounts may command the bot, for a status answer.
// The ids themselves are deliberately not exposed: a status answer is readable
// by anyone who can reach the control socket, and the list of people who can
// drive this bot is not something to hand out for free.
func (p *Policy) Commanders() int { return len(p.commanders) }
