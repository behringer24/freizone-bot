package declared

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "actions.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}
	return path
}

func loadOne(t *testing.T, content string) Action {
	t.Helper()
	actions, err := Load(write(t, content))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}
	return actions[0]
}

func TestNoFileIsNotAnError(t *testing.T) {
	// Most bots declare nothing, and an unset setting is not a mistake.
	got, err := Load("")
	if err != nil || got != nil {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestAFixedReply(t *testing.T) {
	a := loadOne(t, `[{"name":"oncall","summary":"who has the pager","reply":"Andreas this week."}]`)
	if a.Request != nil {
		t.Error("a reply action should carry no request")
	}
	if a.spec().Sensitive {
		// A fixed reply changes nothing, so it is not the thing the Sensitive
		// flag exists to mark. Getting this wrong would make every canned
		// answer need a confirmation.
		t.Error("a fixed reply should not be marked sensitive")
	}
}

func TestARequestIsSensitive(t *testing.T) {
	a := loadOne(t, `[{"name":"deploys","summary":"recent deploys","request":{"url":"https://ci.example.org/api"}}]`)
	if !a.spec().Sensitive {
		t.Error("a request reaches out of this machine and should be marked sensitive")
	}
	if a.Request.Method != "GET" || a.Request.TokenHeader != "Authorization" {
		t.Errorf("defaults not applied: %+v", a.Request)
	}
	if a.Request.timeout() != DefaultTimeout {
		t.Errorf("timeout: got %v", a.Request.timeout())
	}
}

func TestWhatAFileCannotSay(t *testing.T) {
	for _, tc := range []struct {
		what, content, want string
	}{
		{
			"an unknown field",
			`[{"name":"x","summary":"s","reply":"r","patern":"^a$"}]`,
			"unknown field",
		},
		{
			"nothing to do",
			`[{"name":"x","summary":"s"}]`,
			"does nothing",
		},
		{
			"both kinds at once",
			`[{"name":"x","summary":"s","reply":"r","request":{"url":"https://e.org"}}]`,
			"pick one",
		},
		{
			"no summary",
			`[{"name":"x","reply":"r"}]`,
			"needs a summary",
		},
		{
			"an unusable name",
			`[{"name":"My Action","summary":"s","reply":"r"}]`,
			"not a usable action name",
		},
		{
			"the same name twice",
			`[{"name":"x","summary":"s","reply":"a"},{"name":"x","summary":"s","reply":"b"}]`,
			"declared twice",
		},
		{
			// Would otherwise reach the reply, or the URL, as the literal text
			// `{{whoo}}` -- visible in a chat and confusing in a request.
			"a placeholder naming no parameter",
			`[{"name":"x","summary":"s","params":[{"name":"who"}],"reply":"hi {{whoo}}"}]`,
			"not one of its parameters",
		},
		{
			"a pattern that is not a pattern",
			`[{"name":"x","summary":"s","params":[{"name":"a","pattern":"[unclosed"}],"reply":"{{a}}"}]`,
			"error parsing regexp",
		},
		{
			"a bare path where a URL belongs",
			`[{"name":"x","summary":"s","request":{"url":"/api/things"}}]`,
			"needs a whole URL",
		},
		{
			"a scheme with nothing behind it",
			`[{"name":"x","summary":"s","request":{"url":"https://"}}]`,
			"names no host",
		},
		{
			// Not a scheme this can send, and worth refusing loudly: file:// on
			// a bot that holds keys would be a way to read them out.
			"a scheme that is not http",
			`[{"name":"x","summary":"s","request":{"url":"file:///etc/passwd"}}]`,
			"is not http or https",
		},
		{
			"a method this does not send",
			`[{"name":"x","summary":"s","request":{"method":"TRACE","url":"https://e.org"}}]`,
			"is not a method",
		},
		{
			"a timeout somebody would wait through",
			`[{"name":"x","summary":"s","request":{"url":"https://e.org","timeoutSeconds":600}}]`,
			"ceiling",
		},
	} {
		_, err := Load(write(t, tc.content))
		if err == nil {
			t.Errorf("%s: should have been refused", tc.what)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %q, want it to mention %q", tc.what, err, tc.want)
		}
	}
}

// An unanchored pattern matches a substring, so `[0-9]+` would accept
// "12; rm -rf /" -- which is not what anybody writing a pattern means.
func TestAPatternIsAnchored(t *testing.T) {
	a := loadOne(t, `[{"name":"x","summary":"s","params":[{"name":"n","pattern":"[0-9]+"}],"reply":"{{n}}"}]`)
	validate := a.Params[0].validator()
	if validate == nil {
		t.Fatal("the pattern was not compiled")
	}
	if err := validate("12"); err != nil {
		t.Errorf("a matching value: %v", err)
	}
	if err := validate("12 and more"); err == nil {
		t.Error("an unanchored pattern must not accept a value with extra text around it")
	}

	// An already-anchored pattern is left as written.
	b := loadOne(t, `[{"name":"y","summary":"s","params":[{"name":"n","pattern":"^[0-9]+$"}],"reply":"{{n}}"}]`)
	if err := b.Params[0].validator()("12 and more"); err == nil {
		t.Error("an anchored pattern should still refuse")
	}
}

// The error a person sees must not quote the pattern back at them: it is a
// regular expression, they are in a chat window, and it would amount to advice
// on how to construct something that passes.
func TestAPatternIsNotQuotedBackToTheSender(t *testing.T) {
	a := loadOne(t, `[{"name":"x","summary":"s","params":[{"name":"host","pattern":"^web[0-9]+$"}],"reply":"{{host}}"}]`)
	err := a.Params[0].validator()("nonsense")
	if err == nil {
		t.Fatal("want a failure")
	}
	if strings.Contains(err.Error(), "web[0-9]") {
		t.Errorf("the pattern leaked into the reply: %q", err)
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("the message should at least name the parameter, got %q", err)
	}
}

func TestPlaceholdersFillIn(t *testing.T) {
	if got := fill("hello {{who}}, welcome", map[string]string{"who": "Andreas"}, nil); got != "hello Andreas, welcome" {
		t.Errorf("got %q", got)
	}
	// An absent optional parameter leaves a blank rather than the literal
	// placeholder, which is the readable half of the two.
	if got := fill("hello {{who}}", nil, nil); got != "hello " {
		t.Errorf("got %q", got)
	}
}
