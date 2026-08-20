//go:build linux

package control

import (
	"fmt"
	"net"
	"syscall"
)

// peerDescription names who is on the other end, from the kernel rather than
// from anything the caller said about itself.
//
// Logged on every request because the control socket is an authentication
// boundary: "somebody made the bot say all-clear during the incident" is a
// question an operator will eventually need answered, and the socket's
// permissions alone cannot answer it after the fact.
//
// Linux only. SO_PEERCRED is not portable -- darwin has LOCAL_PEERCRED with a
// different shape, and Windows has none of it -- and a name for the caller is
// worth having where it is free rather than nowhere at all.
func peerDescription(conn net.Conn) string {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return "local"
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return "local"
	}

	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || credErr != nil || cred == nil {
		return "local"
	}
	return fmt.Sprintf("uid=%d gid=%d pid=%d", cred.Uid, cred.Gid, cred.Pid)
}
