package setup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentUpdateRetryRecordNamesItsIntents(t *testing.T) {
	path := filepath.Join(t.TempDir(), agentUpdateRetryName)
	if err := recordAgentUpdateRetryAt(path, []string{"one", "two"}, "a connection changed since the question", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if reason, ok := readAgentUpdateRetryAt(path, "two"); !ok || reason != "a connection changed since the question" {
		t.Fatal(reason, ok)
	}
	if _, ok := readAgentUpdateRetryAt(path, "three"); ok {
		t.Fatal("another intent read the stop")
	}
	if err := recordAgentUpdateRetryAt(path, nil, "", time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("clearing left the record", err)
	}
	if err := recordAgentUpdateRetryAt(path, nil, "", time.Unix(3, 0)); err != nil {
		t.Fatal("clearing an absent record", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	// atomic-file keeps its lock beside the record; nothing else may remain.
	if err != nil || len(entries) > 1 || len(entries) == 1 && entries[0].Name() != filepath.Base(path)+".lock" {
		t.Fatal("temporary files left", entries, err)
	}
}
