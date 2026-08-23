package main

import (
	"fmt"
	"strings"

	"github.com/behringer24/freizone-bot/internal/config"
	"github.com/behringer24/freizone-bot/internal/outbound"
	"github.com/behringer24/freizone-server/pkg/address"
)

// The two questions an operator actually asks about a bot that has been running
// for a while: who is getting these, and what decided that.
//
// Read-only, both of them, and that is the whole design rather than a stage on
// the way to something else. There is no `/addrecipient` and there is not going
// to be one: the recipient list is *configuration*, a chat command that edited
// it would route around whatever review that configuration has, and the change
// would either not survive a restart (so the command lied) or survive without
// appearing in the config file (so the config no longer says who is on the
// list). Adding a recipient also means every future message goes there too --
// one command turning "may message this bot" into "receives everything this bot
// will ever say", quietly. Runtime change, if it is ever wanted, belongs in
// re-reading the configuration on a signal, where the file stays the truth.
//
// Both answers are disclosure, and what gates them is the commander allow-list.
// With `FREIZONE_BOT_ALLOW_GROUP_COMMANDS` on, an answer lands in the group for
// everyone in it to read -- which is the operator's choice to make, and worth
// knowing they are making it.

// recipientLines answers /listrecipients.
func (d *daemon) recipientLines() string {
	var b strings.Builder
	b.WriteString("I send to:\n")

	wrote := false
	if d.cfg.RouteGroup != "" {
		fmt.Fprintf(&b, "  group  %s\n", address.FormatForDisplay(d.cfg.RouteGroup))
		wrote = true
	}
	// Re-parsed rather than printed as configured, so what is shown is what is
	// actually addressed: the stored form has already had its separators
	// stripped and its server normalised, and showing the raw string would hide
	// a difference that matters.
	peers, err := config.ParsePeers(d.cfg.RoutePeers)
	if err != nil {
		// Cannot happen -- the daemon would not have started -- but saying so
		// beats printing a confident half-list.
		return "I cannot read my own recipient list: " + err.Error()
	}
	for _, peer := range peers {
		fmt.Fprintf(&b, "  person %s\n", peer.Display())
		wrote = true
	}

	if !wrote {
		// Unreachable while the daemon insists on a route at startup, and here
		// anyway: a bot that answers nothing at all reads like a broken command.
		return "I have no recipients configured."
	}

	// The caveat is not decoration: with routing rules configured, this list is
	// who *can* receive, and a rule decides who does for any given message.
	if len(d.cfg.RouteRules) > 0 {
		b.WriteString("\nA rule can narrow this per message -- ask me /routes.")
	}
	return strings.TrimRight(b.String(), "\n")
}

// routeLines answers /routes.
func (d *daemon) routeLines() string {
	var b strings.Builder

	configured := make([]string, 0, 2)
	if d.cfg.RouteGroup != "" {
		configured = append(configured, outbound.RouteGroup)
	}
	if len(d.cfg.RoutePeers) > 0 {
		configured = append(configured, fmt.Sprintf("%s (%d)", outbound.RoutePeers, len(d.cfg.RoutePeers)))
	}
	fmt.Fprintf(&b, "Routes: %s\n", strings.Join(configured, ", "))

	if len(d.cfg.RouteRules) == 0 {
		b.WriteString("\nNo rules, so everything goes to every route above.")
		return b.String()
	}

	b.WriteString("\nRules, first match wins:\n")
	for _, rule := range d.cfg.RouteRules {
		fmt.Fprintf(&b, "  %s=%s -> %s\n", rule.Label, rule.Value, strings.Join(rule.Routes, "+"))
	}
	// Both halves of this are decisions somebody may be surprised by, so both
	// are stated rather than left to be discovered during an incident.
	b.WriteString("\nA message matching no rule goes everywhere, and an explicit route beats the rules.")
	return b.String()
}
