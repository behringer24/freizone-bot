package webhook

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const goodToken = "a-token-long-enough-to-be-accepted"

// spy records what reached the daemon, which is the only thing worth asserting:
// the handler's whole job is to turn one request into one Accept call.
type spy struct {
	calls  int
	title  string
	text   string
	route  string
	dedup  string
	labels map[string]string

	queued       int
	suppressedBy string
	err          error
}

func (s *spy) Accept(title, text, route, dedupKey string, labels map[string]string) (int, string, error) {
	s.calls++
	s.title, s.text, s.route, s.dedup, s.labels = title, text, route, dedupKey, labels
	if s.err != nil {
		return 0, "", s.err
	}
	if s.queued == 0 && s.suppressedBy == "" {
		s.queued = 1
	}
	return s.queued, s.suppressedBy, nil
}

func post(t *testing.T, h *Handler, target, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func handler(s *spy) *Handler {
	return New(s, map[string]string{goodToken: "ci"}, slog.New(slog.DiscardHandler))
}

// The whole contract, in one test: the body is the text, the query is the
// title, and one request is one message.
func TestOnePostIsOneMessage(t *testing.T) {
	s := &spy{}
	rec := post(t, handler(s), Path+"?title=Backup+failed", "/srv was full", goodToken)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if s.calls != 1 {
		t.Errorf("Accept called %d times, want exactly 1", s.calls)
	}
	if s.title != "Backup failed" {
		t.Errorf("title: got %q", s.title)
	}
	if s.text != "/srv was full" {
		t.Errorf("text: got %q", s.text)
	}
}

// The body is never parsed. A JSON body arrives as the text it is -- which is
// the point: this bot knows nobody's payload format, so a sender that batches
// several events into one request produces one message, not several.
func TestAJSONBodyIsJustText(t *testing.T) {
	s := &spy{}
	body := `{"alerts":[{"labels":{"alertname":"a"}},{"labels":{"alertname":"b"}}]}`
	rec := post(t, handler(s), Path+"?title=two+things", body, goodToken)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d", rec.Code)
	}
	if s.calls != 1 {
		t.Errorf("two events in one request must still be one message, got %d calls", s.calls)
	}
	if s.text != body {
		t.Errorf("the body was altered: %q", s.text)
	}
}

func TestLabelsRouteAndDedupComeFromTheQuery(t *testing.T) {
	s := &spy{}
	rec := post(t, handler(s),
		Path+"?title=t&label=host:web01&label=kind:ci&route=peers&dedup=disk-web01", "", goodToken)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if s.labels["host"] != "web01" || s.labels["kind"] != "ci" {
		t.Errorf("labels: got %v", s.labels)
	}
	if s.route != "peers" || s.dedup != "disk-web01" {
		t.Errorf("route %q, dedup %q", s.route, s.dedup)
	}
}

func TestBadLabels(t *testing.T) {
	for _, tc := range []struct{ what, query, want string }{
		{"no colon", "&label=host", "key:value"},
		{"no value", "&label=host:", "key:value"},
		{"no key", "&label=:web01", "key:value"},
		// Refused rather than last-wins: a repeated key means the sender meant
		// two things, and picking one silently is guessing.
		{"the same key twice", "&label=host:a&label=host:b", "set twice"},
	} {
		s := &spy{}
		rec := post(t, handler(s), Path+"?title=t"+tc.query, "body", goodToken)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", tc.what, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: body %q, want it to mention %q", tc.what, rec.Body, tc.want)
		}
		if s.calls != 0 {
			t.Errorf("%s: nothing should have been accepted", tc.what)
		}
	}
}

func TestTooManyLabels(t *testing.T) {
	var query strings.Builder
	for i := 0; i <= MaxLabels; i++ {
		fmt.Fprintf(&query, "&label=k%d:v", i)
	}
	rec := post(t, handler(&spy{}), Path+"?title=t"+query.String(), "body", goodToken)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
}

func TestAuthentication(t *testing.T) {
	for _, tc := range []struct {
		what, token string
	}{
		{"no token", ""},
		{"a wrong token", "not-the-token-but-long-enough-x"},
		// A prefix of a valid token must not pass -- which a comparison that
		// stopped at the first difference in length would allow.
		{"a prefix of the real one", goodToken[:10]},
	} {
		s := &spy{}
		rec := post(t, handler(s), Path+"?title=t", "body", tc.token)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", tc.what, rec.Code)
		}
		if s.calls != 0 {
			t.Errorf("%s: nothing should have reached the daemon", tc.what)
		}
		// A challenge, but no hint about which part was wrong: distinguishing
		// "no token" from "wrong token" is an oracle for guessing.
		if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
			t.Errorf("%s: WWW-Authenticate %q", tc.what, got)
		}
	}
}

func TestOnlyPostAndOnlyThePath(t *testing.T) {
	h := handler(&spy{})

	req := httptest.NewRequest(http.MethodGet, Path, nil)
	req.Header.Set("Authorization", "Bearer "+goodToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow: %q", got)
	}

	// The path is checked before the token, so a wrong path does not tell an
	// unauthenticated caller anything about the token either way.
	rec = post(t, h, "/something-else", "body", goodToken)
	if rec.Code != http.StatusNotFound {
		t.Errorf("wrong path: status %d, want 404", rec.Code)
	}
}

func TestAnEmptyRequestIsRefused(t *testing.T) {
	// Not silently accepted: a sender posting nothing at all is broken, and a
	// 202 would hide that for as long as it lasted.
	s := &spy{}
	rec := post(t, handler(s), Path, "   ", goodToken)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
	if s.calls != 0 {
		t.Error("nothing should have been accepted")
	}
}

func TestAnOversizedBodyIsRefused(t *testing.T) {
	s := &spy{}
	rec := post(t, handler(s), Path+"?title=t", strings.Repeat("a", MaxBodyBytes+1), goodToken)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d, want 413", rec.Code)
	}
	if s.calls != 0 {
		t.Error("nothing should have been accepted")
	}
}

// A body that is merely long is accepted, and shortened further down by
// outbound's one answer to how long a chat message may be. The two limits are
// different questions: this one is about memory, that one about a phone screen.
func TestALongBodyIsAcceptedAndPassedOn(t *testing.T) {
	s := &spy{}
	rec := post(t, handler(s), Path+"?title=t", strings.Repeat("a", 64<<10), goodToken)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(s.text) != 64<<10 {
		t.Errorf("the handler shortened the text itself: %d bytes", len(s.text))
	}
}

func TestHowFailuresAreAnswered(t *testing.T) {
	for _, tc := range []struct {
		what   string
		err    error
		status int
	}{
		{"no route", fmt.Errorf("%w: none configured", ErrNoRoute), http.StatusServiceUnavailable},
		{"queue full", fmt.Errorf("%w: 1000 waiting", ErrFull), http.StatusServiceUnavailable},
		{"something else", errors.New("the disk is on fire"), http.StatusInternalServerError},
	} {
		rec := post(t, handler(&spy{err: tc.err}), Path+"?title=t", "body", goodToken)
		if rec.Code != tc.status {
			t.Errorf("%s: status %d, want %d", tc.what, rec.Code, tc.status)
		}
		// Past the caller's control, the reason is logged rather than returned:
		// describing this bot's internal state to whoever can POST tells them
		// more about the inside than they need.
		if tc.status == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "disk is on fire") {
			t.Errorf("%s: the internal error leaked: %q", tc.what, rec.Body)
		}
	}

	// Retry-After only on the one failure that is expected to pass.
	rec := post(t, handler(&spy{err: fmt.Errorf("%w: full", ErrFull)}), Path+"?title=t", "b", goodToken)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a full queue should say when to come back")
	}
}

func TestSuppressionIsReportedNotHidden(t *testing.T) {
	// Accepted, since the request was fine -- but the answer says what happened,
	// because a cap that silently swallows is indistinguishable from a bot that
	// has died.
	rec := post(t, handler(&spy{suppressedBy: "duplicate"}), Path+"?title=t", "b", goodToken)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "duplicate") {
		t.Errorf("body %q should say it was suppressed", rec.Body)
	}
}

func TestTheAnswerIsPlainText(t *testing.T) {
	rec := post(t, handler(&spy{}), Path+"?title=t", "b", goodToken)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type %q", ct)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options %q", got)
	}
	if _, err := io.ReadAll(rec.Body); err != nil {
		t.Errorf("reading the body: %v", err)
	}
}
