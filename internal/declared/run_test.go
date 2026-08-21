package declared

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/behringer24/freizone-bot/internal/action"
)

func requestFor(t *testing.T, urlTemplate string, params ...string) *Request {
	t.Helper()
	var declaredParams string
	if len(params) > 0 {
		var quoted []string
		for _, p := range params {
			quoted = append(quoted, `{"name":"`+p+`"}`)
		}
		declaredParams = `"params":[` + strings.Join(quoted, ",") + `],`
	}
	a := loadOne(t, `[{"name":"x","summary":"s",`+declaredParams+
		`"request":{"url":"`+urlTemplate+`"}}]`)
	return a.Request
}

// A parameter has to be percent-encoded on its way into a URL, or a value
// containing `&` or `/` rewrites the request instead of filling in a blank.
func TestAParameterCannotRewriteTheRequest(t *testing.T) {
	r := requestFor(t, "https://ci.example.org/api?q={{q}}", "q")

	got, err := r.buildURL(map[string]string{"q": "a&admin=1"})
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	if strings.Contains(got, "&admin=1") {
		t.Errorf("the parameter added a query field: %q", got)
	}

	// And the belt beside the braces: whatever a value turns out to contain,
	// the request has to still go to the host the file named. A bot sits inside
	// a network and carries a token; being made to fetch a URL of the caller's
	// choosing is the thing to prevent.
	r2 := requestFor(t, "https://ci.example.org/{{path}}", "path")
	for _, hostile := range []string{
		"..\\..\\evil.example.org",
		"/evil.example.org/x",
	} {
		got, err := r2.buildURL(map[string]string{"path": hostile})
		if err != nil {
			continue // refused outright, which is also fine
		}
		if !strings.HasPrefix(got, "https://ci.example.org/") {
			t.Errorf("%q escaped the declared host: %q", hostile, got)
		}
	}
}

func TestARequestReachesTheEndpointAndComesBack(t *testing.T) {
	var gotPath, gotAuth, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")
		gotHeader = r.Header.Get("X-Thing")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"green"}`))
	}))
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatalf("writing the token: %v", err)
	}

	a := loadOne(t, `[{"name":"health","summary":"s","params":[{"name":"env"}],"request":{
		"url":"`+srv.URL+`/health?env={{env}}",
		"headers":{"X-Thing":"yes"},
		"tokenFile":"`+strings.ReplaceAll(tokenFile, `\`, `\\`)+`"}}]`)

	got, err := a.Request.Do(context.Background(), Client(), map[string]string{"env": "prod"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got != "status=green" {
		t.Errorf("reply: got %q", got)
	}
	if gotPath != "/health?env=prod" {
		t.Errorf("path: got %q", gotPath)
	}
	// Trailing newline trimmed, and the Bearer prefix added -- a token file
	// written by a person or a secret manager almost always ends in one.
	if gotAuth != "Bearer s3cret" {
		t.Errorf("authorization: got %q", gotAuth)
	}
	if gotHeader != "yes" {
		t.Errorf("header: got %q", gotHeader)
	}
}

// A 302 is the other way a request ends up somewhere it was not sent, and this
// one carries a token.
func TestARedirectOffTheHostIsRefused(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("should never be reached"))
	}))
	defer elsewhere.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/x", http.StatusFound)
	}))
	defer srv.Close()

	r := requestFor(t, srv.URL+"/start")
	if _, err := r.Do(context.Background(), Client(), nil); err == nil {
		t.Fatal("a redirect to another host should be refused")
	}
}

func TestAnAnswerLargerThanTheLimitIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("a", MaxBodyBytes+10)))
	}))
	defer srv.Close()

	// Refused rather than truncated at this layer: the point here is not
	// readability but not letting an endpoint decide how much memory this uses.
	if _, err := requestFor(t, srv.URL).Do(context.Background(), Client(), nil); err == nil {
		t.Fatal("an oversized answer should be refused")
	}
}

// Registry.Register panics on a duplicate, which is right for a wiring mistake
// in Go and wrong for a name somebody typed in a file: a daemon should refuse to
// start with a sentence, not crash with a stack trace.
func TestANameClashWithABuiltinIsReportedNotPanicked(t *testing.T) {
	r := action.NewRegistry()
	action.RegisterBuiltins(r, func() string { return "" }, []string{"a joke"})

	actions, err := Load(write(t, `[{"name":"help","summary":"s","reply":"mine"}]`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = Register(r, actions, Client())
	if err == nil {
		t.Fatal("want a failure")
	}
	if !strings.Contains(err.Error(), "built-in") {
		t.Errorf("got %q", err)
	}
}

// The whole way through: a declaration becomes a registered action, and running
// it applies the spec's own parameter rules.
func TestADeclaredActionRunsThroughTheRegistry(t *testing.T) {
	r := action.NewRegistry()
	actions, err := Load(write(t, `[{"name":"greet","summary":"say hello",
		"params":[{"name":"who","required":true,"pattern":"^[a-zA-Z]+$"}],
		"reply":"Hello {{who}}."}]`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := Register(r, actions, Client()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := r.Execute(context.Background(), "greet", map[string]string{"who": "Andreas"}, "qsender", "qchat")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Reply != "Hello Andreas." {
		t.Errorf("reply: got %q", got.Reply)
	}

	// The pattern is enforced by the registry, not by the handler -- which is
	// what makes it hold for a model-driven interpreter too, since that also
	// goes through Execute.
	if _, err := r.Execute(context.Background(), "greet", map[string]string{"who": "1; drop"}, "qsender", "qchat"); err == nil {
		t.Error("a value failing the pattern should not reach the handler")
	}
	if _, err := r.Execute(context.Background(), "greet", nil, "qsender", "qchat"); err == nil {
		t.Error("a missing required parameter should be refused")
	}
}
