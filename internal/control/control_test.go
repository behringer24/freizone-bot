package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/behringer24/freizone-bot/internal/ipc"
)

// serve stands a real server on a real socket up, so every test here is an
// actual connection rather than a handler called directly. The socket is the
// part that can be wrong.
func serve(t *testing.T, handlers map[string]Handler) string {
	t.Helper()
	// Short path: a unix socket address has a low length limit on some
	// platforms, and t.TempDir() nests deeply.
	addr := filepath.Join(t.TempDir(), "c.sock")

	ln, err := ipc.Listen(addr)
	if err != nil {
		t.Fatalf("ipc.Listen: %v", err)
	}
	s := New(ln, "test-build", slog.New(slog.NewTextHandler(io.Discard, nil)), handlers)
	go s.Serve() //nolint:errcheck // Serve returns nil on a closed listener
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return addr
}

func do(t *testing.T, addr string, req ipc.Request) *ipc.Response {
	t.Helper()
	resp, err := ipc.Do(context.Background(), addr, req, 5*time.Second)
	if err != nil {
		t.Fatalf("ipc.Do: %v", err)
	}
	return resp
}

func TestARequestGetsItsAnswerBack(t *testing.T) {
	addr := serve(t, map[string]Handler{
		ipc.OpStatus: func(context.Context, ipc.Request) (any, error) {
			return ipc.StatusResponse{Address: "qbot*https://chat.example.org", Connected: true}, nil
		},
	})

	resp := do(t, addr, ipc.Request{Op: ipc.OpStatus})
	if !resp.OK {
		t.Fatalf("want success, got %+v", resp.Error)
	}
	// Every answer carries the daemon's build, so a CLI from a different one can
	// name both versions rather than failing obscurely.
	if resp.Version != "test-build" {
		t.Errorf("version: got %q", resp.Version)
	}
	var body ipc.StatusResponse
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decoding the body: %v", err)
	}
	if body.Address != "qbot*https://chat.example.org" || !body.Connected {
		t.Errorf("body did not survive the round trip: %+v", body)
	}
}

// The version-mismatch case is real rather than theoretical: the CLI and the
// daemon are the same binary, but a package upgrade replaces the file on disk
// while the running daemon keeps its own code.
func TestANewerRequestIsRefusedWithBothVersions(t *testing.T) {
	addr := serve(t, map[string]Handler{
		ipc.OpStatus: func(context.Context, ipc.Request) (any, error) { return ipc.StatusResponse{}, nil },
	})

	// ipc.Do stamps the current version, so this goes over the wire by hand.
	resp, err := rawRequest(addr, `{"v":99,"op":"status"}`)
	if err != nil {
		t.Fatalf("raw request: %v", err)
	}
	if resp.OK {
		t.Fatal("a request from a newer protocol must be refused")
	}
	if resp.Error.Code != ipc.CodeVersionMismatch {
		t.Errorf("code: got %q", resp.Error.Code)
	}
	// Naming both is the point -- "version mismatch" alone leaves an operator
	// with nothing to do about it.
	if !strings.Contains(resp.Error.Message, "99") {
		t.Errorf("the refusal should name what was asked for, got %q", resp.Error.Message)
	}
}

func TestAnUnknownOperationIsNamed(t *testing.T) {
	addr := serve(t, map[string]Handler{})

	resp := do(t, addr, ipc.Request{Op: "teleport"})
	if resp.OK {
		t.Fatal("an unknown operation must be refused")
	}
	if resp.Error.Code != ipc.CodeUnknownOp {
		t.Errorf("code: got %q", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "teleport") {
		t.Errorf("the refusal should name the operation, got %q", resp.Error.Message)
	}
}

// A refusal the handler chose reaches the caller verbatim; anything unexpected
// does not -- its detail belongs in the log rather than in front of whoever
// typed a command.
func TestAHandlersOwnRefusalIsPassedThroughButAPanicIsNot(t *testing.T) {
	addr := serve(t, map[string]Handler{
		"refuse": func(context.Context, ipc.Request) (any, error) {
			return nil, &ipc.Error{Code: ipc.CodeOutboxFull, Message: "the outbox is full (1000 held, limit 1000)"}
		},
		"break": func(context.Context, ipc.Request) (any, error) {
			return nil, errors.New("dial tcp 10.0.0.1:5432: connect: connection refused")
		},
	})

	refused := do(t, addr, ipc.Request{Op: "refuse"})
	if refused.OK || refused.Error.Code != ipc.CodeOutboxFull {
		t.Fatalf("a chosen refusal should come through as-is, got %+v", refused.Error)
	}
	if !strings.Contains(refused.Error.Message, "1000") {
		t.Errorf("its own message should survive, got %q", refused.Error.Message)
	}

	broke := do(t, addr, ipc.Request{Op: "break"})
	if broke.OK || broke.Error.Code != ipc.CodeInternal {
		t.Fatalf("an unexpected failure should be reported as internal, got %+v", broke.Error)
	}
	if strings.Contains(broke.Error.Message, "10.0.0.1") {
		t.Errorf("internal detail must not reach the caller, got %q", broke.Error.Message)
	}
}

func TestMalformedJSONIsRefusedRatherThanIgnored(t *testing.T) {
	addr := serve(t, map[string]Handler{})

	resp, err := rawRequest(addr, `{not json`)
	if err != nil {
		t.Fatalf("raw request: %v", err)
	}
	if resp.OK || resp.Error.Code != ipc.CodeBadRequest {
		t.Errorf("want a bad-request refusal, got %+v", resp)
	}
}

// No daemon is its own error, because it is the one with a specific thing to
// tell the caller: start the service.
func TestNoDaemonSaysWhatToDo(t *testing.T) {
	addr := filepath.Join(t.TempDir(), "nothing.sock")

	_, err := ipc.Do(context.Background(), addr, ipc.Request{Op: ipc.OpStatus}, time.Second)
	var noDaemon *ipc.ErrNoDaemon
	if !errors.As(err, &noDaemon) {
		t.Fatalf("want ErrNoDaemon, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "service running") {
		t.Errorf("the error should suggest the fix, got %q", err)
	}
}

// Shutting down stops accepting. The daemon closes this first on the way out, so
// that nothing is told "safely queued" about a message that will only be
// delivered after the restart.
func TestShutdownStopsAccepting(t *testing.T) {
	addr := filepath.Join(t.TempDir(), "c.sock")
	ln, err := ipc.Listen(addr)
	if err != nil {
		t.Fatalf("ipc.Listen: %v", err)
	}
	s := New(ln, "test", slog.New(slog.NewTextHandler(io.Discard, nil)), map[string]Handler{
		ipc.OpStatus: func(context.Context, ipc.Request) (any, error) { return ipc.StatusResponse{}, nil },
	})
	served := make(chan error, 1)
	go func() { served <- s.Serve() }()

	if resp := do(t, addr, ipc.Request{Op: ipc.OpStatus}); !resp.OK {
		t.Fatal("it should work before shutting down")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	// A closed listener is not an error to report: it is what shutting down is.
	if err := <-served; err != nil {
		t.Errorf("Serve should end quietly, got %v", err)
	}

	if _, err := ipc.Do(context.Background(), addr, ipc.Request{Op: ipc.OpStatus}, time.Second); err == nil {
		t.Error("a request after shutdown must not be accepted")
	}
}
