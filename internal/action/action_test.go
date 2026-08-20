package action

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func registryWith(t *testing.T) (*Registry, *[]Request) {
	t.Helper()
	r := NewRegistry()
	var seen []Request
	r.Register(Spec{
		Name:    "echo",
		Summary: "say something back",
		Params: []Param{
			{Name: "what", Required: true},
			{Name: "times", Validate: func(v string) error {
				if v != "1" && v != "2" {
					return errors.New("has to be 1 or 2")
				}
				return nil
			}},
		},
	}, func(_ context.Context, req Request) (Result, error) {
		seen = append(seen, req)
		return Result{Reply: req.Param("what")}, nil
	})
	return r, &seen
}

// The property the whole package exists for: a name nothing is registered under
// gets a lookup miss, not an attempt. This is what makes a model-driven
// interpreter safe later -- it can name an action and nothing else.
func TestAnUnknownActionIsALookupMissNotAnAttempt(t *testing.T) {
	r, seen := registryWith(t)

	_, err := r.Execute(context.Background(), "delete_everything", map[string]string{"confirm": "yes"}, "qalice", "qchat")
	if !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("want ErrUnknownAction, got %v", err)
	}
	if len(*seen) != 0 {
		t.Error("nothing may have run")
	}
	// The name is in the error, so a log can say what was asked for.
	if !strings.Contains(err.Error(), "delete_everything") {
		t.Errorf("the error should name it, got %q", err)
	}
}

// A parameter nobody described never reaches a handler. An interpreter -- or a
// model -- passing something extra cannot widen the surface it was shown.
func TestOnlyDescribedParametersReachTheHandler(t *testing.T) {
	r, seen := registryWith(t)

	_, err := r.Execute(context.Background(), "echo",
		map[string]string{"what": "hello", "rm": "-rf /", "times": "1"}, "qalice", "qchat")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(*seen) != 1 {
		t.Fatalf("the handler should have run once, got %d", len(*seen))
	}
	got := (*seen)[0]
	if _, present := got.Params["rm"]; present {
		t.Error("an undescribed parameter reached the handler")
	}
	if got.Params["what"] != "hello" || got.Params["times"] != "1" {
		t.Errorf("the described ones should be there, got %+v", got.Params)
	}
}

// Validation is the boundary a model's output has to pass, and it runs here
// rather than being trusted to whoever produced the parameters.
func TestValidationRefusesABadParameter(t *testing.T) {
	r, seen := registryWith(t)

	_, err := r.Execute(context.Background(), "echo",
		map[string]string{"what": "hello", "times": "9999"}, "qalice", "qchat")
	if err == nil {
		t.Fatal("a value the spec rejects must be refused")
	}
	if len(*seen) != 0 {
		t.Error("the handler must not have run")
	}
	if !strings.Contains(err.Error(), "times") {
		t.Errorf("the error should name the parameter, got %q", err)
	}
}

func TestARequiredParameterIsRequired(t *testing.T) {
	r, _ := registryWith(t)

	if _, err := r.Execute(context.Background(), "echo", nil, "qalice", "qchat"); err == nil {
		t.Fatal("a missing required parameter must be refused")
	}
	// Blank counts as missing: a parser handing through an empty string should
	// not satisfy a requirement.
	if _, err := r.Execute(context.Background(), "echo", map[string]string{"what": "  "}, "qalice", "qchat"); err == nil {
		t.Error("whitespace must not satisfy a required parameter")
	}
}

// Sender and chat come from the receive path, so a handler can trust them --
// a message cannot claim to be from somebody else.
func TestTheHandlerLearnsWhoAskedAndWhere(t *testing.T) {
	r, seen := registryWith(t)

	if _, err := r.Execute(context.Background(), "echo",
		map[string]string{"what": "x"}, "qalice", "qgroup1"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := (*seen)[0]
	if got.Sender != "qalice" || got.ChatID != "qgroup1" {
		t.Errorf("got sender %q chat %q", got.Sender, got.ChatID)
	}
}

func TestRegisteringTwiceIsAWiringMistake(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering the same action twice should not survive to a running daemon")
		}
	}()
	r := NewRegistry()
	spec := Spec{Name: "twice"}
	noop := func(context.Context, Request) (Result, error) { return Result{}, nil }
	r.Register(spec, noop)
	r.Register(spec, noop)
}

// --- the built-in set ------------------------------------------------------

func TestHelpListsWhatIsActuallyRegistered(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, func() string { return "fine" }, DefaultJokes)

	out, err := r.Execute(context.Background(), "help", nil, "qalice", "qchat")
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	for _, want := range []string{"/help", "/ping", "/status", "/joke"} {
		if !strings.Contains(out.Reply, want) {
			t.Errorf("help should list %s:\n%s", want, out.Reply)
		}
	}
}

// An action whose dependency is absent is not registered at all, rather than
// registered and failing when used: help then tells the truth about what this
// bot can do.
func TestAnActionWithoutItsDependencyIsNotOffered(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r, nil, nil) // no status source, no jokes

	if out, _ := r.Execute(context.Background(), "help", nil, "q", "q"); strings.Contains(out.Reply, "/joke") {
		t.Error("help must not offer an action that was never registered")
	}
	if _, err := r.Execute(context.Background(), "joke", nil, "q", "q"); !errors.Is(err, ErrUnknownAction) {
		t.Errorf("want ErrUnknownAction, got %v", err)
	}
}

// "Of the day" is a promise: asking twice in an afternoon must not produce two
// different answers, and somebody showing it to a colleague should get the same
// one.
func TestTheJokeOfTheDayIsStableWithinADay(t *testing.T) {
	day := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	first := jokeOfTheDay(DefaultJokes, day)
	for _, hour := range []int{0, 12, 23} {
		at := time.Date(2026, 8, 20, hour, 30, 0, 0, time.UTC)
		if again := jokeOfTheDay(DefaultJokes, at); again != first {
			t.Fatalf("the joke changed within one day: %q then %q", first, again)
		}
	}
	// And it does move on eventually, or it is not a joke of the *day*.
	var moved bool
	for d := 1; d <= len(DefaultJokes); d++ {
		if jokeOfTheDay(DefaultJokes, day.AddDate(0, 0, d)) != first {
			moved = true
			break
		}
	}
	if !moved {
		t.Error("the joke never changes on any following day")
	}
}

func TestJokesCanComeFromAFile(t *testing.T) {
	path := t.TempDir() + "/jokes.txt"
	content := "# a comment\n\nfirst one\nsecond one\n"
	if err := writeFile(path, content); err != nil {
		t.Fatalf("writing: %v", err)
	}

	got, err := LoadJokes(path)
	if err != nil {
		t.Fatalf("LoadJokes: %v", err)
	}
	if len(got) != 2 || got[0] != "first one" {
		t.Errorf("comments and blank lines should be skipped, got %#v", got)
	}
}

func TestAnEmptyJokesFileIsAConfigurationError(t *testing.T) {
	path := t.TempDir() + "/empty.txt"
	if err := writeFile(path, "# nothing but comments\n"); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, err := LoadJokes(path); err == nil {
		t.Error("a file with nothing to say should be reported, not silently empty")
	}
}

func TestNoJokesFileMeansTheBuiltInSet(t *testing.T) {
	got, err := LoadJokes("")
	if err != nil {
		t.Fatalf("LoadJokes: %v", err)
	}
	if len(got) != len(DefaultJokes) {
		t.Errorf("want the built-in set, got %d entries", len(got))
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
