//go:build !windows

package ipc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGroupIDAcceptsANameOrANumber(t *testing.T) {
	// A numeric id, because a container often has no /etc/group entry for the
	// host group whose numeric id was mounted in -- requiring a name would make
	// the container case unconfigurable.
	for _, in := range []string{"0", "65534"} {
		if _, err := groupID(in); err != nil {
			t.Errorf("groupID(%q): %v", in, err)
		}
	}
	for _, in := range []string{"-1", "no-such-group-here-hopefully"} {
		if _, err := groupID(in); err == nil {
			t.Errorf("groupID(%q) should have failed", in)
		}
	}
}

// A group that cannot be resolved stops the daemon before anything is created.
// The alternative is what this setting used to do: get read from the
// environment and then ignored, so an operator had configured access that did
// not exist and nothing said so.
func TestAnUnknownGroupIsRefusedBeforeTheSocketExists(t *testing.T) {
	addr := filepath.Join(t.TempDir(), "sock", "control.sock")

	ln, err := listen(addr, "no-such-group-here-hopefully")
	if err == nil {
		ln.Close() //nolint:errcheck // cleaning up an unexpected success
		t.Fatal("an unresolvable group should have been refused")
	}
	if !strings.Contains(err.Error(), "no-such-group-here-hopefully") {
		t.Errorf("the error should name the group, got %q", err)
	}
	if _, statErr := os.Stat(addr); statErr == nil {
		t.Error("the socket was created despite the refusal")
	}
}

// The ordinary case still works, and the directory is still the gate.
func TestNoGroupLeavesTheDirectoryTight(t *testing.T) {
	addr := filepath.Join(t.TempDir(), "sock", "control.sock")
	ln, err := listen(addr, "")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck // nothing to do about it in a test

	info, err := os.Stat(filepath.Dir(addr))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o750 {
		t.Errorf("socket directory is %o, want 750", perm)
	}
}
