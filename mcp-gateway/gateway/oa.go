package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	casapi "github.com/openabstractions/abstraction-cas/go/api"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	inference "github.com/openabstractions/abstraction-inference/go"
	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"
	inferenceclient "github.com/openabstractions/abstraction-inference/go/client"
	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
)

const (
	handleRecordVersion   = 1
	jobSchema             = "oa.inference.job@1"
	maxResultBytes        = 65536
	maxCompletionBytes    = 256 << 10
	maxCompletionMetadata = 64 << 10
	maxCompletionParts    = 256
	defaultMaxOutput      = 1024
	maxHandleRecordBytes  = 64 << 10
	maxRequestRecordBytes = 4 << 10
)

type OA struct {
	machine  *facade.Machine
	stateDir string
	state    casapi.BoundedFileStore
	// picturesAt replaces storage layout's Pictures folder in tests.
	picturesAt func() (string, error)
	// videosAt replaces storage layout's Videos folder in tests.
	videosAt func() (string, error)
	// statusAt replaces the runtime read of a job's state and result, and
	// readClipAt the content read of a clip, in tests.
	statusAt   func(context.Context, JobHandleInput) (JobStatusOutput, *jobwire.JobResult, error)
	readClipAt func(context.Context, jobwire.Delivery) ([]byte, error)
	// videoWatch is the state of the clips this process saves when their
	// jobs end (video_watch.go).
	videoWatch videoWatch
	// imageReader and imageWriter replace this caller's content reader and
	// writer for a chat image in tests.
	imageReader func(context.Context) (contentReader, error)
	imageWriter func(context.Context) (contentWriter, error)
}

// NewOA binds all calls to the identity that OA observes for this executable.
// stateDir retains opaque recovery handles; callers cannot select an OA scope.
func NewOA(machine *facade.Machine, stateDir string) (*OA, error) {
	if machine == nil || stateDir == "" || !filepath.IsAbs(stateDir) {
		return nil, errors.New("gateway: machine and absolute state directory required")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("gateway: create state directory: %w", err)
	}
	return &OA{machine: machine, stateDir: stateDir, state: casapi.BoundedFileStore{MaxBytes: maxHandleRecordBytes}}, nil
}

func (o *OA) Models(ctx context.Context, in ModelsInput) (ModelsOutput, error) {
	c, err := o.machine.ResolveRouter(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return ModelsOutput{}, err
	}
	snapshot, err := c.ModelsContext(ctx, in.Fresh)
	if err != nil {
		return ModelsOutput{}, err
	}
	models := modelsFrom(snapshot.Models)
	models = append(models, o.setupNeeded(ctx, models)...)
	return ModelsOutput{ObservedUnixMS: time.Now().UnixMilli(), Models: models}, nil
}

// digestText is a content digest or commit anywhere in a name: 16 or more
// hex digits in a row, or a sha256 prefix. It names no model to a person.
var digestText = regexp.MustCompile(`(?i)[0-9a-f]{16,}|(?:^|[\\/:])sha256[-:]`)

// modelFileType is the model file extension a plain name drops.
var modelFileType = regexp.MustCompile(`(?i)\.(gguf|safetensors|bin|onnx|ggml)$`)

// unnamedModel names a model nothing names readably (owner, 2026-11-01: a
// digest is never shown to a person).
const unnamedModel = "Unnamed model"

// plainModelName is the first of names no part of which carries a digest,
// else the plain last part of one without its file type, else "": the
// OpenCode plugin's rule (adopters/opencode inventory.js plainModelName), so
// both clients name a model alike.
func plainModelName(names []string) string {
	for _, name := range names {
		if name != "" && !digestText.MatchString(name) {
			return name
		}
	}
	for _, name := range names {
		parts := strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' })
		if len(parts) == 0 {
			continue
		}
		if plain := modelFileType.ReplaceAllString(parts[len(parts)-1], ""); plain != "" && !digestText.MatchString(plain) {
			return plain
		}
	}
	return ""
}

// headerModelName is what a held object's own header states it is, the
// architecture a store read from a GGUF, for a model whose names are all
// digests; "" when no object states one.
func headerModelName(objects []routerwire.ModelObject) string {
	for _, o := range objects {
		if o.Architecture != "" && !digestText.MatchString(o.Architecture) {
			return o.Architecture
		}
	}
	return ""
}

// modelName is how a person is shown a family: the catalogue's title, else
// its family or a servable alias when one is readable (the name a model host
// declaration gave it), else what its file's header states. A family with
// none of these is listed as unnamedModel and the log names its family.
func modelName(family routerwire.Family, aliases []string) string {
	if family.Title != "" {
		return family.Title
	}
	if name := plainModelName(append([]string{family.Family}, aliases...)); name != "" {
		return name
	}
	if name := headerModelName(family.Objects); name != "" {
		return name
	}
	log.Printf("oa_models_list: model %s has no readable name or header; listed as %q", family.Family, unnamedModel)
	return unnamedModel
}

// plainAliases drops the aliases that carry a digest (digestText), such as a
// model host serving a store's object under its file name
// 55983c5a….gguf, when a plain alias remains: a digest names no model to a
// person. A model whose every alias is a digest keeps them all, since a call
// asks for it by one of them.
func plainAliases(aliases []string) []string {
	plain := slices.DeleteFunc(slices.Clone(aliases), func(alias string) bool { return digestText.MatchString(alias) })
	if len(plain) == 0 {
		return aliases
	}
	return plain
}

// servedFrom names the stores whose copy of family a servable local alias
// serves, from the router's engine associations (Family.Objects), when
// held lists a copy in another store that no engine here serves: the R34
// live test's Qwen3.6 35B held in huggingface (22.85 GB) and in Setup's
// store, served from Setup's store alone (D10). It is nil when every held
// copy is served, or when no engine association names a store.
func servedFrom(family routerwire.Family, held []string) []string {
	servable := map[string]bool{}
	for _, alias := range family.Names {
		if alias.Servable && !alias.Hosted {
			servable[alias.Host+"\x00"+alias.Name] = true
		}
	}
	var served []string
	for _, object := range family.Objects {
		if object.Domain != "" || object.Store == "" || slices.Contains(served, object.Store) {
			continue
		}
		if slices.ContainsFunc(object.Engines, func(e routerwire.ModelEngine) bool { return servable[e.Host+"\x00"+e.Model] }) {
			served = append(served, object.Store)
		}
	}
	if len(served) == 0 || !slices.ContainsFunc(held, func(store string) bool { return !slices.Contains(served, store) }) {
		return nil
	}
	slices.Sort(served)
	return served
}

// modelsFrom is the structured listing: the servable aliases of each family,
// and the stores holding it, whether or not a host serves it.
func modelsFrom(families []routerwire.Family) []Model {
	out := ModelsOutput{Models: make([]Model, 0, len(families))}
	for _, family := range families {
		m := Model{Family: family.Family, HeldIn: family.HeldIn}
		profiles := map[string]bool{}
		lines := map[string][]string{}
		for _, alias := range family.Names {
			if !alias.Servable {
				continue
			}
			m.Aliases = append(m.Aliases, alias.Name)
			lines[alias.Name] = append(lines[alias.Name], capabilityLine(alias))
			for _, profile := range alias.Profiles {
				profiles[profile] = true
			}
		}
		slices.Sort(m.Aliases)
		// The router lists a name once per server serving it (ROUTE-D7).
		m.Aliases = slices.Compact(m.Aliases)
		for profile := range profiles {
			m.Profiles = append(m.Profiles, profile)
		}
		slices.Sort(m.Profiles)
		m.Servable = len(m.Aliases) > 0
		m.Name = modelName(family, m.Aliases)
		m.Aliases = plainAliases(m.Aliases)
		// A hidden digest alias takes its capability line with it.
		for _, alias := range m.Aliases {
			m.Capabilities = append(m.Capabilities, lines[alias]...)
		}
		slices.Sort(m.Capabilities)
		m.Capabilities = slices.Compact(m.Capabilities)
		m.ServedFrom = servedFrom(family, m.HeldIn)
		// A model the machine holds and no host serves is still on this
		// machine; a caller asking what exists here is owed it.
		if m.Servable || len(m.HeldIn) > 0 {
			out.Models = append(out.Models, m)
		}
	}
	slices.SortFunc(out.Models, func(a, b Model) int { return strings.Compare(a.Family, b.Family) })
	return out.Models
}

// Complete runs one bounded text inference attempt. The MCP tool middleware
// owns waiting for pending first-use questions and the one permitted retry.
func (o *OA) Complete(ctx context.Context, in CompleteInput) (CompleteOutput, error) {
	return o.completeOnce(ctx, in)
}

func (o *OA) completeOnce(ctx context.Context, in CompleteInput) (CompleteOutput, error) {
	if in.IncludeReasoning || completesOnChat3(in) {
		return o.completeChat3(ctx, in)
	}
	if in.RequestKey != "" {
		return CompleteOutput{}, errors.New("gateway: request_key requires include_reasoning")
	}
	resume := in.OperationID != ""
	if resume {
		if len(in.OperationID) > 256 || in.Cursor == nil || *in.Cursor < 0 || in.Model != "" || in.Prompt != "" || in.Hosting != "" || in.Credential != "" || in.MaxOutput != 0 || in.Conversation != "" {
			return CompleteOutput{}, errors.New("gateway: resume requires only operation_id and nonnegative cursor")
		}
		if out, ok, err := o.resumeChat3(ctx, in.OperationID, *in.Cursor); ok {
			return out, err
		}
		c, err := o.machine.ResolveInferenceChatV2(ctx, facade.Requirements{Scope: facade.ScopeAny})
		if err != nil {
			return CompleteOutput{}, err
		}
		return waitCompletion(ctx, c, in.OperationID, *in.Cursor)
	}
	if in.Cursor != nil {
		return CompleteOutput{}, errors.New("gateway: cursor requires operation_id")
	}
	if in.Model == "" || in.Prompt == "" || len(in.Model) > 256 || len(in.Prompt) > 65536 || in.MaxOutput < 0 || in.MaxOutput > 4096 {
		return CompleteOutput{}, errors.New("gateway: invalid bounded inference request")
	}
	if (in.Hosting != "local" && in.Hosting != "hosted") || in.Hosting == "hosted" && !credentialName(in.Credential) || in.Hosting == "local" && in.Credential != "" {
		return CompleteOutput{}, errors.New("gateway: inference requires explicit local or named-credential hosted routing")
	}
	if in.Conversation != "" && (!validConversation(in.Conversation) || in.Hosting != "local") {
		return CompleteOutput{}, errors.New("gateway: conversation is a local call's label of at most 128 bytes without control characters")
	}
	if in.MaxOutput == 0 {
		in.MaxOutput = defaultMaxOutput
	}
	if in.Conversation != "" {
		if out, ok, err := o.completeLabelled(ctx, in); ok {
			return out, err
		}
	}
	scope := facade.ScopeLocal
	if in.Hosting == "hosted" {
		scope = facade.ScopeRemote
	}
	c, err := o.machine.ResolveInferenceChatV2(ctx, facade.Requirements{Scope: scope})
	if err != nil {
		return CompleteOutput{}, err
	}
	request := inferenceclient.RequestV2{Target: inferenceclient.Target{Model: in.Model},
		Content: inferenceclient.RequestContent{Messages: []inferenceclient.Message{{Role: inferenceclient.RoleUser, Parts: []inferenceclient.Part{{Kind: inferenceclient.PartKindText, Text: in.Prompt}}}}}}
	if in.Hosting == "hosted" {
		request.Content.Guarantees, request.Content.Credential = []inferenceclient.RequestGuarantee{inferenceclient.RequestGuaranteeHostedAllowed}, in.Credential
	} else {
		request.Content.Guarantees = []inferenceclient.RequestGuarantee{inferenceclient.RequestGuaranteeLocalOnly}
	}
	if in.MaxOutput > 0 {
		request.Content.Options = &inferenceclient.Options{MaxOutput: in.MaxOutput}
	}
	follow, admission, err := startCompletionAwaitingHost(ctx, in.Hosting == "local", func() (inferenceclient.AdmissionV2, error) {
		return c.Start(ctx, request)
	}, func(a inferenceclient.AdmissionV2) bool {
		return a.Outcome == inferenceclient.StartOutcomeUnavailable && activatingCompletionHost(a.Reason)
	})
	if err != nil {
		return CompleteOutput{}, err
	}
	if admission.Outcome != inferenceclient.StartOutcomeAccepted {
		return CompleteOutput{State: "ended", Outcome: admission.Outcome.String(), Reason: admission.Reason, StopReason: inferenceclient.StopReasonNoStop.String(),
			Message: o.plainRefusal(ctx, admission.Reason, in.Model, "chat")}, nil
	}
	return waitCompletion(follow, c, admission.Operation, 0)
}

// waitCompletion observes an existing operation under the native caller binding.
// Ending this wait preserves accepted work; explicit Cancel remains separate.
func waitCompletion(ctx context.Context, c *inferenceclient.ChatV2, operation string, cursor int64) (CompleteOutput, error) {
	until, bounded := followUntil(ctx)
	started := time.Now()
	pending, ended, received, observationFailed := false, false, false, false
	stream := func(yield func(inferenceclient.Delta, error) bool) {
		for {
			line, done, total := progressLine("Working", 0, 0, "", time.Since(started))
			reportProgress(ctx, line, done, total)
			wait, more := observeWait(until, bounded, inferenceclient.PageWait)
			if !more {
				pending = true
				yield(inferenceclient.Delta{Kind: inferenceclient.DeltaKindEnd, End: &inferenceclient.Reply{Outcome: inferenceclient.ReplyOutcomeCompleted}}, nil)
				return
			}
			page, err := c.Observe(ctx, operation, cursor, inferenceclient.PageDeltas, inferenceclient.PageBytes, wait.Milliseconds())
			if err != nil {
				observationFailed = true
				if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
					pending = true
					// A local folding sentinel retains bounded text already read.
					// It is never emitted as a native operation delta.
					yield(inferenceclient.Delta{Kind: inferenceclient.DeltaKindEnd, End: &inferenceclient.Reply{Outcome: inferenceclient.ReplyOutcomeCompleted}}, nil)
				} else {
					yield(inferenceclient.Delta{}, err)
				}
				return
			}
			if page.Outcome != inferenceclient.PageOutcomePage {
				observationFailed = true
				if page.Outcome == inferenceclient.PageOutcomeGap {
					yield(inferenceclient.Delta{}, inferenceclient.ErrGap)
				} else {
					yield(inferenceclient.Delta{}, &inferenceclient.PageRefusal{Outcome: page.Outcome})
				}
				return
			}
			received = received || len(page.Deltas) > 0
			for _, delta := range page.Deltas {
				if delta.Kind == inferenceclient.DeltaKindEnd {
					ended = true
				}
				if !yield(delta, nil) {
					return
				}
			}
			cursor = page.Next
			if page.AtEnd {
				ended = true
				return
			}
		}
	}
	reply, err := foldCompletion(stream)
	if err != nil {
		if received && !observationFailed && !ended && ctx.Err() == nil {
			cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			//unchecked: preserve the bounded local folding failure after best-effort cleanup
			c.Cancel(cancelCtx, operation)
			cancel()
		}
		return CompleteOutput{}, err
	}
	var text strings.Builder
	for _, part := range reply.Message.Parts {
		if part.Kind == inferenceclient.PartKindText {
			if text.Len()+len(part.Text) > maxCompletionBytes {
				return CompleteOutput{}, errors.New("gateway: inference result exceeds 262144-byte bound")
			}
			//unchecked: strings.Builder.WriteString never returns a non-nil error
			text.WriteString(part.Text)
		}
	}
	out := CompleteOutput{OperationID: operation, Cursor: cursor, State: "ended", Outcome: reply.Outcome.String(), Reason: reply.Reason, Text: text.String(), Host: reply.Host, Model: reply.Model, StopReason: reply.StopReason.String()}
	if pending {
		line, _, _ := progressLine("Working", 0, 0, "", time.Since(started))
		out.State, out.Outcome, out.Message, out.StopReason = "working", "pending", line+".", ""
	}
	return out, nil
}

type partFields struct {
	digest, mediaType, callID, name bool
}

// foldCompletion applies bounds before retaining each streamed delta. Ending
// the range on an error also ends Chat.Stream, whose deferred cleanup cancels
// an accepted operation that has not reached its terminal delta.
func foldCompletion(stream iter.Seq2[inferenceclient.Delta, error]) (inferenceclient.Reply, error) {
	var fold inferenceclient.Fold
	parts := make(map[int64]partFields)
	contentBytes, metadataBytes := 0, 0
	add := func(total *int, amount, limit int) bool {
		if amount < 0 || amount > limit-*total {
			return false
		}
		*total += amount
		return true
	}
	for delta, err := range stream {
		if err != nil {
			return inferenceclient.Reply{}, err
		}
		if delta.Kind == inferenceclient.DeltaKindPart && delta.Part != nil {
			fields, known := parts[delta.Index]
			if !known {
				if len(parts) >= maxCompletionParts {
					return inferenceclient.Reply{}, errors.New("gateway: inference result exceeds 256-part bound")
				}
				if !add(&metadataBytes, len(delta.Part.Kind.String()), maxCompletionMetadata) {
					return inferenceclient.Reply{}, errors.New("gateway: inference result metadata exceeds 65536-byte bound")
				}
			}
			if !add(&contentBytes, len(delta.Part.Text)+len(delta.Part.Arguments), maxCompletionBytes) {
				return inferenceclient.Reply{}, errors.New("gateway: inference result exceeds 262144-byte bound")
			}
			for _, field := range []struct {
				value string
				set   *bool
			}{{delta.Part.Digest, &fields.digest}, {delta.Part.MediaType, &fields.mediaType}, {delta.Part.CallID, &fields.callID}, {delta.Part.Name, &fields.name}} {
				if !*field.set && field.value != "" {
					if !add(&metadataBytes, len(field.value), maxCompletionMetadata) {
						return inferenceclient.Reply{}, errors.New("gateway: inference result metadata exceeds 65536-byte bound")
					}
					*field.set = true
				}
			}
			parts[delta.Index] = fields
		}
		if delta.Kind == inferenceclient.DeltaKindEnd && delta.End != nil {
			end := delta.End
			finalBytes := len(end.Outcome.String()) + len(end.Reason) + len(end.StopReason.String()) + len(end.Host) + len(end.Model)
			if end.Cost != nil {
				finalBytes += len(end.Cost.Currency)
			}
			if !add(&metadataBytes, finalBytes, maxCompletionMetadata) {
				return inferenceclient.Reply{}, errors.New("gateway: inference result metadata exceeds 65536-byte bound")
			}
		}
		fold.Add(delta)
	}
	reply, ok := fold.Reply()
	if !ok {
		return inferenceclient.Reply{}, inferenceclient.ErrInconsistent
	}
	return reply, nil
}

// credentialName is the credentials provider's one name rule.
func credentialName(value string) bool { return credentials.ValidName(value) }

type handleRecord struct {
	Version  int                    `json:"version"`
	Schema   string                 `json:"schema"`
	Binding  facade.JobsBinding     `json:"binding"`
	Identity facade.RequestIdentity `json:"identity"`
	// LocalVideo marks a local video job, whose clip the gateway saves when
	// it ends (video_watch.go).
	LocalVideo bool `json:"local_video,omitempty"`
}

type requestRecord struct {
	Version int    `json:"version"`
	Digest  string `json:"digest"`
	Handle  string `json:"handle"`
}

func randomHandle() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (o *OA) recordPath(handle string) (string, error) {
	if len(handle) != 32 {
		return "", errors.New("gateway: unknown handle")
	}
	if _, err := base64.RawURLEncoding.DecodeString(handle); err != nil {
		return "", errors.New("gateway: unknown handle")
	}
	return filepath.Join(o.stateDir, handle+".json"), nil
}

func (o *OA) save(ctx context.Context, handle string, rec handleRecord) error {
	path, err := o.recordPath(handle)
	if err != nil {
		return err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return o.state.WriteContext(ctx, path, casapi.Value{}, data)
}

func (o *OA) requestPath(key string) string {
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(o.stateDir, "request-"+fmt.Sprintf("%x", digest[:])+".json")
}

func jobDigest(in JobSubmitInput) (string, error) {
	in.RequestKey, in.WaitMS = "", 0
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	d := sha256.Sum256(b)
	return fmt.Sprintf("%x", d[:]), nil
}

func loadRequest(path string) (requestRecord, error) {
	b, err := readBounded(path, maxRequestRecordBytes)
	if err != nil {
		return requestRecord{}, err
	}
	var rec requestRecord
	if json.Unmarshal(b, &rec) != nil || rec.Version != handleRecordVersion || rec.Digest == "" || rec.Handle == "" {
		return requestRecord{}, errors.New("gateway: invalid request record")
	}
	return rec, nil
}

func (o *OA) load(handle string) (handleRecord, error) {
	path, err := o.recordPath(handle)
	if err != nil {
		return handleRecord{}, err
	}
	data, err := readBounded(path, maxHandleRecordBytes)
	if err != nil {
		return handleRecord{}, errors.New("gateway: unknown handle")
	}
	var rec handleRecord
	if json.Unmarshal(data, &rec) != nil || rec.Version != handleRecordVersion || rec.Schema != jobSchema {
		return handleRecord{}, errors.New("gateway: unsupported or invalid handle record")
	}
	return rec, nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < 0 || info.Size() > limit {
		return nil, errors.New("gateway: state record exceeds bound")
	}
	b := make([]byte, info.Size())
	if _, err := io.ReadFull(f, b); err != nil {
		return nil, err
	}
	return b, nil
}

// Submit submits or reconciles durable inference under its stable request key.
// The MCP tool middleware owns the one explicit permission retry.
func (o *OA) Submit(ctx context.Context, in JobSubmitInput) (JobSubmitOutput, error) {
	return o.submitOnce(ctx, in)
}

func (o *OA) submitOnce(ctx context.Context, in JobSubmitInput) (JobSubmitOutput, error) {
	_, knownProfile := jobwire.ParseProfile(in.Profile)
	if in.RequestKey == "" || len(in.RequestKey) > 128 || !utf8.ValidString(in.RequestKey) || in.Model == "" || in.Prompt == "" || len(in.Model) > 256 || len(in.Prompt) > 65536 || !knownProfile {
		return JobSubmitOutput{}, errors.New("gateway: invalid durable inference request")
	}
	if err := jobRouting(in); err != nil {
		return JobSubmitOutput{}, err
	}
	if err := jobSize(in); err != nil {
		return JobSubmitOutput{}, err
	}
	if in.Count == 0 {
		in.Count = 1
	}
	if in.Count < 1 || in.Count > 10 || in.DurationMS < 0 || in.DurationMS > 600000 || in.Profile == "video" && in.DurationMS == 0 {
		return JobSubmitOutput{}, errors.New("gateway: invalid durable inference bounds")
	}
	digest, err := jobDigest(in)
	if err != nil {
		return JobSubmitOutput{}, err
	}
	requestPath := o.requestPath(in.RequestKey)
	if existing, err := loadRequest(requestPath); err == nil {
		if existing.Digest != digest {
			return JobSubmitOutput{}, errors.New("gateway: request key already binds a different immutable specification")
		}
		return o.resubmit(ctx, existing.Handle, in)
	} else if !os.IsNotExist(err) {
		return JobSubmitOutput{}, err
	}
	if retry, _ := ctx.Value(permissionRetryKey{}).(bool); retry {
		return JobSubmitOutput{}, errors.New("gateway: permission retry lost its original request mapping")
	}
	return o.firstSubmit(ctx, requestPath, digest, in)
}

func (o *OA) firstSubmit(ctx context.Context, requestPath, digest string, in JobSubmitInput) (JobSubmitOutput, error) {
	need := facade.Requirements{Scope: facade.ScopeLocal, Guarantees: []string{inference.RecoverableUpstreamGuarantee}}
	jobs, err := o.machine.ResolveJobs(ctx, need)
	if err != nil {
		return JobSubmitOutput{}, err
	}
	history, err := jobs.GetHistoryWindow(ctx)
	if err != nil {
		return JobSubmitOutput{}, err
	}
	handle, err := randomHandle()
	if err != nil {
		return JobSubmitOutput{}, err
	}
	keyBytes := make([]byte, 24)
	if _, err := rand.Read(keyBytes); err != nil {
		return JobSubmitOutput{}, err
	}
	id := facade.RequestIdentity{Key: "mcp-" + base64.RawURLEncoding.EncodeToString(keyBytes), HistoryEpoch: history.HistoryEpoch}
	rec := handleRecord{Version: handleRecordVersion, Schema: jobSchema, Binding: jobs.Binding(), Identity: id,
		LocalVideo: in.Profile == profileWord(jobwire.ProfileVideo) && in.Hosting == "local"}
	if err := o.save(ctx, handle, rec); err != nil {
		return JobSubmitOutput{}, fmt.Errorf("gateway: persist recovery handle before submit: %w", err)
	}
	//unchecked: requestRecord is plain int/string fields, which json.Marshal cannot fail on
	mapping, _ := json.Marshal(requestRecord{Version: handleRecordVersion, Digest: digest, Handle: handle})
	if err := o.state.WriteContext(ctx, requestPath, casapi.Value{}, mapping); err != nil {
		// A concurrent process may have won creation. Its immutable mapping is
		// authoritative; reconcile it and leave this unreferenced handle inert.
		existing, readErr := loadRequest(requestPath)
		if readErr != nil {
			return JobSubmitOutput{}, err
		}
		if existing.Digest != digest {
			return JobSubmitOutput{}, errors.New("gateway: request key already binds a different immutable specification")
		}
		return o.resubmit(ctx, existing.Handle, in)
	}
	return o.submitWith(ctx, jobs, handle, id, in)
}

// resubmit presents a repeated request key's identical arguments again under
// its recorded handle and identity. A duplicate Submit returns the accepted
// job's receipt (JOB-A2), and a key the executor refused at admission is
// decided again, so it is admitted once the person answers its question
// (JOB-A16). Reconcile is never used here: it seals an identity with no
// journal against every later acceptance (JOB-A4), and an admission refusal
// leaves no journal.
func (o *OA) resubmit(ctx context.Context, handle string, in JobSubmitInput) (JobSubmitOutput, error) {
	jobs, rec, err := o.restored(ctx, JobHandleInput{Handle: handle})
	if err != nil {
		return JobSubmitOutput{}, err
	}
	return o.submitWith(ctx, jobs, handle, rec.Identity, in)
}

// Each refusal of a job's routing is terminal and says what to send instead,
// so a model does not guess credential names.
var (
	errLocalJobCredential  = errors.New("gateway: refused for good: local work needs no credential; send hosting local with no credential")
	errHostedJobCredential = errors.New("gateway: refused for good: hosted work needs the name of a credential the person registered in OA; for this computer's servers send hosting local with no credential")
	errJobHosting          = errors.New("gateway: refused for good: hosting must be local (this computer, no credential) or hosted (with a registered credential name)")
)

// jobRouting checks a job's routing: local with no credential runs on this
// computer's servers under the local-only guarantee (inference job
// executor, job_video_test's local video job); hosted names a credential.
func jobRouting(in JobSubmitInput) error {
	switch in.Hosting {
	case "local":
		if in.Credential != "" {
			return errLocalJobCredential
		}
	case "hosted":
		if !credentialName(in.Credential) {
			return errHostedJobCredential
		}
	default:
		return errJobHosting
	}
	return nil
}

// jobSizePattern is the size a job may name: auto or WIDTHxHEIGHT.
const jobSizePattern = `^(auto|[1-9][0-9]{0,4}x[1-9][0-9]{0,4})$`

var jobSizeShape = regexp.MustCompile(jobSizePattern)

// errJobSize is the refusal of a size of another shape, naming the values.
var errJobSize = errors.New("gateway: refused for good: size must be auto or WIDTHxHEIGHT such as 512x512; leave it out for auto")

// jobSize refuses at submit a size that is neither absent, auto nor
// WIDTHxHEIGHT. Which WIDTHxHEIGHT the engine makes is its own answer.
func jobSize(in JobSubmitInput) error {
	if in.Size != "" && !jobSizeShape.MatchString(in.Size) {
		return errJobSize
	}
	return nil
}

// jobRequest is the inference job document's request for in: local work is
// local-only with no credential, hosted work hosted-allowed with its
// credential.
func jobRequest(in JobSubmitInput) jobwire.Request {
	profile, _ := jobwire.ParseProfile(in.Profile)
	guarantee, credential := jobwire.RequestGuaranteeHostedAllowed, in.Credential
	if in.Hosting == "local" {
		guarantee, credential = jobwire.RequestGuaranteeLocalOnly, ""
	}
	size := in.Size
	if size == "" {
		size = "auto" // A picture's or video's size is auto unless the caller names one.
	}
	mode := jobwire.ModeGenerate
	if in.InputImage != "" {
		mode = jobwire.ModeEdit
	}
	return jobwire.Request{Profile: profile, DurationMs: in.DurationMS, Image: jobwire.ImageRequest{Model: in.Model, Mode: mode, Prompt: in.Prompt, Size: size, Count: in.Count,
		ImageDigest: in.InputImage, ImageMediaType: in.InputMediaType, MaskDigest: in.Mask, MaskMediaType: in.MaskMediaType,
		Guarantees: []jobwire.RequestGuarantee{guarantee}, Credential: credential}}
}

// jobSpec is the inference job document the gateway submits for in.
func jobSpec(in JobSubmitInput) []byte {
	request := jobRequest(in)
	return jobwire.Encode(&jobwire.Document{Request: &request})
}

func (o *OA) submitWith(ctx context.Context, jobs *facade.JobsClient, handle string, id facade.RequestIdentity, in JobSubmitInput) (JobSubmitOutput, error) {
	submission := facade.Submission{Identity: id, Kind: inference.InferenceJobKind, Spec: jobSpec(in), RequiredGuarantees: []string{inference.RecoverableUpstreamGuarantee}, Label: "MCP inference"}
	accepted, submitErr := jobs.Submit(ctx, submission)
	out := JobSubmitOutput{Handle: handle, Outcome: accepted.Outcome.String(), Reason: accepted.Reason}
	if submitErr != nil {
		out.Outcome, out.Reason = "unknown", "submission reply unavailable; use the handle to reconcile"
		return out, nil
	}
	if accepted.Receipt != nil {
		out.OperationID = accepted.Receipt.OperationID
		out.Guarantees = slices.Clone(accepted.Receipt.AcceptedGuarantees)
	}
	if out.Message == "" {
		out.Message = o.plainRefusal(ctx, out.Reason, in.Model, in.Profile)
	}
	if out.Outcome != "accepted" {
		o.keepRefusal(handle, jobRefusal{Outcome: out.Outcome, Reason: out.Reason, Message: out.Message})
	} else {
		o.keepRefusal(handle, jobRefusal{})
		if in.Profile == profileWord(jobwire.ProfileVideo) && in.Hosting == "local" {
			o.watchVideo(handle)
		}
	}
	return out, nil
}

// jobRefusal is a submit the runtime refused at once, kept beside its handle:
// such a job has no journal for diagnose to read (JOB-A16).
type jobRefusal struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

func (o *OA) refusalPath(handle string) (string, error) {
	path, err := o.recordPath(handle)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(path, ".json") + ".refused.json", nil
}

// keepRefusal records the latest refusal of handle's submit, or clears it
// once a submit is accepted. It is display data: a failed write is dropped.
func (o *OA) keepRefusal(handle string, refusal jobRefusal) {
	path, err := o.refusalPath(handle)
	if err != nil {
		return
	}
	if refusal.Outcome == "" {
		_ = os.Remove(path)
		return
	}
	//unchecked: jobRefusal is plain string fields, which json.Marshal cannot fail on
	data, _ := json.Marshal(refusal)
	_ = os.WriteFile(path, data, 0o600)
}

func (o *OA) refusalOf(handle string) (jobRefusal, bool) {
	path, err := o.refusalPath(handle)
	if err != nil {
		return jobRefusal{}, false
	}
	data, err := readBounded(path, maxRequestRecordBytes*4)
	if err != nil {
		return jobRefusal{}, false
	}
	var refusal jobRefusal
	if json.Unmarshal(data, &refusal) != nil || refusal.Outcome == "" {
		return jobRefusal{}, false
	}
	return refusal, true
}

// jobReasonSentence is the sentence a model passes to the person for a
// provider refusal word found in a job's reason or failure text, or "".
func jobReasonSentence(text string) string {
	if strings.Contains(text, "gpu:standby") {
		return "The computer is going to sleep; try again when it's awake."
	}
	return startFailedSentence(text)
}

func (o *OA) restored(ctx context.Context, in JobHandleInput) (*facade.JobsClient, handleRecord, error) {
	rec, err := o.load(in.Handle)
	if err != nil {
		return nil, handleRecord{}, err
	}
	jobs, err := o.machine.RestoreJobs(ctx, rec.Binding)
	return jobs, rec, err
}

type observedJob struct {
	jobs       *facade.JobsClient
	identity   facade.RequestIdentity
	result     facade.ObservationResult
	localVideo bool
}

func (o *OA) observeJob(ctx context.Context, in JobHandleInput) (observedJob, error) {
	jobs, rec, err := o.restored(ctx, in)
	if err != nil {
		return observedJob{}, err
	}
	observed, err := jobs.ObserveWork(ctx, rec.Identity)
	if err != nil {
		return observedJob{}, err
	}
	return observedJob{jobs: jobs, identity: rec.Identity, result: observed, localVideo: rec.LocalVideo}, nil
}

// Status reads a job's state and result by its handle. A finished video is
// saved in the person's Videos folder once; every later read answers the
// same path.
func (o *OA) Status(ctx context.Context, in JobHandleInput) (JobStatusOutput, error) {
	read := o.readStatus
	if o.statusAt != nil {
		read = o.statusAt
	}
	out, result, err := read(ctx, in)
	if err != nil {
		return JobStatusOutput{}, err
	}
	if result != nil && result.Profile == jobwire.ProfileVideo {
		reportProgress(ctx, "saving", 0, 0)
		out.Path, out.Message = o.saveVideo(ctx, in.Handle, result)
	}
	return out, nil
}

// readStatus observes the job and reads a complete job's result document.
func (o *OA) readStatus(ctx context.Context, in JobHandleInput) (JobStatusOutput, *jobwire.JobResult, error) {
	job, err := o.observeJob(ctx, in)
	if err != nil {
		return JobStatusOutput{}, nil, err
	}
	observed := job.result
	out := JobStatusOutput{Outcome: observed.Outcome.String()}
	if observed.Snapshot == nil {
		return out, nil, nil
	}
	s := observed.Snapshot
	out.State, out.Waiting = s.State.String(), s.Waiting
	out.Progress = map[string]int64{"done": s.Progress.Done, "total": s.Progress.Total}
	restarted := false
	if s.Failure != nil {
		// A retryable provider:lost on unfinished work is the runtime
		// sending the work again after a restart (inference INF-G7).
		restarted = s.State != facade.WorkStateFailed && s.Failure.Classification == facade.FailureClassRetryable && s.Failure.Message == "provider:lost"
		if !restarted {
			out.Failure = s.Failure.Message
			if sentence := jobReasonSentence(s.Failure.Message); sentence != "" {
				out.Failure = sentence
			}
		}
	}
	out.Phase = jobPhase(out.State, out.Waiting, s.Progress.Done, s.Progress.Total, job.localVideo)
	if restarted || out.Waiting == restartingWord {
		out.Message = restartedSentence
	}
	if s.State != facade.WorkStateComplete {
		return out, nil, nil
	}
	read, err := job.jobs.ReadResult(ctx, job.identity, 0, maxResultBytes)
	if err != nil {
		return JobStatusOutput{}, nil, err
	}
	out.ResultOutcome = read.Outcome.String()
	if read.Outcome != facade.ResultOutcomeData || read.Chunk == nil {
		return out, nil, nil
	}
	if !read.Chunk.EOF {
		return JobStatusOutput{}, nil, errors.New("gateway: durable result exceeds 65536-byte MCP bound")
	}
	doc, err := jobwire.Decode(read.Chunk.Data)
	if err != nil || doc.Result == nil {
		return JobStatusOutput{}, nil, errors.New("gateway: invalid durable inference result")
	}
	out.Result = doc.Result
	return out, doc.Result, nil
}

func (o *OA) Diagnose(ctx context.Context, in JobHandleInput) (JobDiagnosisOutput, error) {
	job, err := o.observeJob(ctx, in)
	if err != nil {
		return JobDiagnosisOutput{}, err
	}
	out := diagnosisFromObservation(job.result)
	// A submit refused at once left no operation: its refusal says why.
	if job.result.Snapshot == nil {
		if refusal, ok := o.refusalOf(in.Handle); ok {
			out.Outcome, out.Reason = refusal.Outcome, refusal.Reason
			out.Guidance = refusal.Message
			if out.Guidance == "" {
				out.Guidance = fmt.Sprintf("OpenAbstractions refused this job when it was submitted: %s.", refusal.Reason)
			}
		}
	}
	return out, nil
}

func diagnosisFromObservation(observed facade.ObservationResult) JobDiagnosisOutput {
	out := JobDiagnosisOutput{
		Scope:   "the durable inference job addressed by this handle under this MCP adapter's OA authorization",
		Outcome: observed.Outcome.String(),
	}
	if observed.Snapshot == nil {
		out.Guidance = "OA could not provide an operation snapshot. Check the handle and OA availability, then ask OA to observe it again."
		return out
	}
	s := observed.Snapshot
	out.State = s.State.String()
	out.Waiting = s.Waiting != ""
	out.Progress = map[string]int64{"done": s.Progress.Done, "total": s.Progress.Total}
	if s.Failure == nil {
		if out.Waiting {
			out.Guidance = "The operation is waiting on a provider-reported condition. Check the provider's status for details; this diagnosis does not change the operation."
		} else {
			out.Guidance = "OA reports no failure for this operation. Use the status tool for its current state and result."
		}
		return out
	}

	class := s.Failure.Classification.String()
	if class == "" {
		class = "unknown"
	}
	out.FailureClass = class
	out.FailureCause = safeFailureCause(string(s.Failure.Cause))
	out.Guidance = diagnosisGuidance(class, out.FailureCause)
	return out
}

func safeFailureCause(cause string) string {
	for _, member := range facade.FailureCauseValues() {
		if cause == string(member) {
			return cause
		}
	}
	return "other"
}

func diagnosisGuidance(class, cause string) string {
	if class == facade.FailureClassRetryable.String() {
		return "The provider classifies this failure as retryable. Check provider availability and decide whether to retry; diagnosis never retries automatically."
	}
	if class == facade.FailureClassPermanent.String() {
		switch cause {
		case string(facade.FailureCauseCredential):
			return "The provider reports a credential problem. Review the authorized credential setup in OA; no credential value is shown."
		case string(facade.FailureCauseUnauthorized), string(facade.FailureCauseRefused):
			return "The provider refused the operation. Review its authorization or policy settings before submitting again."
		case string(facade.FailureCauseTransport):
			return "The provider reports a transport failure. Check that the selected provider is reachable and available."
		case string(facade.FailureCauseServerError):
			return "The provider reports a server-side failure. Check the provider's own status before submitting again."
		case string(facade.FailureCauseResultLost):
			return "The operation result is unavailable. Check provider status; submitting again could duplicate work."
		default:
			return "The provider reports a permanent failure. Check provider status for details before submitting again."
		}
	}
	return "OA reports an unknown failure classification. Check provider status for details before taking action."
}

func (o *OA) Cancel(ctx context.Context, in JobHandleInput) (JobCancelOutput, error) {
	jobs, rec, err := o.restored(ctx, in)
	if err != nil {
		return JobCancelOutput{}, err
	}
	result, err := jobs.CancelWork(ctx, rec.Identity)
	if err != nil {
		return JobCancelOutput{}, err
	}
	return JobCancelOutput{Outcome: result.Outcome.String()}, nil
}
