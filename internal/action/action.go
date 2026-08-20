// Package action is the closed set of things the bot can be asked to do.
//
// Closed is the point. Interpretation -- turning a message into "which action,
// with which parameters" -- happens elsewhere and is replaceable; this is the
// only thing that can actually *do* anything, and it only ever does what has
// been registered. An interpreter naming something that does not exist gets a
// lookup miss, not an attempt.
//
// That is what makes a model-driven interpreter safe to add later: it can name
// an action, and nothing else. It cannot invent one, cannot widen a parameter
// past what the spec allows, and cannot reach past the registry to whatever the
// handler is closed over.
package action

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

// ErrUnknownAction reports a name nothing is registered under.
var ErrUnknownAction = errors.New("no such action")

// Param is one parameter an action takes.
type Param struct {
	Name     string
	Summary  string
	Required bool

	// Validate narrows what the parameter may be. Called on whatever the
	// interpreter produced, so it is the boundary a model's output has to pass
	// -- and the reason a parameter is a validated string rather than free text
	// handed onward.
	Validate func(string) error
}

// Spec describes an action: what it is called, what it does, what it takes.
//
// The same specs are what a deterministic parser reads *and* what a later
// model-driven interpreter would render as tool definitions. That is not
// decoration -- it is the check that the specs are a sufficient description. If
// the parser needed something they do not carry, tool definitions would be
// missing it too, and this way that surfaces now.
type Spec struct {
	Name    string
	Summary string
	Params  []Param

	// Sensitive marks an action that changes something beyond this bot, and
	// therefore needs a confirmation rather than only an authorised sender.
	// Nothing registered today sets it; the field exists so the first action
	// that needs it cannot be added without meeting the question.
	Sensitive bool
}

// Request is one action about to run.
type Request struct {
	Spec   Spec
	Params map[string]string

	// Sender and ChatID are where the request came from, so a handler can
	// answer in the right place and can name who asked. Both come from the
	// receive path rather than from the message text: a sender cannot claim to
	// be somebody else.
	Sender string
	ChatID string
}

// Param returns one parameter, or the empty string.
func (r Request) Param(name string) string {
	if r.Params == nil {
		return ""
	}
	return r.Params[name]
}

// Result is what to say back. An empty reply means the action deliberately says
// nothing.
type Result struct {
	Reply string
}

// Handler carries out one action.
type Handler func(ctx context.Context, req Request) (Result, error)

// Registry is the set of registered actions.
type Registry struct {
	mu       sync.RWMutex
	specs    map[string]Spec
	handlers map[string]Handler
}

func NewRegistry() *Registry {
	return &Registry{specs: map[string]Spec{}, handlers: map[string]Handler{}}
}

// Register adds an action. Panics on a duplicate or an unnamed action, because
// both are wiring mistakes that should never survive to a running daemon.
func (r *Registry) Register(s Spec, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if s.Name == "" {
		panic("action: registering an action with no name")
	}
	if _, exists := r.specs[s.Name]; exists {
		panic("action: " + s.Name + " is registered twice")
	}
	r.specs[s.Name] = s
	r.handlers[s.Name] = h
}

// Specs is every registered action, by name. What `help` lists, and what a
// later interpreter turns into tool definitions.
func (r *Registry) Specs() []Spec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Spec, 0, len(r.specs))
	for _, name := range slices.Sorted(maps.Keys(r.specs)) {
		out = append(out, r.specs[name])
	}
	return out
}

// Execute runs the named action after checking its parameters.
//
// Every check happens here rather than being trusted to the interpreter,
// because the interpreter is the replaceable part. A caller that has already
// validated something loses nothing by it being validated again; a caller that
// has not is exactly the case this exists for.
func (r *Registry) Execute(ctx context.Context, name string, params map[string]string, sender, chatID string) (Result, error) {
	r.mu.RLock()
	spec, ok := r.specs[name]
	handler := r.handlers[name]
	r.mu.RUnlock()

	if !ok {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownAction, name)
	}

	// Only parameters the spec names survive. An interpreter passing something
	// extra is not an error worth refusing over -- but it is not passed on
	// either, so a handler can never receive a parameter nobody described.
	kept := map[string]string{}
	for _, p := range spec.Params {
		v, given := params[p.Name]
		v = strings.TrimSpace(v)
		if !given || v == "" {
			if p.Required {
				return Result{}, fmt.Errorf("%s needs %s", spec.Name, p.Name)
			}
			continue
		}
		if p.Validate != nil {
			if err := p.Validate(v); err != nil {
				return Result{}, fmt.Errorf("%s: %s: %w", spec.Name, p.Name, err)
			}
		}
		kept[p.Name] = v
	}

	return handler(ctx, Request{Spec: spec, Params: kept, Sender: sender, ChatID: chatID})
}
