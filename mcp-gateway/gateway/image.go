package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/fileid"
	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"
	inferenceclient "github.com/openabstractions/abstraction-inference/go/client"
	contentwire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

// ToolImageGenerate makes one local picture from a prompt and saves it in the
// person's Pictures folder (backlog/PICTURES-STORAGE.md rulings 2 and 4). It
// has the same name and core arguments as the OpenCode plugin's tool, which
// delegates here.
const ToolImageGenerate = "oa_image_generate"

const (
	imageDescription = "Make one picture from a prompt and save it as a file, answering its path. When outcome is working, follow the exact call in message using the returned handle. " + waitDescription
	// picturePolicy is the account's picture policy Setup writes when it
	// installs the starter picture model.
	picturePolicy     = "oa-image-default"
	pictureFolderName = "OpenAbstractions"
	pictureNameTries  = 20
	maxPictureBytes   = 8 << 20
	maxPicturePrompt  = 32768
	picturePollFirst  = 250 * time.Millisecond
	picturePollMost   = 2 * time.Second
)

// pictureSaveReserve is kept from the call's budget for reading and saving a
// finished picture.
var pictureSaveReserve = callFinishRoom

// Person-facing sentences of the picture tool (ruling 4).
const (
	pictureNotSetUp  = "Pictures are not set up yet."
	pictureHosted    = "Pictures are set to a hosted model, which this tool does not use yet."
	pictureHandleUse = "%s describes a new picture, and handle continues one already started: to wait for it, call oa_image_generate with the handle and optionally wait_ms; to start another, leave out handle."
)

// pictureHandleConflict is the first argument beside handle and wait_ms
// that describes a new picture, by name; "" when every other argument is
// null, empty or its default (size auto), as a client that fills defaults
// sends them (R35).
func pictureHandleConflict(fields map[string]json.RawMessage) string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if name == "handle" || name == "wait_ms" {
			continue
		}
		value := bytes.TrimSpace(fields[name])
		var text string
		if bytes.Equal(value, []byte("null")) || json.Unmarshal(value, &text) == nil && (text == "" || name == "size" && text == "auto") {
			continue
		}
		return name
	}
	return ""
}

// ImageInput is one oa_image_generate call: a new picture from prompt, with
// optional size, model or saved policy, and for an edit inputImage with an
// optional mask; or a continuation that carries only handle. The argument
// names are the OpenCode plugin's.
type ImageInput struct {
	Prompt          string `json:"prompt,omitempty" jsonschema:"what the picture shows; required for a new picture"`
	Size            string `json:"size,omitempty" jsonschema:"auto (default): the model's own size; or WIDTHxHEIGHT such as 512x512"`
	Model           string `json:"model,omitempty" jsonschema:"leave out for the person's picture model"`
	SelectionPolicy string `json:"selectionPolicy,omitempty" jsonschema:"a saved policy name; leave out for the person's picture model"`
	InputImage      string `json:"inputImage,omitempty" jsonschema:"to edit: the OA image digest sha256:... of the picture to change"`
	Mask            string `json:"mask,omitempty" jsonschema:"with inputImage: a PNG digest of the input's pixel size whose fully transparent pixels mark the area to change"`
	Handle          string `json:"handle,omitempty" jsonschema:"only to wait for a picture an earlier call is still making: the handle it answered with, alone or with wait_ms"`
	WaitMS          int64  `json:"wait_ms,omitempty" jsonschema:"milliseconds to wait for the picture before answering working with a handle; default 20000"`
}

// ImageOutput is the structured answer. Message is the one sentence the
// agent relays. A saved picture carries path and digest; a picture still
// being made carries handle; a refusal carries outcome, and for an argument
// refusal field and allowed (backlog/RIGHTS-FLOW.md ruling 3).
type ImageOutput struct {
	Outcome string   `json:"outcome"`
	Message string   `json:"message"`
	Path    string   `json:"path,omitempty"`
	Digest  string   `json:"digest,omitempty"`
	Handle  string   `json:"handle,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	Field   string   `json:"field,omitempty"`
	Allowed []string `json:"allowed,omitempty"`
}

// ImageBackend makes the picture under the backend's own OA principal. The
// tool process writes the file under the person's account (ruling 2).
type ImageBackend interface {
	GenerateImage(context.Context, ImageInput) (ImageOutput, error)
}

var imageArguments = []string{"prompt", "size", "model", "selectionPolicy", "inputImage", "mask", "handle", "wait_ms"}

// policyName is a saved selection policy's name.
var policyName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// pictureSize is a size a picture may name: auto or WIDTHxHEIGHT.
var pictureSize = regexp.MustCompile(`^(auto|[1-9][0-9]{0,4}x[1-9][0-9]{0,4})$`)

func invalidImage(field, message string, allowed ...string) *ImageOutput {
	return &ImageOutput{Outcome: "invalid", Message: message, Field: field, Allowed: allowed}
}

// imageRequest reads the call's arguments. Each wrong argument is refused at
// once with one sentence naming the values it takes; nothing is asked or
// started. A new picture's size defaults to auto.
func imageRequest(raw json.RawMessage) (ImageInput, *ImageOutput) {
	var in ImageInput
	fields := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if json.Unmarshal(raw, &fields) != nil {
			return in, invalidImage("prompt", "The arguments must be an object; prompt is required: describe the picture.", imageArguments...)
		}
	}
	for name := range fields {
		known := false
		for _, allowed := range imageArguments {
			known = known || name == allowed
		}
		if !known {
			return in, invalidImage(name, fmt.Sprintf("Unknown argument %s; allowed: %s.", name, strings.Join(imageArguments, ", ")), imageArguments...)
		}
	}
	text := func(name string, into *string) bool {
		value, ok := fields[name]
		return !ok || json.Unmarshal(value, into) == nil
	}
	if value, ok := fields["wait_ms"]; ok {
		if json.Unmarshal(value, &in.WaitMS) != nil || in.WaitMS < 0 {
			return in, invalidImage("wait_ms", "wait_ms must be a whole number of milliseconds; leave it out for 20000.")
		}
	}
	if !text("handle", &in.Handle) {
		return in, invalidImage("handle", "handle must be the handle an earlier oa_image_generate call answered with.")
	}
	if _, ok := fields["handle"]; ok {
		if field := pictureHandleConflict(fields); field != "" {
			return in, invalidImage(field, fmt.Sprintf(pictureHandleUse, field), "handle", "wait_ms")
		}
		if decoded, err := base64.RawURLEncoding.DecodeString(in.Handle); err != nil || len(in.Handle) != 32 || len(decoded) != 24 {
			return in, invalidImage("handle", "handle must be the handle an earlier oa_image_generate call answered with.")
		}
		return in, nil
	}
	if !text("prompt", &in.Prompt) || strings.TrimSpace(in.Prompt) == "" {
		return in, invalidImage("prompt", "prompt is required: describe the picture.")
	}
	if len(in.Prompt) > maxPicturePrompt || !utf8.ValidString(in.Prompt) {
		return in, invalidImage("prompt", fmt.Sprintf("prompt is too long: at most %d bytes.", maxPicturePrompt))
	}
	if !text("size", &in.Size) || in.Size != "" && !pictureSize.MatchString(in.Size) {
		return in, invalidImage("size", "size must be auto or WIDTHxHEIGHT such as 512x512; leave it out for auto.", "auto", "WIDTHxHEIGHT")
	}
	if _, ok := fields["model"]; ok && (!text("model", &in.Model) || strings.TrimSpace(in.Model) == "" || len(in.Model) > 256) {
		return in, invalidImage("model", "model must be a model name; leave it out for the person's picture model.")
	}
	if _, ok := fields["selectionPolicy"]; ok && (!text("selectionPolicy", &in.SelectionPolicy) || !policyName.MatchString(in.SelectionPolicy)) {
		return in, invalidImage("selectionPolicy", "selectionPolicy must be a saved policy name; leave it out for the person's picture model.")
	}
	if in.Model != "" && in.SelectionPolicy != "" {
		return in, invalidImage("selectionPolicy", "Give model or selectionPolicy, not both; leave both out for the person's picture model.")
	}
	if _, ok := fields["inputImage"]; ok && (!text("inputImage", &in.InputImage) || !validDigest(in.InputImage)) {
		return in, invalidImage("inputImage", "inputImage must be an OA image digest sha256:<64 hex>.")
	}
	if _, ok := fields["mask"]; ok && (!text("mask", &in.Mask) || in.InputImage == "" || !validDigest(in.Mask)) {
		return in, invalidImage("mask", "mask must be a sha256 digest and goes only with inputImage.")
	}
	if in.Size == "" {
		in.Size = "auto"
	}
	return in, nil
}

func imageSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[ImageInput](nil)
	if err != nil {
		panic(err) // Static adapter type inference, as with mcp.AddTool.
	}
	// size names its default in its description only: a schema default is
	// filled into every call by some clients, a continuation by handle
	// included (R35).
	// A new picture names prompt; a continuation names only handle. The
	// handler refuses other shapes with one sentence each.
	schema.AnyOf = []*jsonschema.Schema{{Required: []string{"prompt"}}, {Required: []string{"handle"}}}
	return schema
}

func imageToolResult(out ImageOutput) *mcp.CallToolResult {
	ok := out.Outcome == "saved" || out.Outcome == "working"
	return &mcp.CallToolResult{IsError: !ok, StructuredContent: out, Content: []mcp.Content{&mcp.TextContent{Text: out.Message}}}
}

func addImageTool(s *mcp.Server, backend ImageBackend, bounded func(context.Context, func(context.Context) error) error, effect *mcp.ToolAnnotations) {
	output, err := jsonschema.For[ImageOutput](nil)
	if err != nil {
		panic(err)
	}
	s.AddTool(&mcp.Tool{Name: ToolImageGenerate, Description: imageDescription, Annotations: effect, InputSchema: imageSchema(), OutputSchema: output},
		func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			in, refused := imageRequest(request.Params.Arguments)
			if refused != nil {
				return imageToolResult(*refused), nil
			}
			ctx = withProgress(ctx, request)
			var out ImageOutput
			err := bounded(ctx, func(ctx context.Context) (err error) { out, err = backend.GenerateImage(ctx, in); return err })
			if err != nil {
				if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return nil, err
				}
				// The structured answer keeps one shape for every outcome.
				return imageToolResult(ImageOutput{Outcome: "unavailable", Message: err.Error()}), nil
			}
			return imageToolResult(out), nil
		})
}

// callScopeKey carries what one tools/call keeps across the permission
// middleware's one permitted retry.
type callScopeKey struct{}

type callScope struct{ requestKey string }

// callRequestKey is the gateway-made request key of this call's picture job
// (MCPG-H2). The retry after the person's Allow reuses it, so it reconciles
// the same handle and request identity.
func callRequestKey(ctx context.Context) (string, error) { return callRequestKeyFor(ctx, "picture-") }

// callRequestKeyFor is callRequestKey for a job whose keys start prefix.
func callRequestKeyFor(ctx context.Context, prefix string) (string, error) {
	scope, _ := ctx.Value(callScopeKey{}).(*callScope)
	if scope != nil && scope.requestKey != "" {
		return scope.requestKey, nil
	}
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	key := prefix + base64.RawURLEncoding.EncodeToString(b)
	if scope != nil {
		scope.requestKey = key
	}
	return key, nil
}

// pictureSteps are the effects one picture call takes, in order. OA supplies
// its own; tests supply fakes.
type pictureSteps struct {
	// folder is the person's Pictures folder, or a layout refusal.
	folder func() (string, error)
	// model is the first candidate of the named saved policy, or the answer
	// that ends the call.
	model func(context.Context, string) (string, *ImageOutput, error)
	// inputType is the media type of an edit input, read from its first
	// bytes, or the answer that ends the call.
	inputType func(context.Context, string) (string, *ImageOutput, error)
	submit    func(context.Context, JobSubmitInput) (JobSubmitOutput, error)
	status    func(context.Context, string) (JobStatusOutput, error)
	// read returns the delivery's bytes; makePicture verifies them.
	read func(context.Context, jobwire.Delivery) ([]byte, error)
	// now is the local time a saved picture is named after.
	now func() time.Time
	// saved is the path a handle's picture was saved to, if that file still
	// holds it; remember records it after a save.
	saved    func(handle string) (path, digest string, ok bool)
	remember func(handle, path, digest string)
	// started is when the handle's job was submitted; false when unknown.
	started func(handle string) (time.Time, bool)
}

// makePicture runs one picture call: the Pictures folder first, so nothing is
// asked or started without one; then the model, the durable job, at most the
// call's follow budget of waiting, and the save.
func makePicture(ctx context.Context, s pictureSteps, in ImageInput) (ImageOutput, error) {
	folder, refused := picturesFolder(s.folder)
	if refused != nil {
		return *refused, nil
	}
	handle := in.Handle
	if handle == "" {
		ended := func(answer *ImageOutput, err error) (ImageOutput, error) {
			if answer != nil {
				return *answer, err
			}
			return ImageOutput{}, err
		}
		model := in.Model
		if model == "" {
			policy := in.SelectionPolicy
			if policy == "" {
				policy = picturePolicy
			}
			chosen, answer, err := s.model(ctx, policy)
			if err != nil || answer != nil {
				return ended(answer, err)
			}
			model = chosen
		}
		request := JobSubmitInput{Profile: profileWord(jobwire.ProfileImageBatch), Model: model, Prompt: in.Prompt, Hosting: "local", Size: in.Size, Count: 1}
		// An edit names its inputs' media types, read from their first bytes;
		// the runtime verifies the bytes again before the engine sees them.
		if in.InputImage != "" {
			media, answer, err := s.inputType(ctx, in.InputImage)
			if err != nil || answer != nil {
				return ended(answer, err)
			}
			if _, ok := pictureTypes[media]; !ok {
				return ImageOutput{Outcome: "invalid", Field: "inputImage", Message: "The input image must be a PNG, JPEG or WebP image."}, nil
			}
			request.InputImage, request.InputMediaType = in.InputImage, media
		}
		if in.Mask != "" {
			media, answer, err := s.inputType(ctx, in.Mask)
			if err != nil || answer != nil {
				return ended(answer, err)
			}
			if media != "image/png" {
				return ImageOutput{Outcome: "invalid", Field: "mask", Message: "The mask must be a PNG image."}, nil
			}
			request.Mask, request.MaskMediaType = in.Mask, media
		}
		key, err := callRequestKey(ctx)
		if err != nil {
			return ImageOutput{}, err
		}
		request.RequestKey = key
		submitted, err := s.submit(ctx, request)
		if err != nil {
			return ImageOutput{}, err
		}
		if submitted.Outcome != "accepted" {
			return ImageOutput{Outcome: submitted.Outcome, Reason: submitted.Reason, Handle: submitted.Handle, Message: pictureRefusal(model, submitted)}, nil
		}
		handle = submitted.Handle
	}
	return followPicture(ctx, s, folder, handle, in.Size, callWait(in.WaitMS, defaultCallWait))
}

// unservedModel are the inference executor's refusals of a model no host on
// this computer holds or serves for pictures: host: plus the router's
// not-here and no-host verdicts (abstraction-inference provider.pick).
var unservedModel = map[string]bool{"host:router:not-here": true, "host:router:no-host": true}

// pictureRefusal is the sentence for a picture job the runtime did not
// accept. A model no host here serves is named; an invalid request carries
// its reason, which the agent passes on.
func pictureRefusal(model string, submitted JobSubmitOutput) string {
	switch {
	case submitted.Message != "":
		return submitted.Message
	case strings.HasSuffix(submitted.Reason, "approval:pending") || strings.HasSuffix(submitted.Reason, "program:changed") || startFailedSentence(submitted.Reason) != "":
		return refusalSentence(context.Background(), submitted.Reason, model, "image")
	case unservedModel[submitted.Reason]:
		return fmt.Sprintf("No picture model named %s is served on this computer. Setup or Models installs one; leave model out for the person's picture model.", model)
	case submitted.Outcome == "invalid" && submitted.Reason != "":
		return fmt.Sprintf("OpenAbstractions did not start the picture: invalid (%s).", submitted.Reason)
	}
	return fmt.Sprintf("OpenAbstractions did not start the picture: %s.", submitted.Outcome)
}

func profileWord(p jobwire.Profile) string {
	word, _ := p.WireName()
	return word
}

// picturesFolder resolves the folder pictures go into: the person's Pictures
// folder (STO-L2, STO-L5), which must exist. A layout refusal passes on with
// its reason, and nothing substitutes another folder.
func picturesFolder(resolve func() (string, error)) (string, *ImageOutput) {
	path, err := resolve()
	if err != nil {
		reason := string(layout.ReasonOf(err))
		if reason == "" {
			reason = "unavailable"
		}
		return "", &ImageOutput{Outcome: "no_pictures_folder", Reason: "layout:" + reason,
			Message: fmt.Sprintf("Nothing was made: this account has no Pictures folder (%s).", reason)}
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return "", &ImageOutput{Outcome: "no_pictures_folder", Reason: "layout:missing",
			Message: fmt.Sprintf("Nothing was made: the Pictures folder %s does not exist.", path)}
	}
	return path, nil
}

func stillMaking(handle, progress string) ImageOutput {
	return ImageOutput{Outcome: "working", Handle: handle,
		Message: fmt.Sprintf("%s. To wait for this picture, call oa_image_generate with {\"handle\":%q}.", progress, handle)}
}

// followPicture reads the job until it ends or the follow budget runs out,
// then saves a finished picture.
func followPicture(ctx context.Context, s pictureSteps, folder, handle, size string, wait time.Duration) (ImageOutput, error) {
	started := time.Now()
	until := started.Add(wait)
	if deadline, ok := ctx.Deadline(); ok && deadline.Add(-pictureSaveReserve).Before(until) {
		until = deadline.Add(-pictureSaveReserve)
	}
	// The progress line counts from the job's submission, across the calls
	// that resume it with its handle.
	since := started
	if s.started != nil {
		if submitted, ok := s.started(handle); ok && submitted.Before(since) {
			since = submitted
		}
	}
	line := "Still making the picture"
	pause := picturePollFirst
	for {
		status, err := s.status(ctx, handle)
		if err != nil {
			return ImageOutput{}, err
		}
		if status.Outcome != "observed" {
			return ImageOutput{Outcome: status.Outcome, Handle: handle,
				Message: fmt.Sprintf("OpenAbstractions could not read the picture's job: %s.", status.Outcome)}, nil
		}
		switch status.State {
		case "complete":
			return savePictureResult(ctx, s, folder, handle, status)
		case "failed":
			return pictureFailure(size, status.Failure), nil
		case "cancelled":
			return ImageOutput{Outcome: "cancelled", Message: "The picture was cancelled before it was made."}, nil
		}
		var done, total float64
		line, done, total = progressLine("Making the picture", status.Progress["done"], status.Progress["total"], "", time.Since(since))
		reportProgress(ctx, line, done, total)
		left := time.Until(until)
		if left <= 0 {
			return stillMaking(handle, line), nil
		}
		timer := time.NewTimer(min(pause, left))
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return stillMaking(handle, line), nil
			}
			return ImageOutput{}, ctx.Err()
		case <-timer.C:
		}
		pause = min(pause*2, picturePollMost)
	}
}

// sizeListed is an engine's refusal of a size it does not make, naming the
// sizes it makes: a reason ending size:WxH,WxH.
var sizeListed = regexp.MustCompile(`(?:^|[:\s])size(?::([0-9x,]+))?$`)

func pictureFailure(size, failure string) ImageOutput {
	if match := sizeListed.FindStringSubmatch(strings.TrimSpace(failure)); match != nil {
		allowed := []string{"auto"}
		for _, listed := range strings.Split(match[1], ",") {
			if listed != "" {
				allowed = append(allowed, listed)
			}
		}
		return ImageOutput{Outcome: "invalid", Field: "size", Allowed: allowed,
			Message: fmt.Sprintf("This picture model does not make %s. Allowed sizes: %s. Leave size out for auto.", size, strings.Join(allowed, ", "))}
	}
	if sentence := jobReasonSentence(failure); sentence != "" {
		return ImageOutput{Outcome: "failed", Message: sentence}
	}
	if failure == "" {
		return ImageOutput{Outcome: "failed", Message: "The picture could not be made."}
	}
	return ImageOutput{Outcome: "failed", Message: fmt.Sprintf("The picture could not be made: %s.", strings.TrimSuffix(failure, "."))}
}

// pictureTypes are the media types a picture may be, with their file
// extensions and leading bytes.
var pictureTypes = map[string]struct {
	ext   string
	match func([]byte) bool
}{
	"image/png":  {".png", func(b []byte) bool { return bytes.HasPrefix(b, []byte{137, 80, 78, 71, 13, 10, 26, 10}) }},
	"image/jpeg": {".jpg", func(b []byte) bool { return bytes.HasPrefix(b, []byte{255, 216, 255}) }},
	"image/webp": {".webp", func(b []byte) bool {
		return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
	}},
}

func savePictureResult(ctx context.Context, s pictureSteps, folder, handle string, status JobStatusOutput) (ImageOutput, error) {
	result, _ := status.Result.(*jobwire.JobResult)
	if status.ResultOutcome != "data" || result == nil {
		outcome := status.ResultOutcome
		if outcome == "" || outcome == "data" {
			outcome = "invalid"
		}
		return ImageOutput{Outcome: outcome, Handle: handle, Message: fmt.Sprintf("The picture is made, and its result could not be read: %s.", outcome)}, nil
	}
	if result.Profile != jobwire.ProfileImageBatch || len(result.Deliveries) != 1 {
		return ImageOutput{Outcome: "invalid", Message: "That handle is not one picture; oa_image_generate reads only its own pictures."}, nil
	}
	delivery := result.Deliveries[0]
	kind, known := pictureTypes[delivery.MediaType]
	if delivery.Location != "local" || !known || delivery.Size < 1 || delivery.Size > maxPictureBytes || !validDigest(delivery.Digest) {
		return ImageOutput{Outcome: "unsupported", Digest: delivery.Digest, Message: "Not saved: the picture is not a local PNG, JPEG or WebP image of at most 8 MiB."}, nil
	}
	// A repeated continuation answers the file its picture was saved to.
	if s.saved != nil {
		if path, digest, ok := s.saved(handle); ok && digest == delivery.Digest {
			return savedPicture(path, digest), nil
		}
	}
	data, err := s.read(ctx, delivery)
	if err != nil {
		return ImageOutput{}, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != delivery.Size || "sha256:"+hex.EncodeToString(sum[:]) != delivery.Digest || !kind.match(data) {
		return ImageOutput{Outcome: "invalid", Digest: delivery.Digest, Message: "Not saved: the picture's bytes do not match its digest and type."}, nil
	}
	path, refused := savePicture(folder, data, kind.ext, s.now())
	if refused != "" {
		return ImageOutput{Outcome: "not_saved", Digest: delivery.Digest, Message: fmt.Sprintf("%s The picture is %s.", refused, delivery.Digest)}, nil
	}
	if s.remember != nil {
		s.remember(handle, path, delivery.Digest)
	}
	return savedPicture(path, delivery.Digest), nil
}

func savedPicture(path, digest string) ImageOutput {
	return ImageOutput{Outcome: "saved", Path: path, Digest: digest,
		Message: fmt.Sprintf("Saved to %s. To edit it, pass inputImage %s.", path, digest)}
}

func validDigest(digest string) bool {
	hexPart, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hexPart) != 64 {
		return false
	}
	_, err := hex.DecodeString(hexPart)
	return err == nil && strings.ToLower(hexPart) == hexPart
}

// savePicture writes data to <folder>/OpenAbstractions/image-<local time><ext>.
// Every step opens through an os.Root at the Pictures folder, so a link or
// junction cannot lead the file outside it. Each name is created exclusively;
// a taken name moves on to -2 … -20, and an existing file is never replaced.
// It answers the path, or a sentence saying why nothing was saved.
func savePicture(folder string, data []byte, ext string, now time.Time) (string, string) {
	return saveMedia(folder, "Pictures", "image-", data, ext, now)
}

// saveMedia writes data to <folder>/OpenAbstractions/<prefix><local
// time><ext> as savePicture describes; place names the folder in its
// answers.
func saveMedia(folder, place, prefix string, data []byte, ext string, now time.Time) (string, string) {
	root, err := os.OpenRoot(folder)
	if err != nil {
		return "", fmt.Sprintf("Not saved: the %s folder %s cannot be read.", place, folder)
	}
	defer root.Close()
	dir := filepath.Join(folder, pictureFolderName)
	if err := root.Mkdir(pictureFolderName, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Sprintf("Not saved: the folder %s cannot be made.", dir)
	}
	stamp := now.Local().Format("20060102-150405")
	for n := 1; n <= pictureNameTries; n++ {
		name := prefix + stamp
		if n > 1 {
			name += fmt.Sprintf("-%d", n)
		}
		name += ext
		path := filepath.Join(dir, name)
		file, err := root.OpenFile(pictureFolderName+"/"+name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Sprintf("Not saved: %s cannot be written inside the %s folder.", path, place)
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			//unchecked: the partial file is removed best effort; the answer already says nothing was saved
			_ = root.Remove(pictureFolderName + "/" + name)
			return "", fmt.Sprintf("Not saved: %s could not be written completely.", path)
		}
		return path, ""
	}
	return "", fmt.Sprintf("Not saved: %d file names for this second are taken in %s.", pictureNameTries, dir)
}

// GenerateImage makes one picture under this executable's OA identity and
// saves it in the person's Pictures folder.
func (o *OA) GenerateImage(ctx context.Context, in ImageInput) (ImageOutput, error) {
	return makePicture(ctx, o.pictureSteps(), in)
}

func (o *OA) pictureSteps() pictureSteps {
	return pictureSteps{
		folder:    o.pictures,
		model:     o.pictureModel,
		inputType: o.inputType,
		submit:    o.Submit,
		status: func(ctx context.Context, handle string) (JobStatusOutput, error) {
			return o.Status(ctx, JobHandleInput{Handle: handle})
		},
		read:     o.readPicture,
		now:      time.Now,
		saved:    o.savedPicture,
		remember: o.rememberPicture,
		started:  o.handleStarted,
	}
}

// handleStarted is when the handle's job was submitted: the time its handle
// record was written, which happens once, at submission.
func (o *OA) handleStarted(handle string) (time.Time, bool) {
	path, err := o.recordPath(handle)
	if err != nil {
		return time.Time{}, false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// pictures is the person's Pictures folder from storage layout.
func (o *OA) pictures() (string, error) {
	if o.picturesAt != nil {
		return o.picturesAt()
	}
	return layout.ResolvePerson(layout.Current(), layout.Pictures)
}

// readPolicy reads this program's effective saved policy. The runtime's
// ReadEffective consults no right and asks nothing
// (serve/runtime_inference_selection.go).
func (o *OA) readPolicy(ctx context.Context, name string) (inferenceclient.SelectionReadV2, error) {
	operator, err := o.machine.ResolveInferenceSelectionOperatorV2(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return inferenceclient.SelectionReadV2{}, err
	}
	return operator.ReadEffective(ctx, name)
}

// pictureModel uses the first candidate of the named policy when it is local.
func (o *OA) pictureModel(ctx context.Context, name string) (string, *ImageOutput, error) {
	read, err := o.readPolicy(ctx, name)
	if err != nil {
		return "", nil, err
	}
	model, answer := pictureCandidate(name, read)
	return model, answer, nil
}

// pictureCandidate is the model a policy read names first, or the answer
// that ends the call: not set up, hosted, or the read's refusal.
func pictureCandidate(name string, read inferenceclient.SelectionReadV2) (string, *ImageOutput) {
	notSetUp := &ImageOutput{Outcome: "not_set_up", Message: pictureNotSetUp}
	if name != picturePolicy {
		notSetUp.Message = fmt.Sprintf("The saved policy %s names no picture model.", name)
	}
	switch read.Outcome {
	case inferenceclient.SelectionReadOutcomeFound:
	case inferenceclient.SelectionReadOutcomeUnknown:
		return "", notSetUp
	default:
		return "", &ImageOutput{Outcome: read.Outcome.String(), Message: fmt.Sprintf("OpenAbstractions could not read the picture setting: %s.", read.Outcome)}
	}
	if read.Policy == nil || len(read.Policy.Candidates) == 0 || read.Policy.Candidates[0].Model == "" {
		return "", notSetUp
	}
	first := read.Policy.Candidates[0]
	if first.Placement != inferenceclient.CandidatePlacementLocal {
		return "", &ImageOutput{Outcome: "hosted", Message: pictureHosted}
	}
	return first.Model, nil
}

// contentReader is the part of storage's content reader a picture uses.
type contentReader interface {
	Open(context.Context, string) (contentwire.OpenResult, error)
	Read(context.Context, contentwire.Resource, int64, int64) (contentwire.ReadResult, error)
	Close(context.Context, contentwire.Resource) (contentwire.CloseResult, error)
}

// inputType names an edit input's media type from its first bytes, read
// through this caller's content reader. A digest the caller may not read
// ends the call with the reader's outcome.
func (o *OA) inputType(ctx context.Context, digest string) (string, *ImageOutput, error) {
	reader, err := o.machine.ResolveStorage(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return "", nil, err
	}
	return sniffContent(ctx, reader, digest)
}

func sniffContent(ctx context.Context, reader contentReader, digest string) (string, *ImageOutput, error) {
	opened, err := reader.Open(ctx, digest)
	if err != nil {
		return "", nil, err
	}
	if opened.Outcome != contentwire.OpenOutcomeOpened || opened.Resource == nil {
		outcome := opened.Outcome.String()
		return "", &ImageOutput{Outcome: outcome, Message: fmt.Sprintf("Input image unavailable: %s.", outcome)}, nil
	}
	resource := *opened.Resource
	//unchecked: closing a read handle after the bytes are in hand changes no answer; the reader expires it
	defer reader.Close(context.WithoutCancel(ctx), resource)
	read, err := reader.Read(ctx, resource, 0, min(16, max(resource.Size, 1)))
	if err != nil {
		return "", nil, err
	}
	if read.Outcome != contentwire.ReadOutcomeData || read.Chunk == nil {
		outcome := read.Outcome.String()
		return "", &ImageOutput{Outcome: outcome, Message: fmt.Sprintf("Input image unavailable: %s.", outcome)}, nil
	}
	for media, kind := range pictureTypes {
		if kind.match(read.Chunk.Data) {
			return media, nil, nil
		}
	}
	return "", nil, nil
}

// readPicture reads one local delivery through this caller's content reader.
func (o *OA) readPicture(ctx context.Context, delivery jobwire.Delivery) ([]byte, error) {
	reader, err := o.machine.ResolveStorage(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	opened, err := reader.Open(ctx, delivery.Digest)
	if err != nil {
		return nil, err
	}
	if opened.Outcome != contentwire.OpenOutcomeOpened || opened.Resource == nil || opened.Resource.Size != delivery.Size {
		return nil, fmt.Errorf("gateway: picture content %s", opened.Outcome)
	}
	resource := *opened.Resource
	//unchecked: closing a read handle after the bytes are in hand changes no answer; the reader expires it
	defer reader.Close(context.WithoutCancel(ctx), resource)
	data := make([]byte, 0, resource.Size)
	for int64(len(data)) < resource.Size {
		read, err := reader.Read(ctx, resource, int64(len(data)), 65536)
		if err != nil {
			return nil, err
		}
		if read.Outcome != contentwire.ReadOutcomeData || read.Chunk == nil {
			return nil, fmt.Errorf("gateway: picture content %s", read.Outcome)
		}
		data = append(data, read.Chunk.Data...)
		if read.Chunk.EOF {
			break
		}
	}
	return data, nil
}

// savedRecord is what the gateway keeps about a handle's saved picture, beside
// its handle record: the file and the digest it holds.
type savedRecord struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

func (o *OA) savedPath(handle string) (string, bool) {
	record, err := o.recordPath(handle)
	if err != nil {
		return "", false
	}
	return strings.TrimSuffix(record, ".json") + ".picture.json", true
}

// savedPicture answers the file a handle's picture was saved to, while that
// file still holds the picture.
func (o *OA) savedPicture(handle string) (string, string, bool) {
	path, ok := o.savedPath(handle)
	if !ok {
		return "", "", false
	}
	folder, err := o.pictures()
	if err != nil {
		return "", "", false
	}
	return savedFile(folder, path)
}

// savedFile reads a saved-picture record and checks its file, opened through
// the Pictures folder's root, still holds the recorded digest. The record
// keeps only the base name; any other name is refused.
func savedFile(folder, record string) (string, string, bool) {
	raw, err := readBounded(record, maxRequestRecordBytes)
	var saved savedRecord
	if err != nil || json.Unmarshal(raw, &saved) != nil || !validDigest(saved.Digest) || fileid.SafeName(saved.Name) != nil {
		return "", "", false
	}
	root, err := os.OpenRoot(folder)
	if err != nil {
		return "", "", false
	}
	defer root.Close()
	file, err := root.Open(pictureFolderName + "/" + saved.Name)
	if err != nil {
		return "", "", false
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPictureBytes+1))
	//unchecked: a read-only handle's close error changes nothing already read
	_ = file.Close()
	if err != nil || len(data) > maxPictureBytes {
		return "", "", false
	}
	sum := sha256.Sum256(data)
	if "sha256:"+hex.EncodeToString(sum[:]) != saved.Digest {
		return "", "", false
	}
	return filepath.Join(folder, pictureFolderName, saved.Name), saved.Digest, true
}

// rememberPicture records a handle's saved file by its base name. A record
// that cannot be written leaves a later continuation to save another copy.
func (o *OA) rememberPicture(handle, saved, digest string) {
	path, ok := o.savedPath(handle)
	if !ok {
		return
	}
	//unchecked: savedRecord holds two strings, which json.Marshal cannot fail on
	data, _ := json.Marshal(savedRecord{Name: filepath.Base(saved), Digest: digest})
	//unchecked: the record only spares a second copy; the saved file is the answer either way
	_ = os.WriteFile(path, data, 0o600)
}

// ServesPictures reports whether the person's picture policy names a local
// model, read once when an MCP session lists its tools. ReadEffective
// consults no right, so listing tools asks the person nothing. A read that
// gets no answer reports an error, and the tool is listed.
func (o *OA) ServesPictures(ctx context.Context) (bool, error) {
	read, err := o.readPolicy(ctx, picturePolicy)
	if err != nil {
		return false, err
	}
	if read.Outcome != inferenceclient.SelectionReadOutcomeFound && read.Outcome != inferenceclient.SelectionReadOutcomeUnknown {
		return false, fmt.Errorf("gateway: picture policy %s", read.Outcome)
	}
	_, answer := pictureCandidate(picturePolicy, read)
	return answer == nil, nil
}
