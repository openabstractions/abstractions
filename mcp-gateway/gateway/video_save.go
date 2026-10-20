package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

// maxVideoBytes bounds a saved video: the video engine's container bound.
const maxVideoBytes = 64 << 20

// videoTypes are the media types a video may be, with their extensions.
var videoTypes = map[string]string{"video/webm": ".webm", "video/x-msvideo": ".avi", "video/avi": ".avi"}

// videos is the person's Videos folder from storage layout.
func (o *OA) videos() (string, error) {
	if o.videosAt != nil {
		return o.videosAt()
	}
	return layout.ResolvePerson(layout.Current(), layout.Videos)
}

// saveVideo saves a finished video job's clip as
// <Videos>/OpenAbstractions/video-<local time><ext> under the person's
// account, as oa_image_generate saves a picture, and answers its path. A
// repeated status read answers the file saved before while it still holds
// the clip. A clip that cannot be saved answers no path and one sentence
// saying why; the result still carries its digest.
func (o *OA) saveVideo(ctx context.Context, handle string, result *jobwire.JobResult) (string, string) {
	if len(result.Deliveries) != 1 {
		return "", "Not saved: the video job delivered no single clip."
	}
	delivery := result.Deliveries[0]
	ext, known := videoTypes[delivery.MediaType]
	if delivery.Location != "local" {
		// A result another trust domain holds is read through the job's
		// content read; it never gets a path on this computer.
		return "", "Not saved here: the clip is held elsewhere; read it by its digest through the job's content."
	}
	if !known || delivery.Size < 1 || delivery.Size > maxVideoBytes || !validDigest(delivery.Digest) {
		return "", "Not saved: the clip is not a WebM or AVI video of at most 64 MiB."
	}
	folder, err := o.videos()
	if err != nil {
		return "", fmt.Sprintf("Not saved: this account has no Videos folder (%s).", layout.ReasonOf(err))
	}
	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		return "", fmt.Sprintf("Not saved: the Videos folder %s does not exist.", folder)
	}
	record, recorded := o.savedVideoPath(handle)
	read := o.readPicture
	if o.readClipAt != nil {
		read = o.readClipAt
	}
	// One save at a time per handle: in this process through the lock, and
	// against another gateway process through the claim file beside the
	// handle record.
	unlock := o.videoWatch.lock(handle)
	defer unlock()
	if recorded {
		if path, ok := savedVideo(folder, record, delivery.Digest); ok {
			return path, ""
		}
		release, claimed := claimSave(record)
		if !claimed {
			return "", "The clip is being saved; read the status again in a moment."
		}
		defer release()
	}
	return saveClip(ctx, folder, record, recorded, delivery, ext, read)
}

// saveClaimStale is how old a save claim is before another process takes it
// over: a gateway that died mid-save leaves its claim behind.
const saveClaimStale = 2 * time.Minute

// claimSave takes the save claim beside a handle's saved-video record, or
// reports that another process holds a fresh one.
func claimSave(record string) (func(), bool) {
	claim := strings.TrimSuffix(record, ".json") + ".saving"
	for range 2 {
		file, err := os.OpenFile(claim, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			//unchecked: the claim's existence is the lock; its close carries no data
			_ = file.Close()
			//unchecked: a claim left behind goes stale and is taken over
			return func() { _ = os.Remove(claim) }, true
		}
		info, statErr := os.Stat(claim)
		if statErr != nil || time.Since(info.ModTime()) < saveClaimStale {
			return nil, false
		}
		//unchecked: a stale claim is replaced; a failed remove refuses below
		_ = os.Remove(claim)
	}
	return nil, false
}

// saveClip saves one checked clip delivery into folder, reading its bytes
// with read, and records it at record when recorded.
func saveClip(ctx context.Context, folder, record string, recorded bool, delivery jobwire.Delivery, ext string,
	read func(context.Context, jobwire.Delivery) ([]byte, error)) (string, string) {
	if recorded {
		if path, ok := savedVideo(folder, record, delivery.Digest); ok {
			return path, ""
		}
	}
	data, err := read(ctx, delivery)
	if err != nil {
		return "", fmt.Sprintf("Not saved: the clip could not be read (%v).", err)
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != delivery.Size || "sha256:"+hex.EncodeToString(sum[:]) != delivery.Digest {
		return "", "Not saved: the clip's bytes do not match its digest."
	}
	path, refused := saveMedia(folder, "Videos", "video-", data, ext, time.Now())
	if refused != "" {
		return "", refused
	}
	note := ""
	if ext == ".webm" && !webmHasAudio(data) {
		// A clip whose sound was not muxed is still the person's video.
		note = "This clip has no sound: the engine wrote no audio track."
	}
	if recorded {
		//unchecked: savedRecord holds two strings, which json.Marshal cannot fail on
		raw, _ := json.Marshal(savedRecord{Name: filepath.Base(path), Digest: delivery.Digest})
		//unchecked: the record only spares a second copy; the saved file is the answer either way
		_ = os.WriteFile(record, raw, 0o600)
	}
	return path, note
}

// webmHasAudio reports whether a WebM names an audio codec: a CodecID
// element (0x86) whose short string starts "A_", such as A_PCM/INT/LIT,
// A_OPUS or A_VORBIS, within the header's first 64 KiB.
func webmHasAudio(data []byte) bool {
	head := data[:min(len(data), 64<<10)]
	for i := 0; i+3 < len(head); i++ {
		if head[i] == 0x86 && head[i+1]&0x80 != 0 && head[i+1]&0x7f >= 2 && head[i+2] == 'A' && head[i+3] == '_' {
			return true
		}
	}
	return false
}

// savedVideoPath is where a handle's saved video is recorded, beside its
// handle record.
func (o *OA) savedVideoPath(handle string) (string, bool) {
	record, err := o.recordPath(handle)
	if err != nil {
		return "", false
	}
	return strings.TrimSuffix(record, ".json") + ".video.json", true
}

// savedVideo answers the file a record names when it is a bare name in the
// Videos folder's OpenAbstractions folder and still holds digest.
func savedVideo(folder, record, digest string) (string, bool) {
	raw, err := readBounded(record, maxRequestRecordBytes)
	var saved savedRecord
	if err != nil || json.Unmarshal(raw, &saved) != nil || saved.Digest != digest || fileid.SafeName(saved.Name) != nil {
		return "", false
	}
	root, err := os.OpenRoot(folder)
	if err != nil {
		return "", false
	}
	defer root.Close()
	file, err := root.Open(pictureFolderName + "/" + saved.Name)
	if err != nil {
		return "", false
	}
	defer file.Close()
	h := sha256.New()
	if n, err := io.Copy(h, io.LimitReader(file, maxVideoBytes+1)); err != nil || n > maxVideoBytes {
		return "", false
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != digest {
		return "", false
	}
	return filepath.Join(folder, pictureFolderName, saved.Name), true
}
