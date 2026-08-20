package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// envMap turns a map into the getenv function Load takes, so a test never
// touches the real environment.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestServerIsRequired(t *testing.T) {
	_, err := Load(envMap(nil))
	if err == nil {
		t.Fatal("a bot with no server must not start: it would have to guess where to register an identity")
	}
	if !strings.Contains(err.Error(), envServer) {
		t.Errorf("the error has to name the variable to set, got %q", err)
	}
}

func TestDefaultsLandWhereTheDocsSayTheyDo(t *testing.T) {
	cfg, err := Load(envMap(map[string]string{envServer: "chat.example.org"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StateDir != defaultStateDir {
		t.Errorf("state dir: want %q, got %q", defaultStateDir, cfg.StateDir)
	}
	if cfg.MaxAge != defaultMaxAgeMinutes*time.Minute {
		t.Errorf("max age: want %v, got %v", defaultMaxAgeMinutes*time.Minute, cfg.MaxAge)
	}
	if cfg.RatePerMinute != defaultRatePerMinute {
		t.Errorf("rate: want %d, got %d", defaultRatePerMinute, cfg.RatePerMinute)
	}
	if cfg.MaintenanceInterval != defaultMaintenanceIntervalMinutes*time.Minute {
		t.Errorf("maintenance interval: want %v, got %v", defaultMaintenanceIntervalMinutes*time.Minute, cfg.MaintenanceInterval)
	}
}

// The socket has to land inside the state directory by default, because that
// is what lets a CLI invocation inheriting the daemon's environment find it
// with no further configuration -- and what makes a container's data volume
// carry it.
func TestControlSocketDefaultsIntoTheStateDirectory(t *testing.T) {
	cfg, err := Load(envMap(map[string]string{
		envServer:   "chat.example.org",
		envStateDir: "/var/lib/freizone-bot",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join("/var/lib/freizone-bot", "control.sock")
	if cfg.ControlSocket != want {
		t.Errorf("control socket: want %q, got %q", want, cfg.ControlSocket)
	}
}

func TestAnExplicitControlSocketWins(t *testing.T) {
	cfg, err := Load(envMap(map[string]string{
		envServer:        "chat.example.org",
		envStateDir:      "/var/lib/freizone-bot",
		envControlSocket: "/run/freizone-bot/control.sock",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ControlSocket != "/run/freizone-bot/control.sock" {
		t.Errorf("an explicitly set socket must not be overridden, got %q", cfg.ControlSocket)
	}
}

// Both routes are independent rather than alternatives: the team channel and
// the person carrying the pager are not an either/or.
func TestBothRoutesCanBeConfiguredAtOnce(t *testing.T) {
	cfg, err := Load(envMap(map[string]string{
		envServer:     "chat.example.org",
		envRouteGroup: "plfxcdsa42x4xe4zr2mju",
		envRoutePeers: "qlfxcdsa42x4xe4gwjcnu, qu0qmxckqmum0dv77pndv ,",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RouteGroup != "plfxcdsa42x4xe4zr2mju" {
		t.Errorf("group route: got %q", cfg.RouteGroup)
	}
	// A trailing comma and stray spaces are what an EnvironmentFile edited by
	// hand actually looks like; they are not a configuration error.
	if len(cfg.RoutePeers) != 2 || cfg.RoutePeers[0] != "qlfxcdsa42x4xe4gwjcnu" || cfg.RoutePeers[1] != "qu0qmxckqmum0dv77pndv" {
		t.Errorf("peer route: got %#v", cfg.RoutePeers)
	}
	if !cfg.RoutesConfigured() {
		t.Error("routes are configured, so RoutesConfigured must say so")
	}
}

// Starting once with no route is legitimate: that is how an operator gets the
// bot registered and learns its address before inviting it anywhere.
func TestNoRouteIsNotALoadTimeError(t *testing.T) {
	cfg, err := Load(envMap(map[string]string{envServer: "chat.example.org"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RoutesConfigured() {
		t.Error("nothing was configured, so RoutesConfigured must report that")
	}
}

func TestAnInviteCodeMayComeFromAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invite")
	// Trailing newline is what an editor leaves behind, and it must not become
	// part of the code.
	if err := os.WriteFile(path, []byte("ABCD-EFGH-JKMN\n"), 0o600); err != nil {
		t.Fatalf("writing the invite file: %v", err)
	}
	cfg, err := Load(envMap(map[string]string{
		envServer:     "chat.example.org",
		envInviteFile: path,
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.InviteCode != "ABCD-EFGH-JKMN" {
		t.Errorf("invite code: got %q", cfg.InviteCode)
	}
}

// Two sources for one credential means nobody can say afterwards which was
// used, so it is refused rather than resolved by precedence.
func TestBothInviteSourcesTogetherIsRefused(t *testing.T) {
	_, err := Load(envMap(map[string]string{
		envServer:     "chat.example.org",
		envInviteCode: "ABCD-EFGH-JKMN",
		envInviteFile: "/somewhere/invite",
	}))
	if err == nil {
		t.Fatal("setting both the invite code and its file must be refused")
	}
}

func TestABadNumberNamesTheVariable(t *testing.T) {
	for _, env := range []string{envRate, envOutboxMax, envMaxAge, envMaintenanceInterval} {
		_, err := Load(envMap(map[string]string{envServer: "chat.example.org", env: "nonsense"}))
		if err == nil {
			t.Fatalf("%s: a non-numeric value must be refused", env)
		}
		if !strings.Contains(err.Error(), env) {
			t.Errorf("%s: the error must name the variable, got %q", env, err)
		}
	}
}

// Zero would mean "never deliver anything" for the rate cap and "hold nothing"
// for the outbox -- both are almost certainly a typo rather than an intent.
func TestZeroAndNegativeAreRefused(t *testing.T) {
	for _, v := range []string{"0", "-1"} {
		_, err := Load(envMap(map[string]string{envServer: "chat.example.org", envRate: v})) //nolint:gosec
		if err == nil {
			t.Errorf("a rate of %s must be refused", v)
		}
	}
}

func TestDerivedPathsHangOffTheStateDirectory(t *testing.T) {
	cfg, err := Load(envMap(map[string]string{
		envServer:   "chat.example.org",
		envStateDir: "/srv/bot",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for name, got := range map[string]string{
		"account": cfg.AccountDir(),
		"media":   cfg.MediaDir(),
		"outbox":  cfg.OutboxDir(),
		"address": cfg.AddressFile(),
	} {
		if !strings.HasPrefix(got, filepath.Clean("/srv/bot")) {
			t.Errorf("%s path escaped the state directory: %q", name, got)
		}
	}
}

func TestLogLevelIsForgivingButNotSilent(t *testing.T) {
	if _, err := Load(envMap(map[string]string{envServer: "s", envLogLevel: "WARNING"})); err != nil {
		t.Errorf("WARNING should be accepted: %v", err)
	}
	if _, err := Load(envMap(map[string]string{envServer: "s", envLogLevel: "chatty"})); err == nil {
		t.Error("an unknown level must be refused rather than silently defaulted")
	}
}
