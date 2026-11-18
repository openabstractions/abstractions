package setup

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	modelhost "github.com/openabstractions/abstraction-provider-modelhost/go/host"
)

// The reviewed LTX-2.5 set on the reviewed Windows engine, as the step's
// production owners read them.
func ltxStepVideo(t *testing.T) StepVideo {
	t.Helper()
	set, ok := modelhost.VideoModelSetNamed("ltx-2.5")
	if !ok {
		t.Fatal("ltx-2.5 is not a reviewed set")
	}
	engine, ok := modelhost.PinnedVideoEngine("windows", "amd64")
	if !ok {
		t.Fatal("no reviewed engine")
	}
	return StepVideo{Set: set, Engine: engine}
}

// The question for an app's request for video states the ruled heading,
// body and details from the set's own data (architect ruling 2026-11-11).
func TestVideoQuestionWordsLTX25(t *testing.T) {
	q, err := videoQuestion("OpenCode", ltxStepVideo(t))
	if err != nil {
		t.Fatal(err)
	}
	if q.Title != "OpenCode asks to set up video with sound on this computer." {
		t.Fatalf("heading %q", q.Title)
	}
	if q.Instruction != "LTX-2.5: about 39 GB to download; 41 GB free needed." {
		t.Fatalf("body %q", q.Instruction)
	}
	want := "Licence: LTX-2 Community License Agreement (https://huggingface.co/Lightricks/LTX-2.5/blob/5e6e71018ee1756ed329b697a7b4aedc934dfce9/README.md). " +
		"Sources on Hugging Face: vantagewithai/LTX-2.5-GGUF at revision 9a5c1be and Lightricks/LTX-2.5 at revision 5e6e710. " +
		"Making a video needs about 53 GB of memory. " +
		"Quick: 512x512, about 3 min. Quality: 1280x704 in two stages, about 15 min."
	if q.Details != want {
		t.Fatalf("details\n%q\nwant\n%q", q.Details, want)
	}
	// The retained facts render the same words wherever the question is
	// shown again.
	again, err := asks.SetupQuestionPresentation(q.Facts)
	if err != nil || again.Title != q.Title || again.Instruction != q.Instruction || again.Details != q.Details {
		t.Fatal(again, err)
	}
	facts, err := asks.SetupFactsFromSlots(q.Facts)
	if err != nil || facts.RequestUse != UseVideo || facts.Requester != "OpenCode" || facts.Video == nil || facts.Video.Model != "ltx-2.5" || len(facts.Targets) != 0 ||
		!strings.HasPrefix(facts.Video.LicenseURL, "https://huggingface.co/Lightricks/") {
		t.Fatal(facts, err)
	}
	for _, words := range []string{q.Title, q.Instruction, q.Details} {
		for _, banned := range []string{"192.168", "AI Models", "OA-evidence", `\\`} {
			if strings.Contains(words, banned) {
				t.Fatalf("%q names %q", words, banned)
			}
		}
	}
}

type videoRun struct {
	asked    []StepQuestion
	notices  []StepNotice
	gets     int
	yes      bool
	pool     int64
	served   bool
	getError error
}

func (r *videoRun) step(t *testing.T) SetupStep {
	v := ltxStepVideo(t)
	v.Served = func(context.Context) (bool, error) { return r.served, nil }
	v.Get = func(context.Context) error { r.gets++; return r.getError }
	return SetupStep{JournalPath: filepath.Join(t.TempDir(), "step.json"),
		Requester: &StepRequester{App: "OpenCode", Use: UseVideo, Request: "r1"},
		Owners: StepOwners{
			Fit: func(context.Context, string) (StepFit, error) { return StepFit{PoolBytes: r.pool}, nil },
			Ask: func(_ context.Context, q StepQuestion) (bool, error) { r.asked = append(r.asked, q); return r.yes, nil },
			Notify: func(_ context.Context, n StepNotice) error {
				r.notices = append(r.notices, n)
				return nil
			},
			Video: &v,
		}}
}

func TestVideoRequestOutcomes(t *testing.T) {
	ctx := context.Background()

	small := &videoRun{pool: 32 << 30, yes: true}
	outcome, err := small.step(t).RunAndReport(ctx)
	if err != nil || outcome != stepCannotRun || len(small.asked) != 0 || small.gets != 0 || len(small.notices) != 1 ||
		small.notices[0].Body != "This computer has 32 GB free for models; LTX-2.5 needs 53 GB." {
		t.Fatal("small pool", outcome, err, small)
	}

	served := &videoRun{pool: 96 << 30, served: true, yes: true}
	outcome, err = served.step(t).RunAndReport(ctx)
	if err != nil || outcome != stepAlreadySetUp || len(served.asked) != 0 || served.gets != 0 {
		t.Fatal("served", outcome, err, served)
	}

	declined := &videoRun{pool: 96 << 30}
	outcome, err = declined.step(t).RunAndReport(ctx)
	if err != nil || outcome != stepAnsweredNotNow || len(declined.asked) != 1 || declined.gets != 0 || len(declined.notices) != 0 {
		t.Fatal("not now", outcome, err, declined)
	}

	approved := &videoRun{pool: 96 << 30, yes: true}
	outcome, err = approved.step(t).RunAndReport(ctx)
	if err != nil || outcome != stepSetUp || len(approved.asked) != 1 || approved.gets != 1 || len(approved.notices) != 2 ||
		approved.notices[1].Title != "Video with sound is ready" {
		t.Fatal("set up", outcome, err, approved)
	}

	failed := &videoRun{pool: 96 << 30, yes: true, getError: errors.New("download stopped")}
	step := failed.step(t)
	outcome, err = step.RunAndReport(ctx)
	if !errors.Is(err, errStepVideo) || stageOf(err) != stageVideo || failed.gets != 1 {
		t.Fatal("failed", outcome, err, failed)
	}
	last := failed.notices[len(failed.notices)-1]
	if last.Title != "Couldn't finish setting up OpenCode video" || last.Body != "Video with sound could not be set up. OpenCode can ask again." {
		t.Fatalf("failure notice %+v", last)
	}
	// The coding-model journal records nothing of a video request.
	if state, err := step.Load(ctx); err != nil || state.Stopped != nil || state.Progress != "" {
		t.Fatal("journal written", state, err)
	}

	none := &videoRun{pool: 96 << 30, yes: true}
	s := none.step(t)
	s.Owners.Video = nil
	if outcome, err := s.RunAndReport(ctx); err != nil || outcome != stepCannotRun || len(none.asked) != 0 {
		t.Fatal("no video part", outcome, err)
	}
}
