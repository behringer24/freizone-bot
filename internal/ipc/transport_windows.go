//go:build windows

package ipc

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// Windows also has AF_UNIX (Windows 10 1803 and later, supported by Go since
// 1.12), so the same socket works here and development on Windows needs no
// second mechanism.
//
// What does *not* carry over is the permission model. The unix side leans on
// the directory being 0750; here the mode bits are synthesised and mean
// nothing, so access is whatever the directory's ACL inherits -- which for a
// path under the state directory is the account that created it, and in
// practice fine for a development machine.
//
// It is not enough for a hardened deployment, and that is BOT-07: a named pipe
// with an explicit security descriptor denying remote access, since a named
// pipe is reachable over SMB by default if its descriptor permits it. The seam
// is this file, so that change touches nothing else.

func dial(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", addr)
}

func listen(addr string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(addr), 0o750); err != nil {
		return nil, fmt.Errorf("creating the socket directory: %w", err)
	}

	// Safe only because the caller holds the account lock -- see Listen's
	// comment. A leftover file here is definitively garbage at this point.
	if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("clearing the old socket: %w", err)
	}

	ln, err := net.Listen("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", addr, err)
	}
	return ln, nil
}
