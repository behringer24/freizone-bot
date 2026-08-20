// Package ipc is the wire between the CLI and the daemon.
//
// # Why a socket at all
//
// The daemon owns the account directory, and pkg/client refuses a second
// process that tries to open it -- because two processes writing one account's
// ratchet state corrupt it. So a one-shot `freizone-bot send` cannot do the
// work itself; it has to ask the process that holds the account.
//
// # Why not HTTP over that socket
//
// Tempting, and it would reuse freizone-gateway's whole api idiom. Rejected
// because it turns "this bot opens no network listener" from a property of the
// *code* into a property of the *configuration*: once the handlers are
// http.Handlers, exposing them on a port is a two-line change, and the decision
// to accept requests from the network -- which deserves its own authentication
// design and its own release -- gets taken by accident instead.
//
// So: newline-delimited JSON, one request, one response, close.
package ipc

import (
	"encoding/json"
	"time"
)

// ProtocolVersion guards against a mismatched pair. The CLI and the daemon are
// the same binary, but not necessarily the same *build*: a package upgrade or a
// `docker pull` replaces the file on disk while the running daemon keeps its own
// code, and the next `send` is then a different version talking to it.
const ProtocolVersion = 1

// MaxRequestBytes bounds one request. A local channel still needs a bound:
// `journalctl -b | freizone-bot send` is a thing somebody will type, and an
// unbounded read turns it into an out-of-memory kill of the process holding this
// bot's private keys.
const MaxRequestBytes = 1 << 20 // 1 MiB

// Operations. Strings rather than numbers so an unknown one can be reported by
// name, which is what makes a version mismatch legible instead of puzzling.
const (
	OpSend   = "send"
	OpStatus = "status"
)

// Error codes, so the CLI can choose an exit code without matching on prose.
const (
	CodeUnknownOp       = "unknown_op"
	CodeVersionMismatch = "version_mismatch"
	CodeBadRequest      = "bad_request"
	CodeNoRoute         = "no_route"
	CodeOutboxFull      = "outbox_full"
	CodeInternal        = "internal"
)

// Request is one thing asked of the daemon.
type Request struct {
	V    int             `json:"v"`
	Op   string          `json:"op"`
	Body json.RawMessage `json:"body,omitempty"`
}

// Response is the daemon's single answer.
type Response struct {
	V  int  `json:"v"`
	OK bool `json:"ok"`

	// Version is the daemon's own build, echoed on every answer so a CLI from a
	// different one can say which two versions disagree rather than failing
	// obscurely.
	Version string          `json:"version"`
	Body    json.RawMessage `json:"body,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a refusal, in the same code/message shape freizone-server and
// freizone-gateway both use -- one vocabulary across the family.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// SendRequest is a message for the daemon to deliver.
//
// Title and Text are separate because a notification is read in two glances:
// the line that says what happened, and the detail underneath it. Either may be
// empty, but not both.
type SendRequest struct {
	Title    string `json:"title,omitempty"`
	Text     string `json:"text,omitempty"`
	Severity string `json:"severity,omitempty"`

	// Source is where this came from -- a hostname, a unit name, a job. Carried
	// separately from the text so a later routing rule can read it without
	// parsing prose.
	Source string `json:"source,omitempty"`

	// At is when the thing being reported happened, which is not necessarily
	// when it is delivered. A message that waited out a retry says so rather
	// than looking current.
	At time.Time `json:"at"`

	// Route names which configured route to use. Empty means every route that
	// is configured, which is the ordinary case.
	Route string `json:"route,omitempty"`

	// Wait asks the daemon to answer only once the message has been delivered
	// rather than once it is durably queued.
	Wait bool `json:"wait,omitempty"`
}

// SendResponse reports what became of it.
type SendResponse struct {
	// Queued is how many destinations it was durably enqueued for. One message
	// to a group and two people is three.
	Queued int `json:"queued"`

	// Delivered is set only for a request that asked to wait.
	Delivered int `json:"delivered,omitempty"`

	// Suppressed reports that the rate cap swallowed this message rather than
	// sending it. Not an error -- the cap exists so a storm cannot page
	// somebody into ignoring their phone -- but the caller should be told
	// rather than left believing it went.
	Suppressed bool `json:"suppressed,omitempty"`
}

// StatusResponse is what the daemon says about itself.
type StatusResponse struct {
	Address    string `json:"address"`
	Connected  bool   `json:"connected"`
	Outbox     int    `json:"outbox"`
	RouteGroup string `json:"route_group,omitempty"`
	RoutePeers int    `json:"route_peers,omitempty"`
	Uptime     string `json:"uptime,omitempty"`
}
