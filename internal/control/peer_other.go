//go:build !linux

package control

import "net"

// No portable way to ask the kernel who is on the other end here: darwin's
// LOCAL_PEERCRED has a different shape, and Windows has nothing equivalent for
// this socket type. The socket's own permissions are still the gate; what is
// missing is only the record of who came through it.
//
// Named "local" rather than left empty so a log line reads the same shape on
// every platform, and so its absence is visibly a platform limit rather than a
// lookup that failed.
func peerDescription(conn net.Conn) string { return "local" }
