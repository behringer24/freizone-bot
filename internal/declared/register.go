package declared

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/behringer24/freizone-bot/internal/action"
)

// Client is the http.Client declared requests are sent with.
//
// Its own, rather than the one pkg/client uses: these requests go to whatever an
// operator named, and they should not share a connection pool, a redirect policy
// or a timeout with the bot's conversation with its own server.
//
// Redirects are followed, but only within the host the declaration named -- the
// same rule buildURL enforces on the way out, applied again on the way back,
// because a 302 is the other way a request ends up somewhere unintended.
func Client() *http.Client {
	return &http.Client{
		Timeout: MaxTimeout,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: MaxTimeout,
			MaxIdleConnsPerHost:   2,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Host != via[0].URL.Host {
				return fmt.Errorf("refusing to follow a redirect from %s to %s", via[0].URL.Host, req.URL.Host)
			}
			return nil
		},
	}
}

// Register adds declared actions to the registry.
//
// Collisions with an already-registered action are reported rather than allowed
// to panic: Registry.Register panics on a duplicate, which is right for a wiring
// mistake in Go and wrong for a name somebody typed in a file. A daemon should
// refuse to start with a clear sentence, not crash with a stack trace.
func Register(r *action.Registry, actions []Action, client *http.Client) error {
	existing := make(map[string]struct{})
	for _, spec := range r.Specs() {
		existing[spec.Name] = struct{}{}
	}

	for _, declared := range actions {
		if _, clash := existing[declared.Name]; clash {
			return fmt.Errorf("the declared action %q has the same name as a built-in one", declared.Name)
		}
		existing[declared.Name] = struct{}{}
		r.Register(declared.spec(), declared.handler(client))
	}
	return nil
}

func (a Action) spec() action.Spec {
	spec := action.Spec{
		Name:    a.Name,
		Summary: a.Summary,
		// Sensitive on a request, and not on a fixed reply. A reply changes
		// nothing; a request reaches out of this machine, which is the line that
		// field exists to mark.
		Sensitive: a.Request != nil,
	}
	for _, p := range a.Params {
		spec.Params = append(spec.Params, action.Param{
			Name:     p.Name,
			Summary:  p.Summary,
			Required: p.Required,
			Validate: p.validator(),
		})
	}
	return spec
}

func (p Param) validator() func(string) error {
	if p.compiled == nil {
		return nil
	}
	pattern, name := p.compiled, p.Name
	return func(value string) error {
		if !pattern.MatchString(value) {
			// The pattern itself is not quoted back. It is a regular
			// expression, the person reading this is in a chat window, and it
			// would be advice about how to construct a passing input.
			return fmt.Errorf("%s is not in the expected form", name)
		}
		return nil
	}
}

func (a Action) handler(client *http.Client) action.Handler {
	if a.Request != nil {
		request, name := a.Request, a.Name
		return func(ctx context.Context, req action.Request) (action.Result, error) {
			text, err := request.Do(ctx, client, req.Params)
			if err != nil {
				// One plain sentence, because it goes to a person in a chat.
				// The action is named because a reader with several declared
				// actions cannot otherwise tell which one failed.
				return action.Result{}, fmt.Errorf("%s: %w", name, err)
			}
			return action.Result{Reply: frame(name, text)}, nil
		}
	}

	reply := a.Reply
	return func(_ context.Context, req action.Request) (action.Result, error) {
		return action.Result{Reply: fill(reply, req.Params, nil)}, nil
	}
}

// frame puts the action's name in front of an answer that came from somewhere
// else.
//
// Not decoration. The text below it was written by another system, permanently
// lands in every recipient's transcript, and an endpoint that reflects its input
// is one an outsider can write through. Without a line saying where it came
// from, a group member cannot tell what the bot *found* from what the bot is
// *saying* -- and a reply that reads like the bot's own voice is the useful half
// of anything somebody would want to inject into it.
func frame(name, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return name + ": (no answer)"
	}
	if !strings.Contains(text, "\n") {
		return name + ": " + text
	}
	return name + ":\n\n" + text
}
