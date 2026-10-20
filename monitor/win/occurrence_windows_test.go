//go:build windows

package win

import (
	"errors"
	"testing"
)

// The native question style renders only a Book occurrence: a prompt that
// names none is refused before any window exists, whatever its text.
func TestShowQuestionRefusesFreeText(t *testing.T) {
	prompt := QuestionPrompt{Title: "OpenAbstractions", Instruction: "Connections updated", Content: "No answer was sent.",
		Choices: []QuestionChoice{{Label: "OK", Value: "acknowledged"}}, Level: PromptLevelBlue}
	for _, id := range []string{"", "line\nbreak", string(make([]byte, 129)), "\xff"} {
		before := activePermissionDialog.Load()
		_, err := ShowQuestion(Occurrence{ID: id, Prompt: prompt})
		if !errors.Is(err, errNotOccurrence) {
			t.Errorf("id %q: err = %v, want the occurrence refusal", id, err)
		}
		if activePermissionDialog.Load() != before {
			t.Errorf("id %q: a dialog opened", id)
		}
	}
	for _, id := range []string{"q-1f2e", "8c41a0c3d9f2b7e6a5d4c3b2", "choice:42"} {
		if !bookOccurrenceID(id) {
			t.Errorf("Book id %q refused", id)
		}
	}
}
