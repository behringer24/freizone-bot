// Package outbox holds messages that have been accepted but not yet delivered.
//
// It exists because of what `send` promises. The call returns once a message is
// *durably* here, not once it is delivered -- a cron job wants a fast exit code,
// but `exit 0` must never mean "accepted into a buffer a restart discards".
// Delivery is asynchronous and retried from here.
//
// # One entry per message and destination
//
// Not one per message. An entry covering five recipients would, on a retry after
// three succeeded, page those three again -- and a pager that repeats itself is
// one people learn to ignore. This is the rule pkg/client's group fan-out
// already arrived at (a ratchet advance is not rolled back in a fan-out, because
// partial success means some peers moved on), applied one layer up where the
// fan-out crosses independent conversations.
package outbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/behringer24/freizone-bot/internal/outbound"
)

// ErrFull reports an outbox at its bound. Returned rather than swallowed: a
// loud rejection leaves the caller able to do something -- log it, page a second
// way -- where a silent drop is invisible.
var ErrFull = errors.New("the outbox is full")

// Entry is one message owed to one destination.
type Entry struct {
	ID          string               `json:"id"`
	Message     outbound.Message     `json:"message"`
	Destination outbound.Destination `json:"destination"`

	Attempts    int       `json:"attempts"`
	FirstSeen   time.Time `json:"first_seen"`
	NextAttempt time.Time `json:"next_attempt"`

	// LastError is kept so a give-up can say what kept failing, rather than
	// only that something did.
	LastError string `json:"last_error,omitempty"`
}

// Outbox is a directory of pending entries.
//
// One file per entry, written to a temporary name and renamed into place --
// the same crash-safety pattern pkg/client's store uses, for the same reason: a
// reader sees the old file or the new one, never a half-written one.
type Outbox struct {
	mu  sync.Mutex
	dir string
	max int

	// seq disambiguates entries created in the same nanosecond, which a fan-out
	// across three destinations does every time.
	seq uint64

	// notify wakes the delivery loop. Depth one: it is a nudge, not a queue --
	// whoever wakes up reads the directory and finds everything.
	notify chan struct{}
}

// Open prepares the outbox directory. Entries left by a previous run stay where
// they are and are retried, which is the whole point of it being on disk.
func Open(dir string, max int) (*Outbox, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the outbox directory: %w", err)
	}
	return &Outbox{dir: dir, max: max, notify: make(chan struct{}, 1)}, nil
}

// Notify is closed-over by the delivery loop to know when something new arrived.
func (o *Outbox) Notify() <-chan struct{} { return o.notify }

// Enqueue writes one entry per destination and returns their ids.
//
// Every write is synced before this returns, because the caller is about to be
// told the message is safe.
func (o *Outbox) Enqueue(msg outbound.Message, dests []outbound.Destination, now time.Time) ([]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	existing, err := o.listLocked()
	if err != nil {
		return nil, err
	}
	if len(existing)+len(dests) > o.max {
		return nil, fmt.Errorf("%w (%d held, %d more asked for, limit %d)",
			ErrFull, len(existing), len(dests), o.max)
	}

	ids := make([]string, 0, len(dests))
	for _, d := range dests {
		o.seq++
		e := Entry{
			ID:          fmt.Sprintf("%d-%04d", now.UTC().UnixNano(), o.seq),
			Message:     msg,
			Destination: d,
			FirstSeen:   now,
			NextAttempt: now,
		}
		if err := o.writeLocked(e); err != nil {
			return ids, err
		}
		ids = append(ids, e.ID)
	}

	// After the writes, so a woken reader cannot look before the files exist.
	select {
	case o.notify <- struct{}{}:
	default:
	}
	return ids, nil
}

// Due returns the entries whose next attempt has come, oldest first so a
// backlog drains in the order it arrived.
func (o *Outbox) Due(now time.Time) ([]Entry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	all, err := o.listLocked()
	if err != nil {
		return nil, err
	}
	var due []Entry
	for _, e := range all {
		if !e.NextAttempt.After(now) {
			due = append(due, e)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].FirstSeen.Before(due[j].FirstSeen) })
	return due, nil
}

// Len is how many entries are waiting.
func (o *Outbox) Len() (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	all, err := o.listLocked()
	return len(all), err
}

// Done removes a delivered entry.
func (o *Outbox) Done(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	path, err := o.pathFor(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing a delivered entry: %w", err)
	}
	return nil
}

// Fail records an attempt that did not work and schedules the next one. The
// bool reports that the entry has been given up on and removed.
//
// Backoff doubles from five seconds to five minutes, and the whole entry is
// dropped once it is older than maxAge. A message delivered six hours late is
// noise; the drop is the caller's to log, loudly, because a silent one would be
// a lie about a channel somebody is relying on.
func (o *Outbox) Fail(id string, cause error, now time.Time, maxAge time.Duration) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	path, err := o.pathFor(id)
	if err != nil {
		return false, err
	}
	e, err := readEntry(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil // already gone; nothing to schedule
		}
		return false, err
	}

	e.Attempts++
	if cause != nil {
		e.LastError = cause.Error()
	}

	if now.Sub(e.FirstSeen) >= maxAge {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("dropping an expired entry: %w", err)
		}
		return true, nil
	}

	e.NextAttempt = now.Add(backoff(e.Attempts))
	return false, o.writeLocked(e)
}

// backoff is 5s doubling to a 5-minute ceiling. Bounded because an unreachable
// server is the ordinary case in a federation, and an ever-growing wait would
// turn a brief outage into a message that arrives an hour after it mattered.
func backoff(attempts int) time.Duration {
	d := 5 * time.Second
	for range attempts - 1 {
		d *= 2
		if d >= 5*time.Minute {
			return 5 * time.Minute
		}
	}
	return d
}

func (o *Outbox) pathFor(id string) (string, error) {
	// The id reaches here from a delivery loop rather than from the wire, but a
	// store that trusts an id is a store that can be told to write anywhere.
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return "", fmt.Errorf("refusing unsafe outbox id %q", id)
	}
	return filepath.Join(o.dir, id+".json"), nil
}

func (o *Outbox) writeLocked(e Entry) error {
	path, err := o.pathFor(e.ID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encoding an outbox entry: %w", err)
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("writing an outbox entry: %w", err)
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		f.Close() //nolint:errcheck // the write error is the useful one
		return fmt.Errorf("writing an outbox entry: %w", err)
	}
	// Synced before the rename, because the caller is about to be told this
	// message is safe. Without it, "durably queued" is a claim about a page
	// cache.
	if err := f.Sync(); err != nil {
		f.Close() //nolint:errcheck
		return fmt.Errorf("syncing an outbox entry: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing an outbox entry: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("moving an outbox entry into place: %w", err)
	}
	return nil
}

func (o *Outbox) listLocked() ([]Entry, error) {
	names, err := os.ReadDir(o.dir)
	if err != nil {
		return nil, fmt.Errorf("reading the outbox: %w", err)
	}
	var out []Entry
	for _, n := range names {
		if n.IsDir() || !strings.HasSuffix(n.Name(), ".json") {
			continue // .tmp files are half-written by definition
		}
		e, err := readEntry(filepath.Join(o.dir, n.Name()))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // removed under us by a concurrent Done
			}
			// A single unreadable entry must not hide the rest: skip it and let
			// the caller find out from what is missing, rather than failing the
			// whole queue over one bad file.
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func readEntry(path string) (Entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return Entry{}, fmt.Errorf("decoding %s: %w", path, err)
	}
	return e, nil
}
