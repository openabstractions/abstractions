package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	facade "github.com/openabstractions/abstraction-facade/go/client"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	contentwire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageclient "github.com/openabstractions/abstraction-storage/go/client"
)

// chat3Handle marks an operation handle the gateway started on chat@3 for
// a reasoning choice or an image, so its resume reads chat@3 too.
const chat3Handle = "c3."

// maxCompleteImageBytes bounds an image oa_inference_complete shows the
// model: the inference service's own per-image ceiling.
const maxCompleteImageBytes = 20 << 20

// chatImageTypes are the media types a chat@3 image part takes, as the
// inference service sniffs them.
var chatImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// completesOnChat3 reports whether a call needs chat@3: a reasoning choice,
// an image, or the resume of a call that had one.
func completesOnChat3(in CompleteInput) bool {
	return in.ReasoningEffort != "" || in.ReasoningBudget != 0 || in.Image != "" || in.ImagePath != "" || strings.HasPrefix(in.OperationID, chat3Handle)
}

// chat3Reasoning is the call's reasoning choice as chat@3 options: a budget
// alone caps high effort.
func chat3Reasoning(in CompleteInput) (*wire.ChatReasoning, error) {
	if in.ReasoningEffort == "" && in.ReasoningBudget == 0 {
		return nil, nil
	}
	if in.ReasoningBudget < 0 || in.ReasoningBudget > 1<<20 {
		return nil, errors.New("gateway: reasoning_budget is 1 to 1048576 tokens")
	}
	effort := wire.ChatReasoningEffortHigh
	if in.ReasoningEffort != "" {
		parsed, ok := wire.ParseChatReasoningEffort(in.ReasoningEffort)
		if !ok {
			return nil, errors.New("gateway: reasoning_effort is none, low, medium or high")
		}
		effort = parsed
	}
	r := &wire.ChatReasoning{Effort: effort}
	if in.ReasoningBudget > 0 {
		budget := in.ReasoningBudget
		r.Budget = &budget
	}
	return r, nil
}

// appliedReasoning is the terminal's reasoning_applied as the tool reports it.
func appliedReasoning(a *wire.ChatReasoningApplied) *CompleteReasoningApplied {
	if a == nil {
		return nil
	}
	out := &CompleteReasoningApplied{Support: string(a.Support)}
	if a.Effort != nil {
		out.Effort = a.Effort.String()
	}
	if a.Budget != nil {
		out.Budget = *a.Budget
	}
	if a.BudgetSupport != nil {
		out.BudgetSupport = string(*a.BudgetSupport)
	}
	return out
}

// imagePart is the call's image as a chat@3 input part: an OA digest the
// caller may read, or a file staged into the caller's content store. Nil
// when the call names none.
func (o *OA) imagePart(ctx context.Context, in CompleteInput) (*wire.ChatInputPart, error) {
	switch {
	case in.Image != "" && in.ImagePath != "":
		return nil, errors.New("gateway: name one of image and image_path")
	case in.Image != "":
		if !validDigest(in.Image) {
			return nil, errors.New("gateway: image must be an OA image digest sha256:<64 hex>")
		}
		reader, err := o.chatImageReader(ctx)
		if err != nil {
			return nil, err
		}
		media, err := sniffChatImage(ctx, reader, in.Image)
		if err != nil {
			return nil, err
		}
		return &wire.ChatInputPart{Kind: wire.PartKindImage, Digest: in.Image, MediaType: media}, nil
	case in.ImagePath != "":
		data, media, err := readChatImage(in.ImagePath)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		writer, err := o.chatImageWriter(ctx)
		if err != nil {
			return nil, reportImageStagingError("resolve", err)
		}
		key := make([]byte, 18)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if _, err := writer.Write(ctx, "chat-image-"+base64.RawURLEncoding.EncodeToString(key), digest, bytes.NewReader(data), int64(len(data))); err != nil {
			return nil, reportImageStagingError("write", err)
		}
		return &wire.ChatInputPart{Kind: wire.PartKindImage, Digest: digest, MediaType: media}, nil
	}
	return nil, nil
}

// imageStagingError identifies a storage failure before inference Start.
// The file path, digest and unclassified transport error stay out of diagnostics.
type imageStagingError struct{ reason string }

func (e *imageStagingError) Error() string { return e.reason }

func reportImageStagingError(stage string, err error) error {
	reason := "image:staging:" + stage
	var outcome *storageclient.OutcomeError
	if errors.As(err, &outcome) {
		reason += ":" + outcome.Operation + ":" + outcome.Outcome
	}
	log.Printf("gateway: could not stage image (%s)", reason)
	return &imageStagingError{reason: reason}
}

// contentWriter is the part of storage's content writer a chat image uses.
type contentWriter interface {
	Write(ctx context.Context, request, digest string, content io.ReaderAt, size int64) (storageclient.Stored, error)
}

func (o *OA) chatImageReader(ctx context.Context) (contentReader, error) {
	if o.imageReader != nil {
		return o.imageReader(ctx)
	}
	return o.machine.ResolveStorage(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

func (o *OA) chatImageWriter(ctx context.Context) (contentWriter, error) {
	if o.imageWriter != nil {
		return o.imageWriter(ctx)
	}
	return o.machine.ResolveStorageWriter(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

// readChatImage reads a PNG, JPEG, GIF or WebP file the caller names by its
// absolute path. This helper runs as the calling agent's own process, so it
// reads only what that agent may read. A device or pipe name is refused
// before it is opened, and the path and the opened handle must both be a
// regular file.
func readChatImage(path string) ([]byte, string, error) {
	if !filepath.IsAbs(path) {
		return nil, "", errors.New("gateway: image_path must be an absolute path")
	}
	if devicePath(path) {
		return nil, "", errors.New("gateway: image_path must name a file")
	}
	named, err := os.Stat(path)
	if err != nil {
		return nil, "", fmt.Errorf("gateway: image_path: %w", err)
	}
	if !named.Mode().IsRegular() {
		return nil, "", errors.New("gateway: image_path must name a file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("gateway: image_path: %w", err)
	}
	defer f.Close()
	if opened, err := f.Stat(); err != nil || !opened.Mode().IsRegular() {
		return nil, "", errors.New("gateway: image_path changed while it was opened")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCompleteImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("gateway: image_path: %w", err)
	}
	if len(data) == 0 || len(data) > maxCompleteImageBytes {
		return nil, "", errors.New("gateway: image_path must name an image of at most 20 MiB")
	}
	media := http.DetectContentType(data)
	if !chatImageTypes[media] {
		return nil, "", errors.New("gateway: image_path must name a PNG, JPEG, GIF or WebP image")
	}
	return data, media, nil
}

// devicePath reports a Windows device or raw namespace name, such as a
// named pipe or a console: a volume of two separators, then "." or "?".
func devicePath(path string) bool {
	volume := filepath.VolumeName(path)
	return len(volume) >= 3 && os.IsPathSeparator(volume[0]) && os.IsPathSeparator(volume[1]) && (volume[2] == '.' || volume[2] == '?')
}

// sniffChatImage names a stored image's media type from its first bytes.
func sniffChatImage(ctx context.Context, reader contentReader, digest string) (string, error) {
	opened, err := reader.Open(ctx, digest)
	if err != nil {
		return "", err
	}
	if opened.Outcome != contentwire.OpenOutcomeOpened || opened.Resource == nil {
		return "", fmt.Errorf("gateway: image unavailable: %s", opened.Outcome)
	}
	resource := *opened.Resource
	//unchecked: closing a read handle after the bytes are in hand changes no answer; the reader expires it
	defer reader.Close(context.WithoutCancel(ctx), resource)
	read, err := reader.Read(ctx, resource, 0, min(512, max(resource.Size, 1)))
	if err != nil {
		return "", err
	}
	if read.Outcome != contentwire.ReadOutcomeData || read.Chunk == nil {
		return "", fmt.Errorf("gateway: image unavailable: %s", read.Outcome)
	}
	media := http.DetectContentType(read.Chunk.Data)
	if !chatImageTypes[media] {
		return "", errors.New("gateway: image must be a PNG, JPEG, GIF or WebP image")
	}
	return media, nil
}
