package declared

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Turning somebody else's HTTP response into something worth reading in a chat.
//
// # Does the endpoint have to answer in a particular format?
//
// No, and requiring one would have defeated the point. The value of the request
// kind is that the logic stays in the system that already has it -- a CI server,
// a runbook service, a home-automation box. An endpoint written for this bot is
// an endpoint somebody had to write for this bot, which is barely better than
// recompiling.
//
// So the rules below take what arrives. A declaration can narrow that with
// `field` when the interesting part is buried, but nothing has to.
//
// # What it does with what arrives
//
//  1. A non-2xx status is not a reply, it is a failure. One plain sentence
//     naming the status. The body is shown only if it is small and textual --
//     an HTML error page pasted into a group chat is the failure mode here, and
//     it is the *likeliest* thing a misconfigured endpoint returns.
//  2. `field` selects a dotted path out of a JSON answer.
//  3. Otherwise, by content type: text is the reply as it stands; JSON is
//     decomposed by shape -- a string is itself, a list becomes one line per
//     entry, an object becomes sorted `key=value` lines, which is how this bot
//     already renders labels.
//  4. Anything else -- HTML, an image, a binary blob -- is described rather
//     than pasted: status, type, size.
//
// Everything is trimmed and capped, and says so when it was cut.
//
// # It is foreign text
//
// A response body lands permanently in every recipient's transcript, and an
// endpoint that reflects any of its input is an endpoint an attacker can write
// through. So it is never interpreted, never treated as a command, and the reply
// carries the action's name in front of it -- otherwise a group member cannot
// tell what the bot found from what the bot itself is saying.

const (
	// ReplyLimit is how much of an answer reaches a chat. A phone screen, not a
	// terminal: past this nobody is reading, they are scrolling.
	ReplyLimit = 2000

	// LineLimit caps a list, for the same reason.
	LineLimit = 25

	// listCap is one less, so the "… and N more" line fits inside LineLimit.
	//
	// One short rather than obviously equal, because of a bug this had: a list
	// rendered LineLimit entries *plus* the count, trimToChatSize then cut the
	// last line to get back under the limit, and the line it cut was the one
	// saying what had been dropped. Two truncations in sequence, the outer one
	// throwing away the inner one's only honest part.
	listCap = LineLimit - 1

	// MaxBodyBytes is how much is read at all. Separate from ReplyLimit because
	// this one is about not letting a misconfigured endpoint decide how much
	// memory the bot uses.
	MaxBodyBytes = 1 << 20
)

// render turns a response into reply text, or into an error worth reporting.
func render(status int, contentType string, body []byte, field string) (string, error) {
	mediaType := ""
	if contentType != "" {
		if mt, _, err := mime.ParseMediaType(contentType); err == nil {
			mediaType = mt
		}
	}
	trimmed := strings.TrimSpace(string(body))

	if status < 200 || status > 299 {
		return "", statusError(status, mediaType, trimmed)
	}

	isJSON := mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
	if field != "" && !isJSON {
		return "", fmt.Errorf("expected JSON to read %q out of, got %s", field, describe(mediaType, body))
	}

	switch {
	case isJSON:
		var value any
		if err := json.Unmarshal(body, &value); err != nil {
			return "", fmt.Errorf("the answer says it is JSON but is not: %w", err)
		}
		if field != "" {
			selected, err := selectField(value, field)
			if err != nil {
				return "", err
			}
			value = selected
		}
		return trimToChatSize(renderValue(value, 0)), nil

	case mediaType == "" && looksTextual(body), strings.HasPrefix(mediaType, "text/") && mediaType != "text/html":
		if trimmed == "" {
			return "", nil
		}
		return trimToChatSize(trimmed), nil

	default:
		// Deliberately not "here is the first 2000 characters of it". An HTML
		// page or a binary blob in a transcript is noise that cannot be
		// un-sent, and the useful information is that the endpoint answered
		// with something this cannot read.
		return "", fmt.Errorf("answered with %s, which is not something to put in a chat", describe(mediaType, body))
	}
}

func statusError(status int, mediaType, trimmed string) error {
	text := http.StatusText(status)
	if text == "" {
		text = "an unexpected status"
	}
	// A short, textual body on a failure is usually the actual reason, and
	// worth having. A long one, or HTML, is a page.
	if trimmed != "" && len(trimmed) <= 200 && !strings.Contains(trimmed, "<") &&
		(mediaType == "" || strings.HasPrefix(mediaType, "text/") || mediaType == "application/json") {
		return fmt.Errorf("%d %s: %s", status, text, oneLine(trimmed))
	}
	return fmt.Errorf("%d %s", status, text)
}

// selectField walks a dotted path. A missing step is an error rather than an
// empty reply: a declaration naming a field that is not there is a mistake in
// the declaration, and answering nothing would hide it.
func selectField(value any, field string) (any, error) {
	for _, step := range strings.Split(field, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cannot read %q: %q is not an object", field, step)
		}
		next, ok := object[step]
		if !ok {
			return nil, fmt.Errorf("the answer has no %q (looking for %q)", step, field)
		}
		value = next
	}
	return value, nil
}

// renderValue decomposes a JSON value by shape. Depth is bounded because a chat
// reply is not a place to render a tree -- past one level of nesting the answer
// is that the shape needs a `field`.
func renderValue(value any, depth int) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)

	case []any:
		if len(v) == 0 {
			return "(nothing)"
		}
		var lines []string
		for i, item := range v {
			if i == listCap {
				lines = append(lines, fmt.Sprintf("… and %d more", len(v)-listCap))
				break
			}
			lines = append(lines, oneLine(renderValue(item, depth+1)))
		}
		return strings.Join(lines, "\n")

	case map[string]any:
		if depth > 0 {
			// Inside a list, an object becomes one line, so a list of records
			// reads as a list rather than as a wall.
			return strings.Join(pairs(v), " ")
		}
		if len(v) == 0 {
			return "(nothing)"
		}
		return strings.Join(pairs(v), "\n")

	default:
		return fmt.Sprint(v)
	}
}

// pairs renders an object as sorted `key=value`. Sorted because Go's map order
// would otherwise reshuffle an unchanged answer on every call, which is exactly
// what makes the deduplicator think two identical things are different.
func pairs(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for k := range object {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(keys))
	for i, k := range keys {
		if i == listCap {
			out = append(out, fmt.Sprintf("… and %d more", len(keys)-listCap))
			break
		}
		out = append(out, k+"="+oneLine(renderValue(object[k], 1)))
	}
	return out
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// trimToChatSize trims a reply to something readable, and says when it did.
func trimToChatSize(s string) string {
	lines := strings.Split(s, "\n")
	truncatedLines := false
	if len(lines) > LineLimit {
		lines, truncatedLines = lines[:LineLimit], true
		s = strings.Join(lines, "\n")
	}
	if len(s) > ReplyLimit {
		// Cut on a rune boundary, so a truncated reply is not invalid UTF-8.
		cut := ReplyLimit
		for cut > 0 && !isRuneStart(s[cut]) {
			cut--
		}
		return strings.TrimSpace(s[:cut]) + "\n\n(cut short)"
	}
	if truncatedLines {
		return s + "\n\n(cut short)"
	}
	return s
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// looksTextual is for an endpoint that sent no content type at all, which is
// common enough in small internal services to be worth handling rather than
// refusing.
func looksTextual(body []byte) bool {
	for _, b := range body {
		if b == 0 {
			return false
		}
	}
	return true
}

func describe(mediaType string, body []byte) string {
	if mediaType == "" {
		mediaType = "an unnamed type"
	}
	return fmt.Sprintf("%s, %d bytes", mediaType, len(body))
}
