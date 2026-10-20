package setup

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

var updateSetupWording = flag.Bool("update-setup-wording", false, "update the shared native-route fixture from the catalogue's current Setup wording")

type setupWordingFixture struct {
	Name        string            `json:"name"`
	Facts       map[string]string `json:"facts"`
	Title       string            `json:"title"`
	Instruction string            `json:"instruction"`
	Details     string            `json:"details"`
}

// The shared native-route fixture holds the catalogue's Setup wording for
// every pick state, picture plan and target count; a wording change updates
// it explicitly with -update-setup-wording.
func TestStepQuestionWordingMatchesFixture(t *testing.T) {
	w := newStepWorld(t)
	picture := newPictureFake(w)
	chatPlan := CodingModelPlan()
	var fixtures []setupWordingFixture
	for _, image := range []struct {
		name string
		plan ModelPlan
	}{{"bf16", ApprovedBF16Plan()}, {"pytorch", PyTorchModelPlan()}, {"alternative", TheNoiseModelPlan()}} {
		for _, count := range []int{1, 15} {
			t.Run(fmt.Sprintf("%s/%d", image.name, count+1), func(t *testing.T) {
				var apps []StepApp
				for i := 0; i < count; i++ {
					apps = append(apps, StepApp{Name: fmt.Sprintf("target-%02d", i), Title: fmt.Sprintf("Target %02d", i), Use: UseCode})
				}
				pick := &StepPick{Title: "Qwen3.6 35B", Family: "qwen3.6-35b-a3b", License: strings.Join(chatPlan.Licenses, ", "), Digest: "sha256:" + chatPlan.Assets[0].Download.SHA256}
				work := &stepPictureWork{app: StepApp{Name: "comfyui", Title: "ComfyUI", Use: UseImage}, plan: image.plan, offer: picture.offer}
				for _, withPictures := range []bool{false, true} {
					var p *stepPictureWork
					if withPictures {
						p = work
					}
					for _, state := range []string{"download", "served", "hosted", "local"} {
						copy := *pick
						copy.Served = state == "served"
						copy.Hosted = state == "hosted"
						if state == "local" {
							copy.Local = &StepLocal{}
						}
						got := stepQuestion(apps, &copy, chatPlan, p, nil)
						if got.renderErr != nil {
							t.Fatal(got.renderErr)
						}
						if got.Title == "" || got.Instruction == "" || got.Details == "" {
							t.Fatalf("%s pictures=%v: empty part %+v", state, withPictures, got)
						}
						if state == "download" && (withPictures || image.name == "bf16") {
							fixtures = append(fixtures, setupWordingFixture{fmt.Sprintf("%s/%d/pictures-%v", image.name, count, withPictures), got.Facts, got.Title, got.Instruction, got.Details})
						}
					}
				}
			})
		}
	}
	// Maximum-size target sets also retain the exact included chat grants.
	chat := newChatFake(w).with(w.step()).Owners.Chat
	for _, withPictures := range []bool{false, true} {
		count := 16
		if withPictures {
			count = 15
		}
		var apps []StepApp
		var offers []StepChatOffer
		pick := &StepPick{Title: "Qwen3.6 35B", Family: "qwen3.6-35b-a3b", License: strings.Join(chatPlan.Licenses, ", "), Digest: "sha256:" + chatPlan.Assets[0].Download.SHA256}
		for i := 0; i < count; i++ {
			app := StepApp{Name: fmt.Sprintf("target-%02d", i), Title: fmt.Sprintf("Target %02d", i), Use: UseCode, Program: fmt.Sprintf("C:/Apps/target-%02d.exe", i)}
			offer, err := chat.Offer(context.Background(), app, *pick)
			if err != nil {
				t.Fatal(err)
			}
			apps = append(apps, app)
			offers = append(offers, offer)
		}
		var p *stepPictureWork
		if withPictures {
			p = &stepPictureWork{app: StepApp{Name: "comfyui", Title: "ComfyUI", Use: UseImage}, plan: ApprovedBF16Plan(), offer: picture.offer}
		}
		got := stepQuestion(apps, pick, chatPlan, p, offers)
		if got.renderErr != nil {
			t.Fatal(got.renderErr)
		}
		for _, offer := range offers {
			if !strings.Contains(got.Details, offer.Permission) {
				t.Fatalf("details omit the included chat grant %q", offer.Permission)
			}
		}
		fixtures = append(fixtures, setupWordingFixture{fmt.Sprintf("chat-grants/16/pictures-%v", withPictures), got.Facts, got.Title, got.Instruction, got.Details})
	}
	path := "testdata/setup_question_wording.json"
	if *updateSetupWording {
		raw, err := json.MarshalIndent(fixtures, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var retained []setupWordingFixture
	if err := json.Unmarshal(raw, &retained); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixtures, retained) {
		t.Fatal("shared native-route fixture differs from the catalogue's Setup wording; review the change and update it with -update-setup-wording")
	}
}
