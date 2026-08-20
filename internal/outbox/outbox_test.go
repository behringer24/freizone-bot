package outbox

import (
	"errors"
	"testing"
	"time"

	"github.com/behringer24/freizone-bot/internal/outbound"
)

func testOutbox(t *testing.T, max int) *Outbox {
	t.Helper()
	o, err := Open(t.TempDir(), max)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return o
}

var dests = []outbound.Destination{
	{Kind: outbound.KindGroup, ID: "qgroup"},
	{Kind: outbound.KindPeer, ID: "qpeer1"},
	{Kind: outbound.KindPeer, ID: "qpeer2"},
}

// The rule the whole package is shaped around: one entry per destination, not
// one per message. A single entry covering three recipients would, on a retry
// after two succeeded, page those two again.
func TestOneEntryPerDestination(t *testing.T) {
	o := testOutbox(t, 100)
	now := time.Now().UTC()

	ids, err := o.Enqueue(outbound.Message{Title: "disk full"}, dests, now)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("want three entries, got %d", len(ids))
	}
	// Distinct ids even though all three were created in the same instant,
	// which a fan-out does every single time.
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate entry id %q -- one would overwrite the other", id)
		}
		seen[id] = true
	}

	due, err := o.Due(now)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if len(due) != 3 {
		t.Fatalf("want three due, got %d", len(due))
	}

	// Delivering one must leave the others exactly where they were.
	if err := o.Done(ids[0]); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if n, _ := o.Len(); n != 2 {
		t.Errorf("want two left, got %d", n)
	}
}

// The acknowledgement `send` gives depends on this: an entry has to survive the
// process that wrote it, or "durably queued" is a claim about a page cache.
func TestEntriesSurviveAReopen(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	first, err := Open(dir, 100)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := first.Enqueue(outbound.Message{Title: "before the restart", Text: "detail"}, dests[:1], now); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	second, err := Open(dir, 100)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	due, err := second.Due(now)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("want the entry back, got %d", len(due))
	}
	if due[0].Message.Title != "before the restart" || due[0].Message.Text != "detail" {
		t.Errorf("the message did not survive intact: %+v", due[0].Message)
	}
	if due[0].Destination != dests[0] {
		t.Errorf("the destination did not survive: %+v", due[0].Destination)
	}
}

// A failed attempt has to wait before the next one, or a dead server is hammered
// as fast as the loop can spin.
func TestAFailedAttemptBacksOff(t *testing.T) {
	o := testOutbox(t, 100)
	now := time.Now().UTC()

	ids, err := o.Enqueue(outbound.Message{Title: "x"}, dests[:1], now)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	dropped, err := o.Fail(ids[0], errors.New("their server could not be reached"), now, time.Hour)
	if err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if dropped {
		t.Fatal("one failure inside the age limit must not drop the entry")
	}

	if due, _ := o.Due(now); len(due) != 0 {
		t.Error("an entry that just failed must not be due again immediately")
	}
	if due, _ := o.Due(now.Add(time.Minute)); len(due) != 1 {
		t.Error("it has to become due again once the backoff has elapsed")
	}

	// And the reason is kept, so a give-up can say what kept failing.
	due, _ := o.Due(now.Add(time.Minute))
	if due[0].LastError == "" {
		t.Error("the failure reason should be recorded")
	}
	if due[0].Attempts != 1 {
		t.Errorf("attempts: want 1, got %d", due[0].Attempts)
	}
}

// Backoff has to have a ceiling: an unreachable server is ordinary in a
// federation, and an ever-doubling wait would turn a brief outage into a message
// arriving an hour after it mattered.
func TestBackoffIsBounded(t *testing.T) {
	if got := backoff(1); got != 5*time.Second {
		t.Errorf("first retry: want 5s, got %v", got)
	}
	if got := backoff(2); got != 10*time.Second {
		t.Errorf("second retry: want 10s, got %v", got)
	}
	if got := backoff(50); got != 5*time.Minute {
		t.Errorf("a long-failing entry must cap at 5m, got %v", got)
	}
}

// An alert delivered six hours late is noise, so an entry is eventually given up
// on -- and the caller is told, because a silent drop is a lie about a channel
// somebody is relying on.
func TestAnEntryIsDroppedOnceItIsTooOld(t *testing.T) {
	o := testOutbox(t, 100)
	now := time.Now().UTC()

	ids, err := o.Enqueue(outbound.Message{Title: "stale"}, dests[:1], now)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	dropped, err := o.Fail(ids[0], errors.New("still unreachable"), now.Add(2*time.Hour), time.Hour)
	if err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if !dropped {
		t.Fatal("an entry past the age limit has to be given up on")
	}
	if n, _ := o.Len(); n != 0 {
		t.Errorf("a dropped entry must be gone, %d left", n)
	}
}

// A full outbox refuses loudly. The caller can then log it or page a second way;
// a silent drop is invisible.
func TestAFullOutboxRefuses(t *testing.T) {
	o := testOutbox(t, 2)
	now := time.Now().UTC()

	if _, err := o.Enqueue(outbound.Message{Title: "one"}, dests[:2], now); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	_, err := o.Enqueue(outbound.Message{Title: "two"}, dests[:1], now)
	if !errors.Is(err, ErrFull) {
		t.Fatalf("want ErrFull, got %v", err)
	}
	// And the message says what the numbers are, since "full" alone tells an
	// operator nothing about what to raise.
	if err != nil && !containsDigit(err.Error()) {
		t.Errorf("the refusal should quantify itself, got %q", err)
	}
}

// An id from anywhere is still an id used to build a path.
func TestAnUnsafeIdIsRefused(t *testing.T) {
	o := testOutbox(t, 10)
	for _, id := range []string{"", "../escape", `sub\dir`, "a/b"} {
		if err := o.Done(id); err == nil {
			t.Errorf("id %q should have been refused", id)
		}
	}
}

func containsDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}
