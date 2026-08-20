// Package control is the daemon's side of the local socket.
//
// It is an authentication boundary, and worth naming as one: whoever can write
// here can make the bot say "false alarm, all clear" into the alert group
// during a real incident. Suppression is as damaging as spam, so the gate is
// the socket directory's permissions (see internal/ipc) rather than anything
// this file checks.
package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/behringer24/freizone-bot/internal/ipc"
)

// requestTimeout bounds one exchange. Generous, because a request that asked to
// wait for delivery legitimately takes as long as a send does.
const requestTimeout = 2 * time.Minute

// Handler answers one request. Returning an *ipc.Error is a refusal the caller
// should see verbatim; any other error is reported as internal, because an
// unexpected failure's text is for the log rather than for whoever typed the
// command.
type Handler func(ctx context.Context, req ipc.Request) (any, error)

// Server accepts connections on the local socket.
type Server struct {
	ln       net.Listener
	logger   *slog.Logger
	version  string
	handlers map[string]Handler

	wg sync.WaitGroup

	mu      sync.Mutex
	closing bool
}

// New wraps a listener. The caller opens it (via ipc.Listen) because opening it
// is only safe once the account lock is held, and this package has no business
// knowing about that.
func New(ln net.Listener, version string, logger *slog.Logger, handlers map[string]Handler) *Server {
	return &Server{ln: ln, logger: logger, version: version, handlers: handlers}
}

// Serve accepts until the listener is closed.
func (s *Server) Serve() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			s.mu.Lock()
			closing := s.closing
			s.mu.Unlock()
			if closing || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

// Shutdown stops accepting and waits for the requests already in flight.
//
// Called first in the daemon's shutdown, before anything else: accepting a
// message after deciding to stop would mean acknowledging something that will
// only be delivered after the restart, which is the one thing an alerting tool
// must not do quietly.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()

	err := s.ln.Close()

	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return err
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close() //nolint:errcheck // the answer is already written by then
	_ = conn.SetDeadline(time.Now().Add(requestTimeout))

	peer := peerDescription(conn)

	// Bounded: `journalctl -b | freizone-bot send` is a thing somebody types,
	// and an unbounded read here is an out-of-memory kill of the process holding
	// this bot's private keys.
	reader := bufio.NewReader(io.LimitReader(conn, ipc.MaxRequestBytes))
	line, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		s.logger.Warn("control request could not be read", "peer", peer, "error", err)
		return
	}

	var req ipc.Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(conn, peer, nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: "malformed request"})
		return
	}

	// A version mismatch is real rather than theoretical: the CLI and the daemon
	// are the same binary, but a package upgrade replaces the file while the
	// running daemon keeps its own code.
	if req.V > ipc.ProtocolVersion {
		s.reply(conn, peer, nil, &ipc.Error{
			Code: ipc.CodeVersionMismatch,
			Message: fmt.Sprintf("this daemon speaks version %d and was asked for %d -- "+
				"the running service is older than the command you ran", ipc.ProtocolVersion, req.V),
		})
		return
	}

	handler, ok := s.handlers[req.Op]
	if !ok {
		s.reply(conn, peer, nil, &ipc.Error{
			Code:    ipc.CodeUnknownOp,
			Message: fmt.Sprintf("this daemon does not know the operation %q", req.Op),
		})
		return
	}

	s.logger.Debug("control request", "op", req.Op, "peer", peer)

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	body, err := handler(ctx, req)
	if err != nil {
		var refusal *ipc.Error
		if errors.As(err, &refusal) {
			s.reply(conn, peer, nil, refusal)
			return
		}
		// Logged in full, reported in short: an unexpected failure's detail
		// belongs in the log, not in front of whoever typed a command.
		s.logger.Error("control request failed", "op", req.Op, "peer", peer, "error", err)
		s.reply(conn, peer, nil, &ipc.Error{Code: ipc.CodeInternal, Message: "the daemon could not carry that out"})
		return
	}
	s.reply(conn, peer, body, nil)
}

func (s *Server) reply(conn net.Conn, peer string, body any, refusal *ipc.Error) {
	resp := ipc.Response{V: ipc.ProtocolVersion, OK: refusal == nil, Version: s.version, Error: refusal}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			s.logger.Error("encoding a control answer", "error", err)
			resp = ipc.Response{V: ipc.ProtocolVersion, Version: s.version,
				Error: &ipc.Error{Code: ipc.CodeInternal, Message: "the daemon could not encode its answer"}}
		} else {
			resp.Body = raw
		}
	}

	line, err := json.Marshal(resp)
	if err != nil {
		s.logger.Error("encoding a control answer", "error", err)
		return
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		s.logger.Debug("could not answer a control request", "peer", peer, "error", err)
	}
}
