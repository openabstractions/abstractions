package gateway

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
		if !strings.Contains(err.Error(), "send hosting local with no credential") {
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

// Owner report R20c (2026-11-10): a job naming a size the engine does not make
// was accepted and failed at status. A size of another shape is refused at
// submit with its allowed values, before any runtime call; an absent size is
// auto, and the schema's description states both. The schema carries no
// default a client would fill in beside a handle (R35).
func TestJobSizeIsCheckedAtSubmitAndDefaultsToAuto(t *testing.T) {
	o := &OA{}
	for _, size := range []string{"1024", "big", "1024 x 1024", "0x512", "512x512x2"} {
		_, err := o.Submit(context.Background(), JobSubmitInput{RequestKey: "k1", Profile: "image_batch", Model: "flux", Prompt: "a fox", Hosting: "local", Size: size})
		if err == nil || !strings.Contains(err.Error(), "size must be auto or WIDTHxHEIGHT such as 512x512; leave it out for auto") {
			t.Fatalf("%q: %v", size, err)
		}
	}
	for _, size := range []string{"", "auto", "512x512", "1024x768"} {
		if err := jobSize(JobSubmitInput{Size: size}); err != nil {
			t.Fatalf("%q refused: %v", size, err)
		}
	}
	document, err := jobwire.Decode(jobSpec(JobSubmitInput{Profile: "image_batch", Model: "flux", Prompt: "a fox", Hosting: "local", Count: 1}))
	if err != nil || document.Request == nil || document.Request.Image.Size != "auto" {
		t.Fatalf("an absent size is not auto: %v %+v", err, document)
	}
	size := imageSchema().Properties["size"]
	if len(size.Default) != 0 || size.Pattern != "" || !strings.HasPrefix(size.Description, "auto (default)") {
		t.Fatalf("size schema %+v", size)
	}
	if slices.Contains(imageSchema().Required, "size") {
		t.Fatal("size is required")
	}
	// The job tools carry video only: count and size moved with pictures.
	for _, moved := range []string{"count", "size"} {
		if _, ok := jobSubmitSchema().Properties[moved]; ok {
			t.Fatalf("job submit still offers %s", moved)
		}
	}
	// The profile names video alone. It has no enum: the handler answers a
	// picture with its sentence, which a schema check would replace.
	if profile := jobSubmitSchema().Properties["profile"]; len(profile.Enum) != 0 || profile.Description != "video" {
		t.Fatalf("job submit profile %+v", profile)
	}
}

// PICTURES-STORAGE amendments (coordinator, 2026-11-10): a picture sent to the job tools is refused for good with one
// sentence, before the backend sees it.
func TestJobSubmitRefusesAPicture(t *testing.T) {
	b := &fixtureBackend{principal: "a", ledger: &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}}
	_, cs := connected(t, b, "picture-client", "2026-07-28")
	result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolJobSubmit, Arguments: map[string]any{
		"request_key": "k1", "profile": "image_batch", "model": "flux", "prompt": "a fox", "hosting": "local"}})
	if err != nil || !result.IsError || len(result.Content) == 0 {
		t.Fatal(result, err)
	}
	text, _ := result.Content[0].(*mcp.TextContent)
	if text == nil || text.Text != "Pictures use oa_image_generate." {
		t.Fatalf("%+v", result.Content[0])
	}
	if b.providerEffects != 0 {
		t.Fatal("a picture reached the backend")
	}
}
