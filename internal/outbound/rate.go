package outbound

import (
	"fmt"
	"sync"
	"time"
)

// Limiter caps how many messages leave per minute, collapsing the excess into
// one line instead of dropping it silently.
//
// Not an optimisation. Without a cap, one flapping service turns this bot into
// a denial of service on the operator's own phone -- and on their own Freizone
// server, whose per-device queue is bounded and starts refusing once it is
// full, at which point *nobody* can reach them. A pager that cries wolf a
// hundred times is worse than no pager, because it is the one people mute.
//
// The clever version -- dedup keys, pairing a resolved notice with the firing
// one it answers -- is BOT-03. This is the crude one, and it ships first
// because shipping without any is shipping a footgun.
type Limiter struct {
	mu    sync.Mutex
	limit int
	now   func() time.Time

	windowStart time.Time
	sent        int

	// suppressed counts what the cap swallowed in this window, so the next
	// message that gets through can say how much was missed. A cap that hides
	// its own effect is indistinguishable from a bot that has died.
	suppressed int
}

// NewLimiter caps at limit messages per minute. now is injectable so a test
// need not sleep through a window.
func NewLimiter(limit int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{limit: limit, now: now}
}

// Allow reports whether one more message may go, and returns a note to append
// to it naming what the cap swallowed since the last one that got through.
func (l *Limiter) Allow() (allowed bool, note string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if l.windowStart.IsZero() || now.Sub(l.windowStart) >= time.Minute {
		l.windowStart = now
		l.sent = 0
		// Deliberately not clearing suppressed: the count belongs to the next
		// message that gets through, whichever window that lands in. Resetting
		// it here would lose exactly the number worth reporting.
	}

	if l.sent >= l.limit {
		l.suppressed++
		return false, ""
	}

	l.sent++
	if l.suppressed > 0 {
		n := l.suppressed
		l.suppressed = 0
		// Both halves agree, because this line is read under stress and
		// "1 further message were suppressed" makes a reader stop and re-read
		// exactly when they have no attention to spare.
		phrase := fmt.Sprintf("%d further messages were", n)
		if n == 1 {
			phrase = "1 further message was"
		}
		return true, fmt.Sprintf("\n\n(%s suppressed by the rate limit)", phrase)
	}
	return true, ""
}

// Suppressed is how many are currently unreported, for a status answer.
func (l *Limiter) Suppressed() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.suppressed
}
