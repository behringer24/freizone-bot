package webhook

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// MinTokenLength is the shortest token accepted.
//
// Enforced rather than advised: this endpoint is reachable by whatever can route
// to it, and a short token is guessable at a rate nothing here limits. A
// refusal at startup is the only moment anybody is paying attention.
const MinTokenLength = 24

// LoadTokens reads sender tokens from a file: one `name:token` per line, `#`
// comments and blank lines ignored.
//
// A file rather than an environment variable, for the same reason the invite
// code is one: an environment is readable by more things than a credential
// deserves -- `docker inspect` and `/proc/<pid>/environ` among them -- and a
// token that rotates should not need the unit file edited.
//
// A name per token so that a single sender can be switched off without
// switching off the rest, and so a log line can say which one sent something.
// With one shared token, "who is flooding us" has no answer.
func LoadTokens(path string) (map[string]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	defer file.Close() //nolint:errcheck // read-only

	// A credential file that anybody can read is a credential anybody has. Said
	// here rather than left to the operator's care, since the failure is silent.
	//
	// Not on Windows, following the same reasoning as account.checkPrivate:
	// permissions there are ACLs and the mode bits os.Stat synthesises say
	// nothing about them, so this check would refuse to start on every
	// development machine while proving nothing.
	if runtime.GOOS != "windows" {
		if info, err := file.Stat(); err == nil && info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("%s is readable beyond its owner (mode %o): chmod 600 it", path, info.Mode().Perm())
		}
	}

	tokens := make(map[string]string)
	names := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		name, token, ok := strings.Cut(text, ":")
		name, token = strings.TrimSpace(name), strings.TrimSpace(token)
		switch {
		case !ok || name == "" || token == "":
			return nil, fmt.Errorf("%s line %d is not in the form name:token", path, line)
		case len(token) < MinTokenLength:
			// The token is never quoted back, here or anywhere -- not into an
			// error, not into a log line.
			return nil, fmt.Errorf("%s line %d: the token for %q is shorter than %d characters", path, line, name, MinTokenLength)
		}
		if _, dup := names[name]; dup {
			return nil, fmt.Errorf("%s: %q appears twice", path, name)
		}
		if existing, dup := tokens[token]; dup {
			// Two senders sharing a token defeats the point of naming them: the
			// log would attribute a request to whichever one the map happened to
			// yield.
			return nil, fmt.Errorf("%s: %q and %q have the same token", path, existing, name)
		}
		names[name] = struct{}{}
		tokens[token] = name
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(tokens) == 0 {
		// Fail closed and say so. An empty file with a listener configured is a
		// listener nobody can use, which is safe but is not what was intended.
		return nil, fmt.Errorf("%s names no senders: the webhook would accept nothing", path)
	}
	return tokens, nil
}
