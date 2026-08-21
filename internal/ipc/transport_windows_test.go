//go:build windows

package ipc

import (
	"path/filepath"
	"strings"
	"testing"
)

// Refused rather than ignored. The mode bits on a Windows AF_UNIX socket are
// synthesised, so honouring a group here would be a claim this cannot keep --
// and a setting that appears to grant access without granting it is worse than
// one that is plainly unavailable. The real answer is a named pipe with an
// explicit security descriptor (BOT-07).
func TestAGroupIsRefusedRatherThanIgnored(t *testing.T) {
	addr := filepath.Join(t.TempDir(), "sock", "control.sock")

	ln, err := listen(addr, "operators")
	if err == nil {
		ln.Close() //nolint:errcheck // cleaning up an unexpected success
		t.Fatal("a group should not be silently accepted on Windows")
	}
	if !strings.Contains(err.Error(), "not supported on Windows") {
		t.Errorf("the error should say why, got %q", err)
	}
}

func TestWithoutAGroupItListens(t *testing.T) {
	addr := filepath.Join(t.TempDir(), "sock", "control.sock")
	ln, err := listen(addr, "")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Errorf("closing: %v", err)
	}
}
