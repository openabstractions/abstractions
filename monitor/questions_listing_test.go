package main

import (
	"context"
	"path/filepath"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-asks/go/typedpermission"
)

// The Permissions page reads every page of the question book on each pass
// (architect ruling 2026-11-09, second block): an answered question's
// content is read once, a waiting question's every time.
func TestListedQuestionContentReadsAnsweredOnce(t *testing.T) {
	saved := questionContentRead
	t.Cleanup(func() { questionContentRead = saved })
	reads := map[string]int{}
	questionContentRead = func(_ context.Context, _ typedpermission.DetailsReader, _ string, id string) (askclient.QuestionContent, error) {
		reads[id]++
		return askclient.QuestionContent{}, nil
	}
	waiting := askclient.RecordMetadata{ID: "waiting-" + t.Name()}
	answered := askclient.RecordMetadata{ID: "answered-" + t.Name(), Option: "allow", Answered: "2026-11-09T10:00:00Z"}
	for pass := 0; pass < 3; pass++ {
		for _, record := range []askclient.RecordMetadata{waiting, answered} {
			if _, err := readListedQuestionContent(context.Background(), nil, "alice", record); err != nil {
				t.Fatal(err)
			}
		}
	}
	if reads[waiting.ID] != 3 || reads[answered.ID] != 1 {
		t.Fatalf("reads %v: a waiting question is read every pass, an answered one once", reads)
	}
	// Another answer to the same occurrence is another read.
	answered.Option = "refuse"
	if _, err := readListedQuestionContent(context.Background(), nil, "alice", answered); err != nil || reads[answered.ID] != 2 {
		t.Fatalf("a changed answer read %d times", reads[answered.ID])
	}
}

// A connection update's line leaves out the request code its catalogue
// sentence ends in.
func TestAgentUpdateLineDropsTheRequestCode(t *testing.T) {
	text := "Update the Claude integration connections? Keep these updated connections as your assistants. Each access permission is reviewed separately. Restart Claude integration to finish. Request 0e6867bdcd6e7a461a2822a921bcd724c8ebba352059866f."
	want := "Update the Claude integration connections? Keep these updated connections as your assistants. Each access permission is reviewed separately. Restart Claude integration to finish."
	if got := questionRequestCode.ReplaceAllString(text, "."); got != want {
		t.Fatalf("line %q", got)
	}
}

// A self-registration whose title is the program's path names the program
// by its stem alone.
func TestSelfRegisteredPathTitleReadsByStem(t *testing.T) {
	program := filepath.Join(t.TempDir(), "modelhostd.exe")
	record := selfRegisteredRecord(t, program, program)
	content := &askclient.CatalogueContent{Slots: map[string]string{"program": program, "title": program}}
	if _, sentence, ok := selfRegisteredWords(record, "", content); !ok || sentence != "modelhostd was added to OpenAbstractions. Keep it?" {
		t.Fatalf("sentence %q ok %v", sentence, ok)
	}
	if record.Key != asks.SelfRegisteredKey {
		t.Fatal("fixture key")
	}
}
