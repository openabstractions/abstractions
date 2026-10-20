package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	storageclient "github.com/openabstractions/abstraction-storage/go/client"
)

type recordingWriter struct {
	request, digest string
	data            []byte
}

func (w *recordingWriter) Write(_ context.Context, request, digest string, content io.ReaderAt, size int64) (storageclient.Stored, error) {
	w.request, w.digest = request, digest
	w.data = make([]byte, size)
	_, err := content.ReadAt(w.data, 0)
	return storageclient.Stored{}, err
}

// Architect ruling 2026-11-12: oa_inference_complete takes reasoning_effort
// and reasoning_budget and maps them onto chat@3 ChatOptions.reasoning; an
// image file it is given is stored in the caller's content store and sent
// as a chat@3 image part. The call runs on chat@3 without include_reasoning,
// under a fresh identity, its handle resumes on chat@3, and the answer
// reports reasoning_applied.
func TestCompleteSendsReasoningAndAnImageOnChat3(t *testing.T) {
	picture := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := range 8 {
		for y := range 8 {
			picture.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "red.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded.Bytes())
	digest := "sha256:" + hex.EncodeToString(sum[:])

	o := chat3CompletionOwner(t)
	stored := &recordingWriter{}
	o.imageWriter = func(context.Context) (contentWriter, error) { return stored, nil }
	page := chat3CompletionPage(0)
	low := wire.ChatReasoningEffortLow
	budget := int64(512)
	page.Events[2].End.ReasoningApplied = &wire.ChatReasoningApplied{Effort: &low, Budget: &budget, Support: wire.ChatReasoningSupportApplied}
	f := &chat3CompleteFixture{epoch: "epoch", admission: wire.ChatAdmission{Outcome: wire.ChatStartOutcomeAccepted, Request: "operation"}, pages: []wire.ChatPage{page}}
	in := CompleteInput{Model: "gemma", Prompt: "What colour is this picture?", Hosting: "local", ReasoningEffort: "low", ReasoningBudget: 512, ImagePath: path}
	if !completesOnChat3(in) {
		t.Fatal("reasoning and image call stayed on chat@2")
	}
	out, err := o.completeWithChat3(context.Background(), f, in)
	if err != nil || out.Text != "answer" || out.OperationID != chat3Handle+"operation" {
		t.Fatalf("completion %+v %v", out, err)
	}
	if a := out.ReasoningApplied; a == nil || a.Effort != "low" || a.Budget != 512 || a.Support != "applied" {
		t.Fatalf("reasoning_applied %+v", out.ReasoningApplied)
	}
	if len(f.starts) != 1 {
		t.Fatalf("starts %d", len(f.starts))
	}
	request := f.starts[0]
	if !strings.HasPrefix(request.Identity.Key, "complete-") || request.Identity.HistoryEpoch != "epoch" {
		t.Fatalf("identity %+v", request.Identity)
	}
	options := request.Content.Options
	if options == nil || options.Reasoning == nil || options.Reasoning.Effort != wire.ChatReasoningEffortLow || options.Reasoning.Budget == nil || *options.Reasoning.Budget != 512 {
		t.Fatalf("options %+v", options)
	}
	parts := request.Content.Messages[0].Parts
	if len(parts) != 2 || parts[1].Kind != wire.PartKindImage || parts[1].Digest != digest || parts[1].MediaType != "image/png" {
		t.Fatalf("parts %+v", parts)
	}
	if stored.digest != digest || !bytes.Equal(stored.data, encoded.Bytes()) {
		t.Fatalf("stored %s (%d bytes)", stored.digest, len(stored.data))
	}
	// The handle resumes on chat@3 and keeps its mark.
	f.pages = []wire.ChatPage{chat3CompletionPage(0)}
	cursor := int64(0)
	resumed, err := o.completeWithChat3(context.Background(), f, CompleteInput{OperationID: out.OperationID, Cursor: &cursor})
	if err != nil || resumed.OperationID != out.OperationID || resumed.Text != "answer" {
		t.Fatalf("resume %+v %v", resumed, err)
	}
	if _, err := chat3Reasoning(CompleteInput{ReasoningEffort: "max"}); err == nil {
		t.Fatal("an effort outside none, low, medium and high was taken")
	}
}

// Architect ruling 2026-11-12, model capabilities: every entry of
// oa_models_list has one line per alias in one format, prefixed by its
// host. A local and a hosted alias of one family are two lines, and a
// capability only one host has appears only on its line.
func TestModelsListShowsEachAliasCapabilitiesOnItsOwnLine(t *testing.T) {
	yes, no := true, false
	medium, high := routerwire.ReasoningEffortMedium, routerwire.ReasoningEffortHigh
	efforts := []routerwire.ReasoningEffort{"none", "low", "medium", "high"}
	models := modelsFrom([]routerwire.Family{{Family: "flash", Names: []routerwire.Alias{
		{Host: "modelhost", Name: "flash", Servable: true, Profiles: []string{"chat"}, ContextLength: 262144, EffectiveContextLength: 32768,
			Capabilities: &routerwire.ModelCapabilities{Input: []string{"text", "image"}, Output: []string{"text"}, Tools: &yes,
				Reasoning: &routerwire.ReasoningSupport{Efforts: efforts, Budget: true, DefaultEffort: &medium}, Extensions: []string{"llama.cpp/enable_thinking"}}},
		{Host: "deepseek", Hosted: true, Name: "flash", Servable: true, Profiles: []string{"chat"}, ContextLength: 1048576,
			Capabilities: &routerwire.ModelCapabilities{Input: []string{"text"}, Output: []string{"text"}, Tools: &yes, StructuredOutput: &no,
				Reasoning: &routerwire.ReasoningSupport{Efforts: efforts, DefaultEffort: &high}}},
	}}})
	if len(models) != 1 || len(models[0].Capabilities) != 2 {
		t.Fatalf("models %+v", models)
	}
	hosted, local := models[0].Capabilities[0], models[0].Capabilities[1]
	if hosted != "deepseek flash: chat · in text · tools · 1M · reasoning none, low, medium, high (default high)" {
		t.Fatalf("hosted line %q", hosted)
	}
	if local != "local flash: chat · in text+image · tools · 256k (served 32k) · reasoning none, low, medium, high (budget, default medium) · ext llama.cpp/enable_thinking" {
		t.Fatalf("local line %q", local)
	}
}

// image_path reads only a regular file the calling agent names by an
// absolute path: a relative path, a folder and a Windows device or pipe name
// are refused before anything is stored or started. A local image call
// carries only the local guarantee, so no hosted model sees the image.
func TestCompleteImagePathReadsOnlyARegularFile(t *testing.T) {
	o := chat3CompletionOwner(t)
	stored := &recordingWriter{}
	o.imageWriter = func(context.Context) (contentWriter, error) { return stored, nil }
	f := &chat3CompleteFixture{epoch: "epoch", admission: wire.ChatAdmission{Outcome: wire.ChatStartOutcomeAccepted, Request: "operation"}, pages: []wire.ChatPage{chat3CompletionPage(0)}}
	for _, path := range []string{"red.png", t.TempDir(), `\.\pipe\openabstractions-runtime`, `\?\C:\Windows\win.ini`, `//./CONIN$`} {
		in := CompleteInput{Model: "gemma", Prompt: "p", Hosting: "local", ImagePath: path}
		if _, err := o.completeWithChat3(context.Background(), f, in); err == nil {
			t.Fatalf("image_path %q was read", path)
		}
	}
	if stored.digest != "" || len(f.starts) != 0 {
		t.Fatalf("a refused image_path stored %q or started %d", stored.digest, len(f.starts))
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "p.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := o.completeWithChat3(context.Background(), f, CompleteInput{Model: "gemma", Prompt: "p", Hosting: "local", ImagePath: path}); err != nil {
		t.Fatal(err)
	}
	if len(f.starts) != 1 || len(f.starts[0].Content.Guarantees) != 1 || f.starts[0].Content.Guarantees[0] != wire.ChatGuaranteeLocalOnly || f.starts[0].Content.Credential != "" {
		t.Fatalf("local image call guarantees %+v", f.starts)
	}
}
