package declared

import (
	"fmt"
	"strings"
	"testing"
)

func TestPlainTextIsTheReply(t *testing.T) {
	got, err := render(200, "text/plain; charset=utf-8", []byte("  14 days, 3 hours\n"), "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != "14 days, 3 hours" {
		t.Errorf("got %q", got)
	}
}

// Small internal services routinely send no content type at all, and refusing
// those would make the request kind useless against exactly the sort of endpoint
// an operator is most likely to point it at.
func TestNoContentTypeIsTakenAsText(t *testing.T) {
	got, err := render(200, "", []byte("ok"), "")
	if err != nil || got != "ok" {
		t.Errorf("got %q, %v", got, err)
	}
	// Unless it is plainly not text.
	if _, err := render(200, "", []byte{0x00, 0x01, 0x02}, ""); err == nil {
		t.Error("binary with no content type should not be pasted into a chat")
	}
}

func TestJSONIsDecomposedByShape(t *testing.T) {
	for _, tc := range []struct{ what, body, want string }{
		{"a bare string", `"all good"`, "all good"},
		{"a number", `42`, "42"},
		{"a boolean", `true`, "true"},
		// Sorted, because Go's map order would otherwise reshuffle an unchanged
		// answer on every call -- which is what makes the deduplicator treat two
		// identical things as different.
		{"an object", `{"status":"green","queue":3}`, "queue=3\nstatus=green"},
		{"a list", `["web01","web02"]`, "web01\nweb02"},
		// A list of records reads as a list rather than as a wall: each entry
		// collapses onto one line.
		{"a list of objects", `[{"host":"web01","up":true},{"host":"web02","up":false}]`,
			"host=web01 up=true\nhost=web02 up=false"},
		{"an empty list", `[]`, "(nothing)"},
	} {
		got, err := render(200, "application/json", []byte(tc.body), "")
		if err != nil {
			t.Errorf("%s: %v", tc.what, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.what, got, tc.want)
		}
	}
}

func TestAFieldSelectsPartOfTheAnswer(t *testing.T) {
	body := []byte(`{"meta":{"took":3},"result":{"items":["a","b"]}}`)
	got, err := render(200, "application/json", body, "result.items")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != "a\nb" {
		t.Errorf("got %q", got)
	}

	// A field that is not there is a mistake in the declaration. Answering
	// nothing would hide it behind a command that silently says little.
	if _, err := render(200, "application/json", body, "result.missing"); err == nil {
		t.Error("a missing field should be reported")
	}
	if _, err := render(200, "application/json", body, "meta.took.deeper"); err == nil {
		t.Error("walking into a non-object should be reported")
	}
	// And a field asked of something that is not JSON at all.
	if _, err := render(200, "text/plain", []byte("hello"), "result"); err == nil {
		t.Error("a field against plain text should be reported")
	}
}

func TestAFailureIsAFailureRatherThanAReply(t *testing.T) {
	// A short textual body on a failure is usually the actual reason, and worth
	// having in the chat.
	_, err := render(503, "text/plain", []byte("upstream is down"), "")
	if err == nil || !strings.Contains(err.Error(), "upstream is down") || !strings.Contains(err.Error(), "503") {
		t.Errorf("got %v", err)
	}

	// An HTML error page is the likeliest thing a misconfigured endpoint
	// returns, and pasting one into a group transcript cannot be undone.
	_, err = render(500, "text/html", []byte("<html><body><h1>500 Internal Server Error</h1>"+strings.Repeat("x", 400)+"</body></html>"), "")
	if err == nil {
		t.Fatal("want a failure")
	}
	if strings.Contains(err.Error(), "<html>") || strings.Contains(err.Error(), "xxx") {
		t.Errorf("the page leaked into the message: %q", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("the status should still be named, got %q", err)
	}
}

// Even with a 200. An operator who points an action at a web page rather than an
// API should learn that from one sentence, not from a wall of markup arriving on
// everybody's phone.
func TestHTMLIsDescribedNotPasted(t *testing.T) {
	_, err := render(200, "text/html", []byte("<html><body>hello</body></html>"), "")
	if err == nil {
		t.Fatal("want a failure")
	}
	if strings.Contains(err.Error(), "<html>") {
		t.Errorf("got %q", err)
	}
	if !strings.Contains(err.Error(), "text/html") {
		t.Errorf("the message should name what arrived, got %q", err)
	}
}

func TestALongAnswerIsCutAndSaysSo(t *testing.T) {
	got, err := render(200, "text/plain", []byte(strings.Repeat("a", ReplyLimit*2)), "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(got) > ReplyLimit+64 {
		t.Errorf("still %d characters long", len(got))
	}
	if !strings.Contains(got, "cut short") {
		// Silent truncation reads as a complete answer, which is worse than a
		// short one -- especially for a list somebody is checking against.
		t.Errorf("truncation was not admitted: %q", got[len(got)-40:])
	}
}

func TestALongListIsCutAndCounted(t *testing.T) {
	var items []string
	for i := 0; i < LineLimit+10; i++ {
		items = append(items, `"item"`)
	}
	got, err := render(200, "application/json", []byte("["+strings.Join(items, ",")+"]"), "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := fmt.Sprintf("and %d more", (LineLimit+10)-listCap)
	if !strings.Contains(got, want) {
		t.Errorf("the count of what was dropped is the useful part -- want %q in %q", want, got)
	}
}

// Cutting mid-rune would put invalid UTF-8 into a transcript.
func TestTruncationDoesNotSplitACharacter(t *testing.T) {
	got, err := render(200, "text/plain", []byte(strings.Repeat("ä", ReplyLimit)), "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for i, r := range got {
		if r == '�' {
			t.Fatalf("broken rune at %d", i)
		}
	}
}

func TestAnAnswerCarriesTheActionsName(t *testing.T) {
	// Without it, a group member cannot tell what the bot found from what the
	// bot is saying -- and text that reads in the bot's own voice is the useful
	// half of anything somebody would want to inject through an endpoint.
	if got := frame("deploys", "all green"); got != "deploys: all green" {
		t.Errorf("got %q", got)
	}
	if got := frame("deploys", "a\nb"); !strings.HasPrefix(got, "deploys:\n\n") {
		t.Errorf("a multi-line answer should be introduced on its own line, got %q", got)
	}
	if got := frame("deploys", "   "); !strings.Contains(got, "no answer") {
		t.Errorf("an empty answer should say so, got %q", got)
	}
}
