package authz

import "testing"

// No allow-list means no command surface, not "anyone". An operator who has not
// thought about who may command their bot has not implicitly decided that
// everyone may.
func TestAnEmptyListDisablesCommandsEntirely(t *testing.T) {
	p := New(nil, true)
	if p.Enabled() {
		t.Fatal("with nobody configured there is no command surface")
	}
	if p.MayCommand("qanybody", false) {
		t.Error("nobody may command a bot with no commanders configured")
	}
	// Even with group commands allowed, which is the combination somebody
	// switching on the wrong thing would produce.
	if p.MayCommand("qanybody", true) {
		t.Error("allowing group commands must not imply allowing anybody")
	}
}

func TestOnlyListedAccountsMayCommand(t *testing.T) {
	p := New([]string{"qalice", "qbob"}, false)

	if !p.MayCommand("qalice", false) {
		t.Error("a listed account may command in a one-to-one chat")
	}
	if p.MayCommand("qstranger", false) {
		t.Error("an unlisted account may not")
	}
}

// Group commands are off unless asked for: a command in a group is visible to
// everyone in it, its answer is too, and the membership drifts without the
// operator being told.
func TestGroupCommandsAreOffByDefault(t *testing.T) {
	off := New([]string{"qalice"}, false)
	if off.MayCommand("qalice", true) {
		t.Error("a listed account must not command from a group by default")
	}
	if !off.MayCommand("qalice", false) {
		t.Error("the same account may still command one-to-one")
	}

	on := New([]string{"qalice"}, true)
	if !on.MayCommand("qalice", true) {
		t.Error("with group commands enabled a listed account may command from a group")
	}
	// Still only listed accounts -- enabling group commands widens *where*, not
	// *who*.
	if on.MayCommand("qstranger", true) {
		t.Error("enabling group commands must not widen who may command")
	}
}

func TestBlankEntriesAreIgnored(t *testing.T) {
	// What an EnvironmentFile edited by hand actually looks like.
	p := New([]string{" qalice ", "", "  "}, false)
	if p.Commanders() != 1 {
		t.Errorf("want one commander, got %d", p.Commanders())
	}
	if !p.MayCommand("qalice", false) {
		t.Error("surrounding whitespace must not stop a match")
	}
}
