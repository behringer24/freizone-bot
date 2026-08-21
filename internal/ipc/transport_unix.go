//go:build !windows

package ipc

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
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

func listen(addr, group string) (net.Listener, error) {
	dir := filepath.Dir(addr)

	// Resolved before anything is created, so a group that does not exist is a
	// refusal to start rather than a socket nobody asked for the wrong people
	// to reach.
	gid := -1
	if group != "" {
		resolved, err := groupID(group)
		if err != nil {
			return nil, err
		}
		gid = resolved
	}

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

	if gid >= 0 {
		// Both, and in this order: the directory is the gate, the socket is the
		// thing written to, and granting one without the other grants nothing.
		//
		// A failure here stops the daemon rather than warning. An operator who
		// named a group believes they have given specific people the ability to
		// send -- and this setting used to be read from the environment and then
		// ignored entirely, which is the failure mode worth being loud about:
		// they had configured access that did not exist, and nothing said so.
		for _, path := range []string{dir, addr} {
			if err := os.Chown(path, -1, gid); err != nil {
				ln.Close() //nolint:errcheck // returning the more useful error
				return nil, fmt.Errorf("giving group %q access to %s: %w "+
					"(the daemon's own user has to be a member of that group, or root, to hand it out)",
					group, path, err)
			}
		}
	}
	return ln, nil
}

// groupID looks a group up by name, or accepts a numeric id -- a container
// often has no /etc/group entry for the host group whose numeric id was mounted
// in, so requiring a name would make the container case unconfigurable.
func groupID(group string) (int, error) {
	if id, err := strconv.Atoi(group); err == nil {
		if id < 0 {
			return 0, fmt.Errorf("group %q is not a group id", group)
		}
		return id, nil
	}
	found, err := user.LookupGroup(group)
	if err != nil {
		return 0, fmt.Errorf("looking up group %q: %w", group, err)
	}
	id, err := strconv.Atoi(found.Gid)
	if err != nil {
		return 0, fmt.Errorf("group %q has an unreadable id %q", group, found.Gid)
	}
	return id, nil
}
