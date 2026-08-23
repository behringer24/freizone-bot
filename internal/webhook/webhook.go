// Package webhook is the bot's HTTP ingress: one POST in, one message out.
//
// # The whole contract
//
// The body is the message text. The title and the labels come from the query
// string. That is all.
//
//	POST /hook?title=Backup+failed&label=host:web01
//	Authorization: Bearer <token>
//
//	/srv was full
//
// # What it deliberately does not do
//
// It does not parse the body, and it knows no sender's format -- not
// Alertmanager's, not Grafana's, not anybody's. That is a decision about what
// this bot *is*: a general bridge between Freizone and other systems, where
// operations alerting is one use case among a build result, a scheduled digest,
// a sensor reading and a chat companion's answer. A named adapter for one
// monitoring tool's payload would make that tool's vocabulary the centre of
// gravity, and everything else would have to bend into it.
//
// The direct consequence, and it is a feature: **one request is one message.**
// No grouping, no fan-out, no pairing a "resolved" with an earlier "firing".
// All of that complexity came from a single sender's habit of batching several
// events into one POST -- and reading a body is the only way to know it did.
// Whoever wants a batch reshaped puts something in front of this.
//
// The title comes from the query rather than a header because the webhook URL is
// the one thing every sender lets you configure, while plenty of them will not
// let you add headers.
package webhook

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Path is where the handler listens. One path, since there is one thing to do.
const Path = "/hook"

// MaxBodyBytes is the largest request accepted at all. Generous, because a
// caller piping a log tail in is doing something reasonable; bounded, because
// otherwise a misconfigured sender decides how much memory this process uses.
const MaxBodyBytes = 1 << 20

// MaxLabels bounds how many labels one request may set. Labels are rendered
// into the message and used as a deduplication key, so an unbounded number is
// both an unreadable message and a way to defeat deduplication.
const MaxLabels = 20

// Accepter takes a message in. Implemented by the daemon, which owns routing,
// deduplication, the rate cap and the outbox -- none of which this package
// should have an opinion about, and all of which the control socket already
// goes through. One path in, however many producers.
type Accepter interface {
	Accept(title, text, route, dedupKey string, labels map[string]string) (queued int, suppressedBy string, err error)
}

// Handler serves the ingress.
type Handler struct {
	accepter Accepter
	tokens   map[string]string // token -> sender name
	logger   *slog.Logger
}

// ErrNoRoute is what an Accepter returns when a message has nowhere to go. The
// handler answers 503 for it: the request was fine, this bot is not configured
// to carry it.
var ErrNoRoute = errors.New("no route")

// ErrFull is what an Accepter returns when the queue is full. 503 as well, and
// a Retry-After, since it is the one failure here that is expected to pass.
var ErrFull = errors.New("queue full")

// New builds the handler. tokens maps a token to the name of the sender holding
// it -- one per sender rather than one shared, so a single sender can be
// switched off without switching off the rest, and so a log line can say which
// one sent something.
func New(accepter Accepter, tokens map[string]string, logger *slog.Logger) *Handler {
	return &Handler{accepter: accepter, tokens: tokens, logger: logger}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != Path {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		refuse(w, http.StatusMethodNotAllowed, "this endpoint takes a POST")
		return
	}

	sender, ok := h.authenticate(r)
	if !ok {
		// The realm is deliberately vague: an error that distinguished "no
		// token" from "wrong token" would be an oracle for guessing.
		w.Header().Set("WWW-Authenticate", `Bearer realm="freizone-bot"`)
		refuse(w, http.StatusUnauthorized, "a valid bearer token is required")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
	if err != nil {
		refuse(w, http.StatusBadRequest, "the request body could not be read")
		return
	}
	if len(body) > MaxBodyBytes {
		refuse(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("the body is larger than %d bytes", MaxBodyBytes))
		return
	}

	query := r.URL.Query()
	title := strings.TrimSpace(query.Get("title"))
	text := strings.TrimSpace(string(body))
	if title == "" && text == "" {
		// Not silently accepted: a sender posting nothing is a sender that is
		// broken, and answering 202 would hide it for as long as it lasted.
		refuse(w, http.StatusBadRequest, "there is nothing to send: no title and an empty body")
		return
	}

	labels, err := labelsFrom(query["label"])
	if err != nil {
		refuse(w, http.StatusBadRequest, err.Error())
		return
	}

	queued, suppressedBy, err := h.accepter.Accept(
		title, text, strings.TrimSpace(query.Get("route")), strings.TrimSpace(query.Get("dedup")), labels)
	switch {
	case errors.Is(err, ErrNoRoute):
		h.logger.Warn("webhook message had nowhere to go", "sender", sender, "error", err)
		refuse(w, http.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, ErrFull):
		w.Header().Set("Retry-After", "30")
		h.logger.Warn("webhook message refused, queue full", "sender", sender)
		refuse(w, http.StatusServiceUnavailable, err.Error())
		return
	case err != nil:
		// The reason is logged, not returned: past this point a failure is
		// about this bot's own state, and describing it to whoever can POST
		// tells them more about the inside than they need.
		h.logger.Error("webhook message could not be queued", "sender", sender, "error", err)
		refuse(w, http.StatusInternalServerError, "the message could not be queued")
		return
	}

	// The title is logged and the body is not, the same rule the rest of the bot
	// follows: a body lands permanently in every recipient's transcript already,
	// and routinely carries things that should not also sit in a log file.
	if suppressedBy != "" {
		h.logger.Info("webhook message suppressed", "sender", sender, "by", suppressedBy, "title", title)
		answer(w, http.StatusAccepted, "suppressed: "+suppressedBy)
		return
	}
	h.logger.Info("webhook message queued", "sender", sender, "destinations", queued, "title", title)
	answer(w, http.StatusAccepted, fmt.Sprintf("queued for %d destination(s)", queued))
}

// authenticate returns the sender's name for a valid token.
func (h *Handler) authenticate(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	presented, found := strings.CutPrefix(header, "Bearer ")
	if !found {
		presented, found = strings.CutPrefix(header, "bearer ")
	}
	presented = strings.TrimSpace(presented)
	if !found || presented == "" {
		return "", false
	}

	// Every configured token is compared, and all of them are compared even
	// after a match, so the time this takes does not depend on which token was
	// presented or whether one matched at all.
	name, matched := "", false
	for token, sender := range h.tokens {
		if subtle.ConstantTimeCompare([]byte(token), []byte(presented)) == 1 {
			name, matched = sender, true
		}
	}
	return name, matched
}

// labelsFrom reads repeated `label=key:value` query parameters.
//
// `key:value` rather than `key=value`, because a query parameter is already
// `label=<something>` and nesting a second equals sign inside it is the kind of
// thing that works until somebody's shell does not quote it.
func labelsFrom(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > MaxLabels {
		return nil, fmt.Errorf("at most %d labels, got %d", MaxLabels, len(raw))
	}

	out := make(map[string]string, len(raw))
	for _, entry := range raw {
		key, value, ok := strings.Cut(entry, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("label %q is not in the form key:value", entry)
		}
		if _, dup := out[key]; dup {
			// Refused rather than last-wins: a repeated key means the sender
			// meant two things, and picking one silently is guessing.
			return nil, fmt.Errorf("the label %q is set twice", key)
		}
		out[key] = value
	}
	return out, nil
}

func refuse(w http.ResponseWriter, status int, reason string) {
	answer(w, status, reason)
}

// answer writes plain text. No JSON: nothing here needs a machine-readable
// shape, and a status code plus a sentence is what a person reads out of a
// sender's own error log.
func answer(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text+"\n")
}

// Server wraps the handler with the timeouts a listener facing anything at all
// needs. Separate from New so a test can exercise the handler without a socket.
func Server(h *Handler, addr string) *http.Server {
	return &http.Server{
		Addr:    addr,
		Handler: h,
		// A slow or stalled client must not hold a connection open forever:
		// this process has work to do that has nothing to do with HTTP.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}
