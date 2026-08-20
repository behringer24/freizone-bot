package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// ErrNoDaemon reports that nothing is listening. Its own type because it is the
// one failure with a specific thing to tell the caller -- start the service --
// and the one the CLI turns into its own exit code.
type ErrNoDaemon struct {
	Addr string
	Err  error
}

func (e *ErrNoDaemon) Error() string {
	return fmt.Sprintf("no daemon at %s (is the freizone-bot service running?)", e.Addr)
}

func (e *ErrNoDaemon) Unwrap() error { return e.Err }

// Do sends one request and reads the one answer, then closes. Deliberately not
// a persistent connection: the CLI is a one-shot, and a pool would be state to
// get wrong for no gain.
func Do(ctx context.Context, addr string, req Request, timeout time.Duration) (*Response, error) {
	req.V = ProtocolVersion

	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := dial(dialCtx, addr)
	if err != nil {
		// Anything that stops the dial reaching a listener is reported the same
		// way: from where the caller stands, a socket file that is not there and
		// one nobody is accepting on are the same problem with the same fix.
		return nil, &ErrNoDaemon{Addr: addr, Err: err}
	}
	defer conn.Close() //nolint:errcheck // the response is already read by then

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}

	line, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encoding the request: %w", err)
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("sending the request: %w", err)
	}

	// Bounded on this side too. A daemon is trusted more than a network peer,
	// but "trusted" is not a reason to read without limit from anything.
	reader := bufio.NewReader(io.LimitReader(conn, MaxRequestBytes))
	raw, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reading the answer: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("the daemon closed the connection without answering")
	}

	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decoding the answer: %w", err)
	}
	return &resp, nil
}

// Listen opens the socket for the daemon.
//
// The caller must already hold the account lock. That ordering is not
// incidental: a leftover socket file may only be removed once something proves
// no daemon is running, and the lock is that proof. Without it, "stale" and
// "a daemon is running" are indistinguishable, and clearing the file would
// steal a live daemon's socket.
func Listen(addr string) (net.Listener, error) { return listen(addr) }
