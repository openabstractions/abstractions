package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"
)

// A finished video is saved once in the person's Videos folder under
// OpenAbstractions and its path answered; a repeated status read answers
// the same file without reading the clip again, and bytes that differ from
// the digest are never saved.
func TestFinishedVideoIsSavedOnceAndItsPathAnswered(t *testing.T) {
	folder := t.TempDir()
	record := filepath.Join(t.TempDir(), "handle.video.json")
	// An EBML header, a track whose CodecID (0x86) is A_PCM/INT/LIT, frames.
	clip := append([]byte{0x1A, 0x45, 0xDF, 0xA3, 0x86, 0x8D}, []byte("A_PCM/INT/LIT")...)
	clip = append(clip, bytes.Repeat([]byte("clip"), 1000)...)
	sum := sha256.Sum256(clip)
	delivery := jobwire.Delivery{Digest: "sha256:" + hex.EncodeToString(sum[:]), MediaType: "video/webm", Size: int64(len(clip)), Location: "local"}
	reads := 0
	read := func(context.Context, jobwire.Delivery) ([]byte, error) { reads++; return clip, nil }
	path, note := saveClip(context.Background(), folder, record, true, delivery, ".webm", read)
	if note != "" || filepath.Dir(path) != filepath.Join(folder, "OpenAbstractions") || !strings.HasPrefix(filepath.Base(path), "video-") || filepath.Ext(path) != ".webm" {
		t.Fatalf("saved %q: %s", path, note)
	}
	if saved, _ := os.ReadFile(path); !bytes.Equal(saved, clip) {
		t.Fatal("the saved file is not the clip")
	}
	again, note := saveClip(context.Background(), folder, record, true, delivery, ".webm", read)
	if again != path || note != "" || reads != 1 {
		t.Fatalf("the second read answered %q (%s) after %d reads", again, note, reads)
	}
	wrong := func(context.Context, jobwire.Delivery) ([]byte, error) {
		return bytes.Repeat([]byte("x"), len(clip)), nil
	}
	if path, note := saveClip(context.Background(), folder, filepath.Join(t.TempDir(), "r.json"), true, delivery, ".webm", wrong); path != "" || note == "" {
		t.Fatalf("differing bytes were saved at %q", path)
	}
}

// A clip whose audio track is missing is still saved, and the answer says
// it has no sound; a clip held in another trust domain gets no local path.
func TestVideoWithoutAudioIsSavedAndRemoteResultGetsNoPath(t *testing.T) {
	folder := t.TempDir()
	clip := append([]byte{0x1A, 0x45, 0xDF, 0xA3, 0x86, 0x85}, []byte("V_VP8")...)
	clip = append(clip, bytes.Repeat([]byte("frame"), 100)...)
	sum := sha256.Sum256(clip)
	delivery := jobwire.Delivery{Digest: "sha256:" + hex.EncodeToString(sum[:]), MediaType: "video/webm", Size: int64(len(clip)), Location: "local"}
	read := func(context.Context, jobwire.Delivery) ([]byte, error) { return clip, nil }
	path, note := saveClip(context.Background(), folder, "", false, delivery, ".webm", read)
	if path == "" || note != "This clip has no sound: the engine wrote no audio track." {
		t.Fatalf("saved %q: %q", path, note)
	}
	o := &OA{videosAt: func() (string, error) { return folder, nil }}
	delivery.Location = "machine:studio"
	path, note = o.saveVideo(context.Background(), "h", &jobwire.JobResult{Profile: jobwire.ProfileVideo, Deliveries: []jobwire.Delivery{delivery}})
	if path != "" || !strings.Contains(note, "content") {
		t.Fatalf("a remote result answered %q: %q", path, note)
	}
}
