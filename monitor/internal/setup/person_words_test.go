package setup

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
)

// retiredWords are the words no person-facing Setup or asks string uses
// (architect wording ruling 2026-11-09): the runtime, the retired "picture
// engine" and a raw graphics family code.
var retiredWords = regexp.MustCompile(`PyTorch|picture engine|gfx[0-9]`)

// The setup question's frozen goldens, the permission sentences, every
// catalogue choice's display name and the graphics blocker say none of
// them; identity strings (plan names, catalogue titles) are never shown.
func TestPersonFacingSetupWordsAreRetiredFree(t *testing.T) {
	raw, err := os.ReadFile("testdata/setup_question_wording.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []map[string]any
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		for _, key := range []string{"title", "instruction", "details"} {
			if s, _ := c[key].(string); retiredWords.MatchString(s) {
				t.Errorf("golden %s says %q", key, retiredWords.FindString(s))
			}
		}
	}
	for _, kind := range []string{"make_pictures", "write_pictures", "read_pictures"} {
		if s := asks.SetupPermissionWords(kind); retiredWords.MatchString(s) {
			t.Errorf("%s: %q", kind, s)
		}
	}
	c := catalogForTarget("windows", "amd64")
	for _, choice := range c.Choices {
		if retiredWords.MatchString(choice.Name()) {
			t.Errorf("choice %s shows %q", choice.ID, choice.Name())
		}
	}
	for _, blocker := range c.graphicsBlockers(map[AppID]bool{ComfyUI: true}, nil) {
		if retiredWords.MatchString(blocker) {
			t.Errorf("blocker %q", blocker)
		}
	}
	for _, engine := range []AppID{Lemonade, TheNoise, PyTorch} {
		if retiredWords.MatchString(engineFeatureTitle(engine)) {
			t.Errorf("feature title %q", engineFeatureTitle(engine))
		}
	}
}
