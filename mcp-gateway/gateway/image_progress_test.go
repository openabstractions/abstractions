package gateway

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	casapi "github.com/openabstractions/abstraction-cas/go/api"
)

// The working answer counts the seconds since the picture's job was
// submitted, read from the handle record OA wrote then, so a call that
// resumes the handle reports the job's whole time, not its own wait.
func TestPictureProgressCountsFromTheJobsSubmission(t *testing.T) {
	o := &OA{stateDir: t.TempDir(), state: casapi.BoundedFileStore{MaxBytes: maxHandleRecordBytes}}
	handle := strings.Repeat("A", 32)
	if err := o.save(t.Context(), handle, handleRecord{Version: handleRecordVersion, Schema: jobSchema}); err != nil {
		t.Fatal(err)
	}
	path, err := o.recordPath(handle)
	if err != nil {
		t.Fatal(err)
	}
	submitted := time.Now().Add(-75 * time.Second)
	if err := os.Chtimes(path, submitted, submitted); err != nil {
		t.Fatal(err)
	}
	steps := pictureSteps{
		status: func(context.Context, string) (JobStatusOutput, error) {
			return JobStatusOutput{Outcome: "observed", State: "running", Progress: map[string]int64{"done": 5, "total": 9}}, nil
		},
		started: o.handleStarted,
	}
	out, err := followPicture(t.Context(), steps, t.TempDir(), handle, "auto", 0)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "Making the picture: step 5 of 9, 75 s so far. To wait for this picture, call oa_image_generate with "
	next, hasPrefix := strings.CutPrefix(out.Message, prefix)
	next, hasSuffix := strings.CutSuffix(next, ".")
	var args map[string]string
	if out.Outcome != "working" || out.Handle != handle || !hasPrefix || !hasSuffix ||
		json.Unmarshal([]byte(next), &args) != nil || len(args) != 1 || args["handle"] != handle {
		t.Fatalf("answer %+v", out)
	}
	// A handle without a record counts from this call.
	other := strings.Repeat("B", 32)
	out, err = followPicture(t.Context(), steps, t.TempDir(), other, "auto", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.Message, "Making the picture: step 5 of 9, 0 s so far.") {
		t.Fatalf("answer %q", out.Message)
	}
}
