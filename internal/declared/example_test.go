package declared

import (
	"path/filepath"
	"testing"
)

// The example file that ships with this repository has to load.
//
// Not a formality. It is the first thing anybody copies, so a broken one is a
// broken first impression -- and JSON has no comments, so there is no way to
// warn a reader inside it. This is the warning, in the place that can fail.
//
// It also pins the escaping that is easy to get wrong: `\\p{L}` in the file has
// to reach Go's regexp as `\p{L}`. Writing that pattern in a test got it wrong
// on the first attempt, with the same error an operator would see.
func TestTheExampleFileLoads(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "actions.example.json")

	actions, err := Load(path)
	if err != nil {
		t.Fatalf("the shipped example does not load: %v", err)
	}
	if len(actions) == 0 {
		t.Fatal("the example declares nothing")
	}

	for _, a := range actions {
		if a.Summary == "" {
			t.Errorf("%q has no summary, so /help would not describe it", a.Name)
		}
		// Every parameter that reaches a URL wants a pattern -- the example is
		// what people copy, so it should model that rather than leave it out.
		if a.Request == nil {
			continue
		}
		for _, p := range a.Params {
			if p.compiled == nil {
				t.Errorf("%s: parameter %q reaches a URL with no pattern", a.Name, p.Name)
			}
		}
	}
}
