//go:build windows

package win

import (
	"testing"
)

func countPermissionChimes(t *testing.T) *int {
	t.Helper()
	count := 0
	previous := permissionChimeSound
	permissionChimeSound = func() { count++ }
	t.Cleanup(func() { permissionChimeSound = previous })
	return &count
}

// One prompt plays its sound once: a second ring from a relayout, a DPI or
// theme change, or the TaskDialog fallback after a Windows 11 layout that
// does not fit, finds the same chime already rung.
func TestPermissionChimeOncePerPrompt(t *testing.T) {
	count := countPermissionChimes(t)
	prompt := QuestionPrompt{Permission: true}
	chime := newQuestionChime(prompt)
	chime.ring()
	chime.ring()
	pendingTaskDialogChime.Store(chime)
	pendingTaskDialogChime.Swap(nil).ring()
	if *count != 1 {
		t.Fatalf("one prompt rang %d times", *count)
	}
	newQuestionChime(prompt).ring()
	if *count != 2 {
		t.Fatalf("a second prompt rang %d times in total, want 2", *count)
	}
	pendingTaskDialogChime.Swap(nil).ring() // nothing pending: silent
	if *count != 2 {
		t.Fatal("a TaskDialog with no pending chime rang")
	}
}

// Only a permission question sounds. Ordinary application choices, ordinary
// questions and result dialogs are silent.
func TestPermissionChimeKinds(t *testing.T) {
	count := countPermissionChimes(t)
	for _, c := range []struct {
		name   string
		prompt QuestionPrompt
		rings  bool
	}{
		{"permission", QuestionPrompt{Permission: true}, true},
		{"ordinary choice", QuestionPrompt{AppChoice: true}, false},
		{"ordinary choice marked permission", QuestionPrompt{Permission: true, AppChoice: true}, false},
		{"ordinary question", QuestionPrompt{}, false},
	} {
		before := *count
		newQuestionChime(c.prompt).ring()
		if rang := *count > before; rang != c.rings {
			t.Errorf("%s: rang=%t want %t", c.name, rang, c.rings)
		}
	}
	// ShowQuestionResult builds its prompt without a chime; ringing none is silent.
	before := *count
	var none *questionChime
	none.ring()
	if *count != before {
		t.Error("a prompt without a chime rang")
	}
}

// Windows' held-notification states (QUNS) silence the sound; an unknown state
// leaves the person's sound scheme in charge.
func TestPermissionChimeFollowsNotificationState(t *testing.T) {
	for state, want := range map[uint32]bool{1: false, 2: false, 3: false, 4: false, 5: true, 6: false, 7: true} {
		if got := permissionChimeAllowed(state, true); got != want {
			t.Errorf("QUNS %d: allowed=%t want %t", state, got, want)
		}
	}
	if !permissionChimeAllowed(0, false) {
		t.Error("unknown notification state silenced the sound")
	}
	state, known := userNotificationState()
	t.Logf("this session's notification state: QUNS=%d known=%t allowed=%t", state, known, permissionChimeAllowed(state, known))
}
