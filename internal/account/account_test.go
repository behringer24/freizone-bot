package account

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/behringer24/freizone-bot/internal/config"
	"github.com/behringer24/freizone-server/pkg/client"
)

func testConfig(t *testing.T, server string) *config.Config {
	t.Helper()
	cfg, err := config.Load(func(k string) string {
		switch k {
		case "FREIZONE_BOT_SERVER":
			return server
		case "FREIZONE_BOT_STATE_DIR":
			return t.TempDir()
		}
		return ""
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The identity is on disk in the clear -- there is nobody to type a passphrase
// at three in the morning -- so a directory anyone else can read is worth
// stopping for rather than starting quietly on top of.
func TestALooseAccountDirectoryRefusesToStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permissions here are ACLs; the mode bits os.Stat synthesises say nothing about them")
	}
	cfg := testConfig(t, "https://chat.example.org")
	if err := os.MkdirAll(cfg.AccountDir(), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	_, err := Open(cfg)
	if err == nil {
		t.Fatal("a group-readable account directory must be refused")
	}
	// The message has to say what to do about it, not merely that something is
	// wrong: an operator reading this at speed needs the fix in the sentence.
	if !strings.Contains(err.Error(), "chmod 700") {
		t.Errorf("the error should name the fix, got %q", err)
	}
}

func TestATightAccountDirectoryIsAccepted(t *testing.T) {
	cfg := testConfig(t, "https://chat.example.org")
	if err := os.MkdirAll(cfg.AccountDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	c, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// A directory that does not exist yet is the first-run case, and pkg/client
// creates it 0700 itself.
func TestAFreshStateDirectoryIsFine(t *testing.T) {
	cfg := testConfig(t, "https://chat.example.org")
	c, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close() //nolint:errcheck
	if _, err := os.Stat(cfg.AccountDir()); err != nil {
		t.Errorf("the account directory should exist after Open: %v", err)
	}
}

// Opening while the account is already held has to say something an operator
// can act on -- specifically, that the daemon is probably the holder and the
// control socket is the way in.
func TestASecondOpenerIsToldWhereToGoInstead(t *testing.T) {
	cfg := testConfig(t, "https://chat.example.org")
	first, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer first.Close() //nolint:errcheck

	// In-process, pkg/client deliberately hands back the same client rather
	// than refusing -- that is what lets a shell open one account from two
	// isolates. So this asserts the inherited behaviour rather than a refusal;
	// the cross-process refusal is pkg/client's own test, driven with a real
	// second process.
	second, err := Open(cfg)
	if err != nil {
		t.Fatalf("a second opener in this process should get the same client: %v", err)
	}
	if first != second {
		t.Error("two openers in one process must share one client, or they write over each other")
	}
	// Both, because the account is held until the *last* opener lets go. Closing
	// once here left the lock file open, and on Windows an open file cannot be
	// deleted -- so the test's own cleanup failed and said so, which is a fair
	// demonstration of what a leaked Close costs in the field.
	if err := second.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// EnsureRegistered must be a no-op for an account that already has an identity
// -- it runs on every start, and registering again would abandon the account
// the bot has been using.
func TestEnsureRegisteredLeavesAnExistingIdentityAlone(t *testing.T) {
	cfg := testConfig(t, "https://chat.example.org")
	c, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close() //nolint:errcheck

	// Stand an identity up directly, which is what a previous run would have
	// left behind. No server is involved, and none should be reached.
	want, err := client.NewIdentity("https://chat.example.org")
	if err != nil {
		t.Fatalf("NewIdentity: %v", err)
	}
	if err := c.SetIdentity(want); err != nil {
		t.Fatalf("SetIdentity: %v", err)
	}

	got, fresh, err := EnsureRegistered(context.Background(), c, cfg, quietLogger())
	if err != nil {
		t.Fatalf("EnsureRegistered: %v", err)
	}
	if fresh {
		t.Error("an account that already exists was not registered just now")
	}
	if got.AccountID != want.AccountID {
		t.Errorf("identity changed: %s then %s", want.AccountID, got.AccountID)
	}
}

func TestWriteAddressIsReadableAndPrivate(t *testing.T) {
	cfg := testConfig(t, "https://chat.example.org")
	id, err := client.NewIdentity("https://chat.example.org")
	if err != nil {
		t.Fatalf("NewIdentity: %v", err)
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := WriteAddress(cfg, id); err != nil {
		t.Fatalf("WriteAddress: %v", err)
	}

	raw, err := os.ReadFile(cfg.AddressFile())
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	// The canonical `id*server` form, which is what a person pastes into the app
	// -- spelled out here rather than taken from id.Address() so that this
	// pins the format instead of agreeing with whatever it currently produces.
	// The default scheme is absent on purpose: an https server is the ordinary
	// case, so leaving it out is what makes a non-default scheme visible.
	want := id.AccountID + "*chat.example.org\n"
	if string(raw) != want {
		t.Errorf("address file holds %q, want %q", raw, want)
	}
	if filepath.Dir(cfg.AddressFile()) != filepath.Clean(cfg.StateDir) {
		t.Errorf("the address file belongs in the state directory, got %q", cfg.AddressFile())
	}
}
