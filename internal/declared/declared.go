// Package declared is actions an operator writes in a file instead of in Go.
//
// # What this is for
//
// Adding a command used to mean editing internal/action/builtin.go and
// rebuilding, which is honest for a Go binary and useless to the person actually
// running one. Two kinds cover most of what people want:
//
//   - a fixed reply -- an on-call rota, a runbook link, a canned answer. Nothing
//     is executed, so this adds no attack surface at all.
//   - an HTTP request -- the bot asks something that already exists and reports
//     what came back. The logic stays in the system that has it; the bot holds a
//     URL and a token rather than a shell.
//
// # What this deliberately is not
//
// It runs no commands. `restart=systemctl restart nginx` in a configuration file
// is remote code execution for anybody who can get a message past the allow-list,
// and it would arrive looking like a convenience feature. An action that has to
// run something on the host belongs behind an endpoint that decides for itself
// whether to do it -- which is the same boundary an operator would want anyway,
// and which they can already reach with the request kind here.
//
// # The invariant that matters
//
// A declared action is still a *registered* action: closed set, typed
// parameters, authorization before interpretation. Nothing here widens what an
// interpreter can reach; it only changes who writes the handler down.
package declared

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Action is one declaration out of the file.
//
// Exactly one of Reply and Request is set. Both would be ambiguous about what
// the bot is being asked to do, and neither is an action.
type Action struct {
	Name    string  `json:"name"`
	Summary string  `json:"summary"`
	Params  []Param `json:"params,omitempty"`

	// Reply is a fixed answer, with {{param}} placeholders filled in.
	Reply string `json:"reply,omitempty"`

	// Request asks something else and reports the answer.
	Request *Request `json:"request,omitempty"`
}

// Param is one parameter the action takes. A deliberately smaller vocabulary
// than action.Param: a declaration cannot supply a Go validation function, so
// what it can constrain is what can be expressed in a file.
type Param struct {
	Name     string `json:"name"`
	Summary  string `json:"summary,omitempty"`
	Required bool   `json:"required,omitempty"`

	// Pattern is a regular expression the whole value must match. Optional, and
	// the only narrowing a file can express -- which is why an action whose
	// parameter reaches a URL should carry one.
	Pattern string `json:"pattern,omitempty"`

	compiled *regexp.Regexp
}

// Request is an outbound HTTP call.
type Request struct {
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`

	// TokenFile holds a bearer token, read at request time. A file rather than a
	// value in this file, for the same reason the invite code is: an environment
	// and a configuration file are both readable by more things than the secret
	// deserves, and a token that rotates should not need this file edited.
	TokenFile string `json:"tokenFile,omitempty"`

	// TokenHeader is where the token goes, defaulting to Authorization with a
	// "Bearer " prefix. Named because plenty of real APIs use something else.
	TokenHeader string `json:"tokenHeader,omitempty"`

	// Body is sent with a non-GET, with placeholders filled in. The content type
	// is whatever Headers says; nothing is assumed about it.
	Body string `json:"body,omitempty"`

	// Field selects part of a JSON answer, as a dotted path ("result.items").
	// Configured rather than guessed: see render.go for what happens without it.
	Field string `json:"field,omitempty"`

	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`

	// scheme and host are what the URL template resolves to with every
	// placeholder emptied, kept so a filled-in parameter cannot move the request
	// to a different server. See buildURL.
	scheme, host string
}

// DefaultTimeout is how long a declared request may take. Short, because
// somebody is waiting in a chat for the answer.
const DefaultTimeout = 10 * time.Second

// MaxTimeout bounds what a declaration may ask for. A command that hangs for
// minutes has already failed as an answer, and it holds a slot while it does.
const MaxTimeout = 60 * time.Second

var placeholder = regexp.MustCompile(`\{\{([a-zA-Z0-9_]+)\}\}`)

// Load reads and validates a declarations file. An empty path is not an error --
// most bots declare nothing.
//
// Everything that can be checked is checked here rather than when an action
// first runs: a typo in this file should stop a daemon from starting, not
// surface as a broken command the first time somebody in a chat tries it.
func Load(path string) ([]Action, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var actions []Action
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	// An unknown field is a typo, and a typo in a security-relevant declaration
	// -- "patern" instead of "pattern" -- would otherwise be silently ignored
	// and leave a parameter unconstrained.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&actions); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	seen := make(map[string]struct{}, len(actions))
	for i := range actions {
		if err := actions[i].validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if _, dup := seen[actions[i].Name]; dup {
			return nil, fmt.Errorf("%s: %q is declared twice", path, actions[i].Name)
		}
		seen[actions[i].Name] = struct{}{}
	}
	return actions, nil
}

var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func (a *Action) validate() error {
	if !validName.MatchString(a.Name) {
		return fmt.Errorf("%q is not a usable action name: lowercase letters, digits and dashes, starting with a letter", a.Name)
	}
	if a.Summary == "" {
		// Required because /help lists it, and an action nobody can find out
		// about is an action nobody will use.
		return fmt.Errorf("action %q needs a summary: it is what /help shows", a.Name)
	}
	switch {
	case a.Reply != "" && a.Request != nil:
		return fmt.Errorf("action %q has both a reply and a request: pick one", a.Name)
	case a.Reply == "" && a.Request == nil:
		return fmt.Errorf("action %q does nothing: it needs a reply or a request", a.Name)
	}

	names := make(map[string]struct{}, len(a.Params))
	for i := range a.Params {
		p := &a.Params[i]
		if !validName.MatchString(p.Name) {
			return fmt.Errorf("action %q has an unusable parameter name %q", a.Name, p.Name)
		}
		if _, dup := names[p.Name]; dup {
			return fmt.Errorf("action %q declares the parameter %q twice", a.Name, p.Name)
		}
		names[p.Name] = struct{}{}
		if p.Pattern != "" {
			re, err := regexp.Compile(p.Pattern)
			if err != nil {
				return fmt.Errorf("action %q, parameter %q: %w", a.Name, p.Name, err)
			}
			// Anchored, because an unanchored pattern matches a substring and
			// would let anything through as long as it contained something
			// acceptable -- which is not what anybody writing a pattern means.
			p.compiled = re
			if !strings.HasPrefix(p.Pattern, "^") || !strings.HasSuffix(p.Pattern, "$") {
				p.compiled = regexp.MustCompile(`\A(?:` + p.Pattern + `)\z`)
			}
		}
	}

	// Every placeholder has to name a declared parameter, or it would reach the
	// URL or the reply as the literal text `{{typo}}`.
	for _, text := range a.templates() {
		for _, m := range placeholder.FindAllStringSubmatch(text, -1) {
			if _, ok := names[m[1]]; !ok {
				return fmt.Errorf("action %q uses {{%s}}, which is not one of its parameters", a.Name, m[1])
			}
		}
	}

	if a.Request != nil {
		return a.Request.validate(a.Name)
	}
	return nil
}

func (a *Action) templates() []string {
	if a.Request == nil {
		return []string{a.Reply}
	}
	out := []string{a.Request.URL, a.Request.Body}
	for _, k := range sortedKeys(a.Request.Headers) {
		out = append(out, a.Request.Headers[k])
	}
	return out
}

func (r *Request) validate(action string) error {
	if r.Method == "" {
		r.Method = http.MethodGet
	}
	r.Method = strings.ToUpper(r.Method)
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead:
	default:
		return fmt.Errorf("action %q: %s is not a method this can send", action, r.Method)
	}

	// Resolve the template with every placeholder emptied, and keep what it
	// points at. This is the check that a parameter cannot move the request to
	// another server: whatever the substituted URL turns out to be, it has to
	// still be this scheme and this host.
	skeleton := placeholder.ReplaceAllString(r.URL, "")
	u, err := url.Parse(skeleton)
	if err != nil {
		return fmt.Errorf("action %q: %q is not a URL: %w", action, r.URL, err)
	}
	// Three separate mistakes with three separate sentences, in the order that
	// makes each one accurate. A wrong scheme usually also leaves no host, and a
	// bare path has neither -- so a single check would report whichever it
	// happened to reach first and send the reader after the wrong thing.
	switch u.Scheme {
	case "":
		return fmt.Errorf("action %q: %q needs a whole URL, with a scheme and a host -- there is no base address for a path to hang off", action, r.URL)
	case "https":
	case "http":
		// Allowed, because a bot on the same box as the thing it is asking is a
		// real and reasonable case. Not silently, though: this is a request
		// carrying a token over a network the operator is vouching for.
	default:
		return fmt.Errorf("action %q: %q is not http or https", action, r.URL)
	}
	if u.Host == "" {
		return fmt.Errorf("action %q: %q names no host", action, r.URL)
	}
	r.scheme, r.host = u.Scheme, u.Host

	if r.TimeoutSeconds < 0 {
		return fmt.Errorf("action %q: a negative timeout", action)
	}
	if time.Duration(r.TimeoutSeconds)*time.Second > MaxTimeout {
		return fmt.Errorf("action %q: %ds is longer than the %s ceiling -- somebody is waiting in a chat", action, r.TimeoutSeconds, MaxTimeout)
	}
	if r.TokenHeader == "" {
		r.TokenHeader = "Authorization"
	}
	return nil
}

func (r *Request) timeout() time.Duration {
	if r.TimeoutSeconds > 0 {
		return time.Duration(r.TimeoutSeconds) * time.Second
	}
	return DefaultTimeout
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
