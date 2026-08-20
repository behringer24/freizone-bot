package outbound

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Deduper collapses repeats of the same message inside a window.
//
// Distinct from the rate cap, which counts *everything* leaving per minute
// regardless of content. This counts one thing repeating: a check that flaps
// every thirty seconds produces one page and then a count, instead of a page
// every thirty seconds.
//
// Off by default, and that is deliberate rather than timid. Deciding two alerts
// are "the same" is a judgement, and the monitoring system upstream usually
// knows better -- it has the labels, the history and the operator's own rules.
// This exists for the case where it does not, or where the thing generating
// alerts is a shell script with none of that.
//
// # What counts as the same message
//
// An explicit key when the caller gives one, because only the caller knows
// whether two alerts are the same incident. Otherwise severity, title and
// source -- and deliberately **not the body**, which routinely carries a
// timestamp, a load average or a line number that differs every time while the
// alert is plainly the same one. Hashing the body would make this feature do
// nothing at all in exactly the cases it exists for.
type Deduper struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time

	seen map[string]*repeat
}

type repeat struct {
	first      time.Time
	lastPassed time.Time
	suppressed int
}

// NewDeduper collapses repeats within window. A zero window disables it, in
// which case Allow always says yes.
func NewDeduper(window time.Duration, now func() time.Time) *Deduper {
	if now == nil {
		now = time.Now
	}
	return &Deduper{window: window, now: now, seen: map[string]*repeat{}}
}

// Enabled reports whether this deduper does anything.
func (d *Deduper) Enabled() bool { return d.window > 0 }

// Allow reports whether a message may go, and returns a note to append when
// repeats were swallowed since the last one that did.
func (d *Deduper) Allow(m Message, key string) (allowed bool, note string) {
	if !d.Enabled() {
		return true, ""
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()
	d.evictLocked(now)

	k := DedupKey(m, key)
	r, ok := d.seen[k]
	if !ok {
		d.seen[k] = &repeat{first: now, lastPassed: now}
		return true, ""
	}

	if now.Sub(r.lastPassed) < d.window {
		r.suppressed++
		return false, ""
	}

	// The window has passed, so this one goes -- carrying what was swallowed
	// while it was closed. A count that is never reported is indistinguishable
	// from a bot that stopped working.
	n := r.suppressed
	r.suppressed = 0
	r.lastPassed = now
	if n == 0 {
		return true, ""
	}
	phrase := fmt.Sprintf("%d identical messages were", n)
	if n == 1 {
		phrase = "1 identical message was"
	}
	return true, fmt.Sprintf("\n\n(%s suppressed since %s)",
		phrase, r.first.UTC().Format("15:04:05 UTC"))
}

// Suppressed is how many repeats are currently unreported, across all keys.
func (d *Deduper) Suppressed() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	for _, r := range d.seen {
		n += r.suppressed
	}
	return n
}

// evictLocked forgets keys that have been quiet for well past the window, so a
// long-running daemon seeing many distinct alerts does not grow a map forever.
//
// Deliberately generous -- ten windows -- because forgetting a key early costs
// a duplicate page, which is the failure worth avoiding here. An entry still
// holding an unreported count is never evicted: that number is owed to
// somebody.
func (d *Deduper) evictLocked(now time.Time) {
	for k, r := range d.seen {
		if r.suppressed == 0 && now.Sub(r.lastPassed) > 10*d.window {
			delete(d.seen, k)
		}
	}
}

// DedupKey is what makes two messages "the same" here. Exported so a test and a
// caller cannot disagree about it.
func DedupKey(m Message, explicit string) string {
	if explicit != "" {
		return "k:" + explicit
	}
	// Hashed rather than concatenated so an unbounded title cannot become an
	// unbounded map key.
	sum := sha256.Sum256([]byte(strings.ToLower(m.Severity) + "\x00" + m.Title + "\x00" + m.Source))
	return "h:" + hex.EncodeToString(sum[:8])
}
