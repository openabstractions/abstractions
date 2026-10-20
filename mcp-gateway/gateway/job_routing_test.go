package gateway

import (
	"context"
	"strings"
	"testing"

	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"
)

// A local job runs on this computer's servers under the local-only guarantee
// with no credential; a hosted job carries its credential. Models no longer
// guess credential names for local work (demo-model run, 2026-10-02).
func TestJobRequestRoutesLocalWithoutCredentialAndHostedWithIt(t *testing.T) {
	local := jobRequest(JobSubmitInput{Profile: "video", Model: "ltx", Prompt: "a fox", Hosting: "local", DurationMS: 4000, Count: 1})
	if local.Profile != jobwire.ProfileVideo || local.Image.Credential != "" || len(local.Image.Guarantees) != 1 ||
		local.Image.Guarantees[0] != jobwire.RequestGuaranteeLocalOnly || local.DurationMs != 4000 || local.Image.Mode != jobwire.ModeGenerate {
		t.Fatalf("local request %+v", local)
	}
	hosted := jobRequest(JobSubmitInput{Profile: "image_batch", Model: "flux", Prompt: "a fox", Hosting: "hosted", Credential: "fal", Count: 2})
	if hosted.Profile != jobwire.ProfileImageBatch || hosted.Image.Credential != "fal" || len(hosted.Image.Guarantees) != 1 ||
		hosted.Image.Guarantees[0] != jobwire.RequestGuaranteeHostedAllowed || hosted.Image.Count != 2 {
		t.Fatalf("hosted request %+v", hosted)
	}
}

// Every refused routing is terminal and says what to send instead, before any
// runtime call.
func TestJobRoutingRefusalsSayWhatToSend(t *testing.T) {
	o := &OA{}
	base := JobSubmitInput{RequestKey: "k1", Profile: "image_batch", Model: "flux", Prompt: "a fox"}
	for _, c := range []struct {
		hosting, credential, want string
	}{
		{"local", "guessed-key", "local work needs no credential"},
		{"hosted", "", "hosted work needs the name of a credential"},
		{"hosted", "not a name!", "hosted work needs the name of a credential"},
		{"", "", "hosting must be local"},
		{"cloud", "fal", "hosting must be local"},
	} {
		in := base
		in.Hosting, in.Credential = c.hosting, c.credential
		_, err := o.Submit(context.Background(), in)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "refused for good") {
			t.Fatalf("%s/%q: %v", c.hosting, c.credential, err)
		}
		if c.hosting != "local" && c.hosting != "hosted" {
			continue
		}
		if !strings.Contains(err.Error(), "openabstractions_generate_image") {
			t.Fatalf("%s/%q names no way forward: %v", c.hosting, c.credential, err)
		}
	}
	if err := jobRouting(JobSubmitInput{Hosting: "local"}); err != nil {
		t.Fatalf("local with no credential: %v", err)
	}
	if err := jobRouting(JobSubmitInput{Hosting: "hosted", Credential: "fal"}); err != nil {
		t.Fatalf("hosted with a credential: %v", err)
	}
}

// The submitted document decodes to the local-only request with no
// credential, as the inference job executor reads it.
func TestJobSpecCarriesTheLocalRequest(t *testing.T) {
	document, err := jobwire.Decode(jobSpec(JobSubmitInput{Profile: "image_batch", Model: "flux", Prompt: "a fox", Hosting: "local", Size: "512x512", Count: 1}))
	if err != nil || document.Request == nil {
		t.Fatalf("decode: %v %+v", err, document)
	}
	image := document.Request.Image
	if image.Model != "flux" || image.Prompt != "a fox" || image.Size != "512x512" || image.Credential != "" ||
		len(image.Guarantees) != 1 || image.Guarantees[0] != jobwire.RequestGuaranteeLocalOnly {
		t.Fatalf("local job document %+v", image)
	}
}
