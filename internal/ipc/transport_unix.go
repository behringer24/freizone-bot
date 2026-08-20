//go:build !windows

package ipc

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// A unix socket, and the *directory* is the access gate rather than the
// socket's own mode.
//
// bind(2) applies the umask on some platforms, and between Listen and a later
// Chmod there is a real window in which the socket is more permissive than
// intended. A directory created 0750 before anything is bound has no such
// window: traversal is denied there, before the socket's mode is ever
// consulted. The socket gets 0660 as well, as belt and braces.
//
// This matters more than it looks. Whoever can write to this socket can make
// the bot say "false alarm, all clear" into the alert group during a real
// incident -- suppression is as damaging here as spam.

func dial(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", addr)
}

func listen(addr string) (net.Listener, error) {
	dir := filepath.Dir(addr)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("creating the socket directory: %w", err)
	}
	// Enforced even when the directory already existed, since a restore from an
	// archive or a careless chmod is exactly how it would have loosened.
	if err := os.Chmod(dir, 0o750); err != nil {
		return nil, fmt.Errorf("tightening the socket directory: %w", err)
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
	if err := os.Chmod(addr, 0o660); err != nil {
		ln.Close() //nolint:errcheck // returning the more useful error
		return nil, fmt.Errorf("tightening the socket: %w", err)
	}
	return ln, nil
}
