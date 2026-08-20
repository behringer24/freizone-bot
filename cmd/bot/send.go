package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/behringer24/freizone-bot/internal/config"
	"github.com/behringer24/freizone-bot/internal/ipc"
)

// Exit codes, because this ends up inside shell scripts and a script can only
// branch on a number. Documented in the README as part of the contract.
const (
	exitUsage      = 1
	exitRefused    = 2 // the daemon said no: unknown route, outbox full
	exitDaemonErr  = 3
	exitNoDaemon   = 4
	exitTimeoutErr = 5
)

// exitError carries a code out of runSend, so main can keep its one error path.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func runSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	title := fs.String("title", "", "the headline: what happened, in one line")
	severity := fs.String("severity", "", "free-form severity, shown in front of the title (e.g. info, warning, critical)")
	source := fs.String("source", "", "where this came from -- a host, a unit, a job")
	route := fs.String("route", "", "send only to one configured route (\"group\" or \"peers\"); default is every configured route")
	wait := fs.Bool("wait", false, "wait for delivery instead of returning once the message is durably queued")
	timeout := fs.Duration("timeout", 10*time.Second, "how long to wait for the daemon")
	fs.Parse(args) //nolint:errcheck // ExitOnError

	text, err := messageText(fs.Args())
	if err != nil {
		return &exitError{exitUsage, err}
	}
	if text == "" && *title == "" {
		return &exitError{exitUsage, fmt.Errorf("nothing to send: give a message as an argument, on stdin, or use -title")}
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return &exitError{exitUsage, fmt.Errorf("loading configuration: %w", err)}
	}

	body, err := json.Marshal(ipc.SendRequest{
		Title: *title, Text: text, Severity: *severity, Source: *source,
		At: time.Now().UTC(), Route: *route, Wait: *wait,
	})
	if err != nil {
		return &exitError{exitUsage, err}
	}

	// The wait case legitimately takes as long as a send does, so the flag
	// raises the ceiling rather than the caller having to know to.
	deadline := *timeout
	if *wait && deadline < time.Minute {
		deadline = time.Minute
	}

	resp, err := ipc.Do(context.Background(), cfg.ControlSocket,
		ipc.Request{Op: ipc.OpSend, Body: body}, deadline)
	if err != nil {
		var noDaemon *ipc.ErrNoDaemon
		if errors.As(err, &noDaemon) {
			return &exitError{exitNoDaemon, err}
		}
		if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
			return &exitError{exitTimeoutErr, err}
		}
		return &exitError{exitDaemonErr, err}
	}
	if resp.Error != nil {
		code := exitDaemonErr
		switch resp.Error.Code {
		case ipc.CodeNoRoute, ipc.CodeOutboxFull, ipc.CodeBadRequest:
			code = exitRefused
		}
		return &exitError{code, errors.New(resp.Error.Message)}
	}

	var out ipc.SendResponse
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			return &exitError{exitDaemonErr, fmt.Errorf("decoding the answer: %w", err)}
		}
	}

	switch {
	case out.Suppressed:
		// Not an error -- the cap did its job -- but the caller must not be left
		// believing the message went.
		fmt.Fprintln(os.Stderr, "suppressed by the rate limit; it will be counted on the next message that gets through")
	case *wait:
		fmt.Printf("delivered to %d of %d\n", out.Delivered, out.Queued)
	default:
		fmt.Printf("queued for %d destination(s)\n", out.Queued)
	}
	return nil
}

// messageText takes the body from the arguments, or from standard input when no
// argument was given.
//
// **An argument means standard input is never read**, and that rule is worth its
// paragraph because the obvious alternative hangs. Reading whenever stdin is not
// a terminal sounds harmless -- a pipe has an end, after all -- but under a
// service manager, a CI runner or cron, stdin is routinely an open pipe that
// nobody ever writes to and nobody closes. `send "disk full"` then blocks
// forever waiting for an EOF that is not coming: an alerting tool hanging at the
// exact moment it is needed, which is the worst failure it has available.
//
// So the rule is positional: text in the arguments means the text is there.
// Nothing there means it comes from stdin, which is the `cmd | send` case where
// the producer does close it. The convenience of accepting both at once is not
// worth a hang.
func messageText(args []string) (string, error) {
	if fromArgs := strings.TrimSpace(strings.Join(args, " ")); fromArgs != "" {
		return fromArgs, nil
	}

	info, err := os.Stdin.Stat()
	if err != nil {
		return "", nil // nothing to read from; -title alone may still carry it
	}
	// A character device is a terminal: somebody is typing, not piping, and
	// reading would wait for a Ctrl-D they have no reason to expect to give.
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}

	piped, err := io.ReadAll(io.LimitReader(os.Stdin, ipc.MaxRequestBytes))
	if err != nil {
		return "", fmt.Errorf("reading standard input: %w", err)
	}
	return strings.TrimRight(string(piped), "\n"), nil
}

// runStatus asks the daemon how it is doing.
func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	timeout := fs.Duration("timeout", 10*time.Second, "how long to wait for the daemon")
	fs.Parse(args) //nolint:errcheck // ExitOnError

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return &exitError{exitUsage, fmt.Errorf("loading configuration: %w", err)}
	}

	resp, err := ipc.Do(context.Background(), cfg.ControlSocket, ipc.Request{Op: ipc.OpStatus}, *timeout)
	if err != nil {
		var noDaemon *ipc.ErrNoDaemon
		if errors.As(err, &noDaemon) {
			return &exitError{exitNoDaemon, err}
		}
		return &exitError{exitDaemonErr, err}
	}
	if resp.Error != nil {
		return &exitError{exitDaemonErr, errors.New(resp.Error.Message)}
	}

	var out ipc.StatusResponse
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return &exitError{exitDaemonErr, fmt.Errorf("decoding the answer: %w", err)}
	}
	fmt.Printf("address:   %s\n", out.Address)
	fmt.Printf("connected: %t\n", out.Connected)
	fmt.Printf("outbox:    %d waiting\n", out.Outbox)
	if out.RouteGroup != "" {
		fmt.Printf("route:     group %s\n", out.RouteGroup)
	}
	if out.RoutePeers > 0 {
		fmt.Printf("route:     %d peer(s)\n", out.RoutePeers)
	}
	fmt.Printf("uptime:    %s\n", out.Uptime)
	fmt.Printf("daemon:    %s\n", resp.Version)
	return nil
}
