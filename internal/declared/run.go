package declared

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// fill substitutes {{param}} placeholders.
//
// escape decides what happens to a value on the way in, and is the whole reason
// this is one function rather than a string replace at each site: a value going
// into a URL has to be percent-encoded, or a parameter containing `&` or `/`
// rewrites the request rather than filling in a blank.
func fill(template string, params map[string]string, escape func(string) string) string {
	return placeholder.ReplaceAllStringFunc(template, func(match string) string {
		name := match[2 : len(match)-2]
		value := params[name]
		if escape != nil {
			return escape(value)
		}
		return value
	})
}

// buildURL fills in the URL and then checks it still points where the
// declaration said.
//
// The check is the point. Percent-encoding already stops the obvious rewrite,
// but this is the belt: whatever a parameter turns out to contain, the request
// has to still go to the scheme and host resolved when the file was loaded. An
// action whose parameter could redirect it elsewhere would be a way to make the
// bot -- which sits inside a network and holds a token -- fetch a URL of the
// caller's choosing.
func (r *Request) buildURL(params map[string]string) (string, error) {
	filled := fill(r.URL, params, url.QueryEscape)

	u, err := url.Parse(filled)
	if err != nil {
		return "", fmt.Errorf("the parameters do not make a usable URL: %w", err)
	}
	if u.Scheme != r.scheme || u.Host != r.host {
		return "", fmt.Errorf("refusing to send this somewhere other than %s://%s", r.scheme, r.host)
	}
	return filled, nil
}

// Do carries out one declared request and returns what to say back.
func (r *Request) Do(ctx context.Context, client *http.Client, params map[string]string) (string, error) {
	target, err := r.buildURL(params)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()

	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(fill(r.Body, params, nil))
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target, body)
	if err != nil {
		return "", err
	}
	for _, name := range sortedKeys(r.Headers) {
		req.Header.Set(name, fill(r.Headers[name], params, nil))
	}
	if r.TokenFile != "" {
		// Read per request rather than held: a rotated token then takes effect
		// without a restart, and a token that is not needed yet is not sitting
		// in this process's memory.
		raw, err := os.ReadFile(r.TokenFile)
		if err != nil {
			return "", fmt.Errorf("reading the token: %w", err)
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			return "", fmt.Errorf("the token file %s is empty", r.TokenFile)
		}
		if strings.EqualFold(r.TokenHeader, "Authorization") {
			token = "Bearer " + token
		}
		req.Header.Set(r.TokenHeader, token)
	}

	resp, err := client.Do(req)
	if err != nil {
		// Not wrapped further: the caller turns this into one sentence for a
		// chat, and Go's transport errors already name the host and the reason.
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck // reading is done; a close error changes nothing

	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading the answer: %w", err)
	}
	if len(raw) > MaxBodyBytes {
		return "", fmt.Errorf("the answer is larger than %d bytes", MaxBodyBytes)
	}

	return render(resp.StatusCode, resp.Header.Get("Content-Type"), raw, r.Field)
}
