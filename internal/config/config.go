// Package config loads and validates bot configuration from environment
// variables -- same style as freizone-server and freizone-gateway: no config
// file, everything explicit and env-driven so the bot is trivial to run in a
// container or under a service manager.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/behringer24/freizone-server/pkg/address"
)

// The FREIZONE_BOT_ prefix rather than the terser BOT_ the gateway's own
// GATEWAY_ would suggest. The gateway runs in a container where it is the only
// process; this bot's whole point is running on somebody's existing operations
// host, next to cron, CI runners, service units and very often a second chat
// bot -- and BOT_TOKEN, BOT_NAME and BOT_ID are among the most heavily
// colonised environment names there are. A collision here produces no error: it
// produces a bot that registers against the wrong server, or pages the wrong
// group.
const (
	envServer     = "FREIZONE_BOT_SERVER"
	envStateDir   = "FREIZONE_BOT_STATE_DIR"
	envInviteCode = "FREIZONE_BOT_INVITE_CODE"
	envInviteFile = "FREIZONE_BOT_INVITE_CODE_FILE"
	envLogLevel   = "FREIZONE_BOT_LOG_LEVEL"

	envControlSocket = "FREIZONE_BOT_CONTROL_SOCKET"
	envControlGroup  = "FREIZONE_BOT_CONTROL_GROUP"

	envRouteGroup = "FREIZONE_BOT_ROUTE_GROUP"
	envRoutePeers = "FREIZONE_BOT_ROUTE_PEERS"
	envRouteRules = "FREIZONE_BOT_ROUTE_RULES"

	envDedupWindow = "FREIZONE_BOT_DEDUP_WINDOW_MINUTES"

	envCommanders        = "FREIZONE_BOT_COMMANDERS"
	envAllowGroupCommand = "FREIZONE_BOT_ALLOW_GROUP_COMMANDS"
	envJokesFile         = "FREIZONE_BOT_JOKES_FILE"
	envActionsFile       = "FREIZONE_BOT_ACTIONS_FILE"
	envWebhookAddr       = "FREIZONE_BOT_WEBHOOK_ADDR"
	envWebhookTokens     = "FREIZONE_BOT_WEBHOOK_TOKENS_FILE"
	envAcceptInvites     = "FREIZONE_BOT_ACCEPT_GROUP_INVITES"

	envMaxAge    = "FREIZONE_BOT_MAX_AGE_MINUTES"
	envRate      = "FREIZONE_BOT_RATE_PER_MINUTE"
	envOutboxMax = "FREIZONE_BOT_OUTBOX_MAX"

	envMaintenanceInterval = "FREIZONE_BOT_MAINTENANCE_INTERVAL_MINUTES"
)

const (
	defaultStateDir = "./data"

	// An undelivered message older than this is dropped rather than sent. An
	// alert delivered six hours late is noise; the drop is logged at error
	// level naming the message and its destination, because a silent one would
	// be a lie about a channel somebody is relying on.
	defaultMaxAgeMinutes = 60

	// A hard ceiling on messages leaving per minute, with the excess collapsed
	// into a single "N further messages suppressed" line. Without it one
	// flapping service turns the bot into a denial of service on the
	// operator's phone -- and on their own Freizone server, whose per-device
	// queue is bounded and answers 429 once full. The smart version (dedup
	// keys, firing/resolved pairing) is BOT-03; this crude cap ships first
	// because shipping without it is shipping a footgun.
	defaultRatePerMinute = 20

	// Beyond this the outbox refuses new messages, loudly, and `send` exits
	// non-zero. A loud rejection beats silent loss: the caller can still log
	// it or page a second way, where a dropped message is invisible.
	defaultOutboxMax = 1000

	// Maintenance runs on every stream connect, which is the app's rule and a
	// good one. This is the floor underneath it, and it is this bot's own
	// addition: a phone reconnects constantly -- screen off, network handover,
	// app resume -- while a server bot holds one stream for weeks, and then
	// none of the four maintenance calls would ever run again. The prekey pool
	// would drain toward zero with nothing to notice.
	defaultMaintenanceIntervalMinutes = 360
)

// Config holds all bot configuration.
type Config struct {
	// Server is the Freizone server this bot's account lives on. Required:
	// there is no sensible default, and guessing one would mean registering an
	// identity somewhere the operator did not choose.
	Server string

	StateDir string

	// InviteCode is resolved from either the variable or the file it names.
	// The file is the better habit and the README says so: an environment
	// variable is readable through /proc/<pid>/environ and `docker inspect`,
	// and an invite code is a credential.
	InviteCode string

	LogLevel slog.Level

	// ControlSocket is where the daemon listens for the CLI. Defaults inside
	// the state directory, so a CLI invocation that inherits the daemon's
	// environment needs no further configuration, and so a container's data
	// volume -- the one thing certainly shared -- carries it.
	ControlSocket string

	// ControlGroup, when set, owns the socket's parent directory, which is the
	// actual access gate (see internal/control). Empty leaves the directory to
	// the daemon's own user alone.
	ControlGroup string

	// RouteGroup and RoutePeers are independent, not alternatives: a message
	// goes to both when both are configured. That is what makes escalation
	// expressible -- the team channel and the person carrying the pager.
	RouteGroup string
	RoutePeers []string

	// RouteRules narrow where a message goes based on its labels, in order --
	// the first rule whose label matches decides. A message matching nothing,
	// and every message when this is empty, goes everywhere configured.
	//
	// On labels rather than on a severity field, because severity is only one
	// thing a message might be routed by: `kind=digest` to the group,
	// `severity=critical` to the pager, `repo=freizone-app` somewhere else
	// again. Hard-coding one of them would have made this an alerting tool.
	RouteRules []RouteRule

	// DedupWindow collapses repeats of the same message. Zero is off, which is
	// the default: the bot is a delivery path first, and deciding that two
	// alerts are "the same" is a judgement the monitoring system upstream is
	// usually better placed to make. Worth turning on for a check that is known
	// to flap.
	DedupWindow time.Duration

	// Commanders may command the bot. Empty disables the command surface
	// entirely -- fail closed, because an operator who has not thought about
	// who may drive their bot has not decided that everyone may. Never learned
	// from group membership, which drifts without anybody being told.
	Commanders []string

	// AllowGroupCommands lets commands be given in a group. Off by default: a
	// command in a group is visible to everyone in it, its answer is too, and
	// the membership changes without the operator.
	AllowGroupCommands bool

	// AcceptGroupInvites lets anybody who knows this address pull the bot into a
	// group of theirs. Off by default: an invitation the operator did not ask for
	// is a stranger deciding what this bot is a member of, and from then on it
	// holds that group's facts and receives its traffic. The configured route
	// group is always accepted regardless -- naming it *is* asking for it.
	AcceptGroupInvites bool

	// JokesFile replaces the built-in set for the joke action. Empty keeps the
	// built-in one.
	JokesFile string

	// ActionsFile declares actions in a file instead of in Go -- a fixed reply,
	// or an HTTP request to something that already exists. See internal/declared.
	ActionsFile string

	// WebhookAddr is where the HTTP ingress listens. Empty means there is none,
	// which is the default and the property worth keeping: the bot opens no
	// network listener unless somebody says so.
	WebhookAddr string

	// WebhookTokens is the file naming who may POST. Required whenever
	// WebhookAddr is set -- an open ingress is not an option this offers.
	WebhookTokens string

	MaxAge              time.Duration
	RatePerMinute       int
	OutboxMax           int
	MaintenanceInterval time.Duration
}

// Load reads configuration from getenv, which is injected so tests need no
// real environment.
func Load(getenv func(string) string) (*Config, error) {
	cfg := &Config{
		// The same rule as the server half of an address: a bare host gets
		// https, a scheme that was written out is respected, trailing slashes
		// go. Shared rather than re-derived -- it had already been written twice
		// before SRV-31, and a rule with two homes has a half-life.
		Server:        address.NormalizeServer(getenv(envServer)),
		StateDir:      orDefault(getenv(envStateDir), defaultStateDir),
		ControlSocket: strings.TrimSpace(getenv(envControlSocket)),
		ControlGroup:  strings.TrimSpace(getenv(envControlGroup)),
		RouteGroup:    strings.TrimSpace(getenv(envRouteGroup)),
		RoutePeers:    splitList(getenv(envRoutePeers)),
		Commanders:    splitList(getenv(envCommanders)),
		JokesFile:     strings.TrimSpace(getenv(envJokesFile)),
		ActionsFile:   strings.TrimSpace(getenv(envActionsFile)),
		WebhookAddr:   strings.TrimSpace(getenv(envWebhookAddr)),
		WebhookTokens: strings.TrimSpace(getenv(envWebhookTokens)),
	}

	level, err := parseLogLevel(orDefault(getenv(envLogLevel), "info"))
	if err != nil {
		return nil, err
	}
	cfg.LogLevel = level

	if cfg.AcceptGroupInvites, err = boolOr(getenv, envAcceptInvites, false); err != nil {
		return nil, err
	}
	if cfg.AllowGroupCommands, err = boolOr(getenv, envAllowGroupCommand, false); err != nil {
		return nil, err
	}

	if cfg.InviteCode, err = loadInviteCode(getenv); err != nil {
		return nil, err
	}

	minutes, err := positiveInt(getenv, envMaxAge, defaultMaxAgeMinutes)
	if err != nil {
		return nil, err
	}
	cfg.MaxAge = time.Duration(minutes) * time.Minute

	if cfg.RatePerMinute, err = positiveInt(getenv, envRate, defaultRatePerMinute); err != nil {
		return nil, err
	}
	if cfg.OutboxMax, err = positiveInt(getenv, envOutboxMax, defaultOutboxMax); err != nil {
		return nil, err
	}

	minutes, err = positiveInt(getenv, envMaintenanceInterval, defaultMaintenanceIntervalMinutes)
	if err != nil {
		return nil, err
	}
	cfg.MaintenanceInterval = time.Duration(minutes) * time.Minute

	// Zero is meaningful here (off), so this cannot go through positiveInt.
	dedupMinutes, err := nonNegativeInt(getenv, envDedupWindow, 0)
	if err != nil {
		return nil, err
	}
	cfg.DedupWindow = time.Duration(dedupMinutes) * time.Minute

	if cfg.RouteRules, err = parseRouteRules(getenv(envRouteRules)); err != nil {
		return nil, err
	}

	if cfg.ControlSocket == "" {
		cfg.ControlSocket = filepath.Join(cfg.StateDir, "control.sock")
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Server == "" {
		return fmt.Errorf("%s is required: the bot has no default server to register against", envServer)
	}
	if c.StateDir == "" {
		return fmt.Errorf("%s must not be empty", envStateDir)
	}
	if c.WebhookAddr != "" && c.WebhookTokens == "" {
		// An ingress with no allow-list is the one configuration this refuses to
		// assemble: it would accept a message from anything that can reach the
		// port, and this bot is a full member of somebody's group.
		return fmt.Errorf("%s is set but %s is not: an ingress with nobody authorised to use it would accept everything", envWebhookAddr, envWebhookTokens)
	}
	if c.WebhookTokens != "" && c.WebhookAddr == "" {
		// The mirror image, and a warning would be too quiet: somebody wrote a
		// token file expecting an ingress, and there is none.
		return fmt.Errorf("%s is set but %s is not, so nothing is listening for those senders", envWebhookTokens, envWebhookAddr)
	}

	if _, err := ParseGroupID(c.RouteGroup); err != nil {
		return fmt.Errorf("%s: %w", envRouteGroup, err)
	}
	if _, err := ParsePeers(c.RoutePeers); err != nil {
		// Checked here so a typo in an environment file fails at startup rather
		// than at the first message -- which is the worst possible moment to
		// discover that a recipient was never addressable.
		return fmt.Errorf("%s: %w", envRoutePeers, err)
	}

	// Deliberately not an error at load time: a bot with no route is a
	// perfectly good thing to start once, to register and print its address,
	// before the operator has invited it anywhere. The daemon refuses to run
	// without one; see RoutesConfigured.
	return nil
}

// AccountDir is where pkg/client keeps this bot's identity, sessions and
// transcripts. One process owns it at a time -- see internal/account.
func (c *Config) AccountDir() string { return filepath.Join(c.StateDir, "account") }

// MediaDir is separate because attachments are the one thing here that is
// large and worth putting on different storage.
func (c *Config) MediaDir() string { return filepath.Join(c.StateDir, "media") }

// OutboxDir holds messages accepted but not yet delivered.
func (c *Config) OutboxDir() string { return filepath.Join(c.StateDir, "outbox") }

// AddressFile holds the bot's own Freizone address, written once it has one.
//
// The single fact an operator must act on -- they cannot invite the bot
// anywhere without it -- and a log line at first start is often watched by
// nobody. See account.WriteAddress.
func (c *Config) AddressFile() string { return filepath.Join(c.StateDir, "address") }

// RoutesConfigured reports whether anything is configured to send to. Follows
// the gateway's FCMConfigured shape: an unconfigured subsystem is reported
// rather than defaulted into something.
func (c *Config) RoutesConfigured() bool {
	return c.RouteGroup != "" || len(c.RoutePeers) > 0
}

// CommandsConfigured reports whether the bot will answer anybody. Same shape
// again, and the same reason: an operator should be told that the command
// surface is off rather than discovering it by asking the bot something.
func (c *Config) CommandsConfigured() bool { return len(c.Commanders) > 0 }

func loadInviteCode(getenv func(string) string) (string, error) {
	inline := strings.TrimSpace(getenv(envInviteCode))
	path := strings.TrimSpace(getenv(envInviteFile))

	if inline != "" && path != "" {
		return "", fmt.Errorf("%s and %s are both set: pick one, so there is no question which code was used", envInviteCode, envInviteFile)
	}
	if path == "" {
		return inline, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", envInviteFile, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// KnownRoutes are the route names a rule may refer to. Kept here so config can
// reject a typo at load time rather than at the first message that uses it --
// discovering a misconfiguration during an incident is the wrong moment.
var KnownRoutes = []string{"group", "peers"}

// RouteRule sends messages carrying one label value to a particular set of
// routes.
type RouteRule struct {
	Label  string
	Value  string
	Routes []string
}

// MatchRoute returns the first rule whose label matches, or nil.
//
// First match rather than most specific: the order in the configuration is the
// operator's own statement of precedence, and inferring specificity from a set
// of equally-shaped rules would mean guessing at it.
func (c *Config) MatchRoute(labels map[string]string) *RouteRule {
	for i := range c.RouteRules {
		r := &c.RouteRules[i]
		if v, ok := labels[r.Label]; ok && strings.EqualFold(strings.TrimSpace(v), r.Value) {
			return r
		}
	}
	return nil
}

// parseRouteRules reads `severity:critical=group+peers,kind:digest=group`.
//
// Three separators, each for a different job: a colon between a label and its
// value, an equals before the routes, a plus joining routes, and a comma
// between rules. It looks like a lot until you try it with fewer -- using
// commas for both the rules and the routes would make
// `severity:critical=group,peers` ambiguous with a second rule.
func parseRouteRules(raw string) ([]RouteRule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []RouteRule
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue // a trailing comma is not a configuration error
		}
		match, routes, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("%s: %q is not label:value=route (e.g. severity:critical=group+peers)", envRouteRules, entry)
		}
		label, value, ok := strings.Cut(match, ":")
		if !ok {
			return nil, fmt.Errorf("%s: %q is missing the label (e.g. severity:critical=group)", envRouteRules, entry)
		}
		label = strings.TrimSpace(label)
		value = strings.TrimSpace(value)
		if label == "" || value == "" {
			return nil, fmt.Errorf("%s: %q names no label or no value", envRouteRules, entry)
		}

		var names []string
		for _, r := range strings.Split(routes, "+") {
			r = strings.ToLower(strings.TrimSpace(r))
			if r == "" {
				continue
			}
			if !slices.Contains(KnownRoutes, r) {
				return nil, fmt.Errorf("%s: unknown route %q in %q (known: %s)",
					envRouteRules, r, entry, strings.Join(KnownRoutes, ", "))
			}
			names = append(names, r)
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("%s: %q is mapped to no route", envRouteRules, entry)
		}
		out = append(out, RouteRule{Label: label, Value: value, Routes: names})
	}
	return out, nil
}

// nonNegativeInt is positiveInt where zero is a meaningful value rather than a
// typo -- a window of zero means "do not deduplicate".
func nonNegativeInt(getenv func(string) string, env string, def int) (int, error) {
	v := strings.TrimSpace(getenv(env))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid value %q (must be a whole number): %w", env, v, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s must not be negative, got %d", env, n)
	}
	return n, nil
}

func positiveInt(getenv func(string) string, env string, def int) (int, error) {
	v := strings.TrimSpace(getenv(env))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid value %q (must be a whole number): %w", env, v, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%s must be a positive number, got %d", env, n)
	}
	return n, nil
}

// splitList accepts a comma-separated list, ignoring empty entries so a
// trailing comma or a blank line in a systemd EnvironmentFile is not an error.
func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseLogLevel(v string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("%s: unknown level %q (debug, info, warn or error)", envLogLevel, v)
	}
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

// boolOr reads a boolean setting, or its default when unset.
func boolOr(getenv func(string) string, env string, def bool) (bool, error) {
	raw := strings.TrimSpace(getenv(env))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: invalid value %q (true or false): %w", env, raw, err)
	}
	return v, nil
}
