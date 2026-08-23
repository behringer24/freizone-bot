package webhook

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func tokenFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tokens")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}
	return path
}

func TestNoFileIsNotAnError(t *testing.T) {
	// Most bots have no ingress, and an unset setting is not a mistake.
	got, err := LoadTokens("")
	if err != nil || got != nil {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestTokensAreReadWithCommentsIgnored(t *testing.T) {
	got, err := LoadTokens(tokenFile(t, `
# the CI runner
ci: `+goodToken+`

monitoring:another-token-that-is-long-enough
`))
	if err != nil {
		t.Fatalf("LoadTokens: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tokens, want 2", len(got))
	}
	// Keyed by token, valued by sender: the lookup happens per request, and the
	// name is what a log line needs.
	if got[goodToken] != "ci" {
		t.Errorf("got %v", got)
	}
}

func TestWhatATokenFileCannotSay(t *testing.T) {
	for _, tc := range []struct{ what, content, want string }{
		{"no colon", "ci " + goodToken, "name:token"},
		{"no token", "ci:", "name:token"},
		{"no name", ":" + goodToken, "name:token"},
		// A short token is guessable at a rate nothing here limits, and startup
		// is the only moment anybody is paying attention.
		{"a short token", "ci:short", "shorter than"},
		{"the same name twice", "ci:" + goodToken + "\nci:another-token-long-enough-xx", "appears twice"},
		// Two senders sharing a token defeats the point of naming them: a log
		// line would attribute a request to whichever the map happened to yield.
		{"a shared token", "ci:" + goodToken + "\nmonitoring:" + goodToken, "same token"},
		// Fail closed *and say so*: an empty file with a listener configured is
		// a listener nobody can use, which is safe but is not what was meant.
		{"nothing at all", "# only a comment\n", "names no senders"},
	} {
		_, err := LoadTokens(tokenFile(t, tc.content))
		if err == nil {
			t.Errorf("%s: should have been refused", tc.what)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %q, want it to mention %q", tc.what, err, tc.want)
		}
	}
}

// The token itself must never appear in an error, since an error goes to a log
// that is read by more people than the credential is meant for.
func TestATokenIsNeverQuotedBack(t *testing.T) {
	_, err := LoadTokens(tokenFile(t, "ci:short-secret"))
	if err == nil {
		t.Fatal("want a failure")
	}
	if strings.Contains(err.Error(), "short-secret") {
		t.Errorf("the token leaked into the error: %q", err)
	}
	if !strings.Contains(err.Error(), "ci") {
		t.Errorf("the sender's name should still be named, got %q", err)
	}
}

func TestAWorldReadableFileIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The mode bits here are synthesised, so this check has nothing to read.
		t.Skip("no meaningful file mode on Windows")
	}
	path := tokenFile(t, "ci:"+goodToken)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	_, err := LoadTokens(path)
	if err == nil {
		t.Fatal("a credential file anybody can read is a credential anybody has")
	}
	if !strings.Contains(err.Error(), "chmod") {
		t.Errorf("the error should say what to do, got %q", err)
	}
}
