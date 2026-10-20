package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentUpdateBatchRetainedSourceRequiresExactKeyAndKeep(t *testing.T) {
	s := batchImageFixture(t)
	path := filepath.Join(filepath.Dir(s.Old.Program()), "retained.previous")
	if err := os.WriteFile(path, []byte("older retained bytes; never executed"), 0600); err != nil {
		t.Fatal(err)
	}
	image, err := holdAgentImageNamedForReview(t.Context(), filepath.Dir(path), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	verify := func(context.Context, string, string) error { return nil }
	if _, err := retainedHeldAgentUpdate(t.Context(), s.Old, s.Source, image, "foreign-key", verify); err == nil {
		t.Fatal("foreign prior key admitted")
	}
	if _, err := retainedHeldAgentUpdate(t.Context(), s.Old, s.Source, image, image.FileKey(), func(context.Context, string, string) error { return errors.New("revoked") }); err == nil {
		t.Fatal("revoked Keep admitted")
	}
	retained, err := retainedHeldAgentUpdate(t.Context(), s.Old, s.Source, image, image.FileKey(), verify)
	if err != nil || retained.Image != image || retained.imagePin.Hash == retained.sourcePin.Hash {
		t.Fatal("exact retained generation unavailable", err)
	}
}
