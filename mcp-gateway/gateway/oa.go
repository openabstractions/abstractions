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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	casapi "github.com/openabstractions/abstraction-cas/go/api"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	inference "github.com/openabstractions/abstraction-inference/go"
	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"
	inferenceclient "github.com/openabstractions/abstraction-inference/go/client"
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
	out := ModelsOutput{ObservedUnixMS: time.Now().UnixMilli(), Models: make([]Model, 0, len(snapshot.Models))}
	for _, family := range snapshot.Models {
		m := Model{Family: family.Family}
		profiles := map[string]bool{}
		for _, alias := range family.Names {
			if !alias.Servable {
				continue
			}
			m.Aliases = append(m.Aliases, alias.Name)
			for _, profile := range alias.Profiles {
				profiles[profile] = true
			}
		}
		slices.Sort(m.Aliases)
		for profile := range profiles {
			m.Profiles = append(m.Profiles, profile)
		}
		slices.Sort(m.Profiles)
		if len(m.Aliases) > 0 {
			out.Models = append(out.Models, m)
		}
	}
	slices.SortFunc(out.Models, func(a, b Model) int { return strings.Compare(a.Family, b.Family) })
	return out, nil
}

func (o *OA) Complete(ctx context.Context, in CompleteInput) (CompleteOutput, error) {
	if in.Model == "" || in.Prompt == "" || len(in.Model) > 256 || len(in.Prompt) > 65536 || in.MaxOutput < 0 || in.MaxOutput > 4096 {
		return CompleteOutput{}, errors.New("gateway: invalid bounded inference request")
	}
	if (in.Hosting != "local" && in.Hosting != "hosted") || in.Hosting == "hosted" && !credentialName(in.Credential) || in.Hosting == "local" && in.Credential != "" {
		return CompleteOutput{}, errors.New("gateway: inference requires explicit local or named-credential hosted routing")
	}
	if in.MaxOutput == 0 {
		in.MaxOutput = defaultMaxOutput
	}
	scope := facade.ScopeLocal
	if in.Hosting == "hosted" {
		scope = facade.ScopeRemote
	}
	c, err := o.machine.ResolveInference(ctx, facade.Requirements{Scope: scope})
	if err != nil {
		return CompleteOutput{}, err
	}
	request := inferenceclient.Request{Model: in.Model, Messages: []inferenceclient.Message{{Role: inferenceclient.RoleUser, Parts: []inferenceclient.Part{{Kind: inferenceclient.PartKindText, Text: in.Prompt}}}}}
	if in.Hosting == "hosted" {
		request.Guarantees, request.Credential = []inferenceclient.RequestGuarantee{inferenceclient.RequestGuaranteeHostedAllowed}, in.Credential
	} else {
		request.Guarantees = []inferenceclient.RequestGuarantee{inferenceclient.RequestGuaranteeLocalOnly}
	}
	if in.MaxOutput > 0 {
		request.Options = &inferenceclient.Options{MaxOutput: in.MaxOutput}
	}
	reply, err := foldCompletion(c.Stream(ctx, request))
	if err != nil {
		return CompleteOutput{}, err
	}
	var text strings.Builder
	for _, part := range reply.Message.Parts {
		if part.Kind == inferenceclient.PartKindText {
			if text.Len()+len(part.Text) > maxCompletionBytes {
				return CompleteOutput{}, errors.New("gateway: inference result exceeds 262144-byte bound")
			}
			text.WriteString(part.Text)
		}
	}
	return CompleteOutput{Outcome: reply.Outcome.String(), Reason: reply.Reason, Text: text.String(), Host: reply.Host, Model: reply.Model, StopReason: reply.StopReason.String()}, nil
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

func credentialName(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

type handleRecord struct {
	Version  int                    `json:"version"`
	Schema   string                 `json:"schema"`
	Binding  facade.JobsBinding     `json:"binding"`
	Identity facade.RequestIdentity `json:"identity"`
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
	in.RequestKey = ""
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

func (o *OA) Submit(ctx context.Context, in JobSubmitInput) (JobSubmitOutput, error) {
	if in.RequestKey == "" || len(in.RequestKey) > 128 || !utf8.ValidString(in.RequestKey) || in.Model == "" || in.Prompt == "" || len(in.Model) > 256 || len(in.Prompt) > 65536 || (in.Profile != "image_batch" && in.Profile != "video") {
		return JobSubmitOutput{}, errors.New("gateway: invalid durable inference request")
	}
	if in.Hosting != "hosted" || !credentialName(in.Credential) {
		return JobSubmitOutput{}, errors.New("gateway: durable inference requires hosted routing and an authorized named credential")
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
		return o.reconcileOrSubmit(ctx, existing.Handle, in)
	} else if !os.IsNotExist(err) {
		return JobSubmitOutput{}, err
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
	rec := handleRecord{Version: handleRecordVersion, Schema: jobSchema, Binding: jobs.Binding(), Identity: id}
	if err := o.save(ctx, handle, rec); err != nil {
		return JobSubmitOutput{}, fmt.Errorf("gateway: persist recovery handle before submit: %w", err)
	}
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
		return o.reconcileOrSubmit(ctx, existing.Handle, in)
	}
	return o.submitWith(ctx, jobs, handle, id, in)
}

func (o *OA) reconcileOrSubmit(ctx context.Context, handle string, in JobSubmitInput) (JobSubmitOutput, error) {
	rec, err := o.load(handle)
	if err != nil {
		return JobSubmitOutput{}, err
	}
	jobs, err := o.machine.RestoreJobs(ctx, rec.Binding)
	if err != nil {
		return JobSubmitOutput{}, err
	}
	result, err := jobs.Reconcile(ctx, rec.Identity)
	if err != nil {
		return JobSubmitOutput{}, err
	}
	if result.Outcome == facade.AcceptanceOutcomeDefinitelyNotAccepted {
		return o.submitWith(ctx, jobs, handle, rec.Identity, in)
	}
	out := JobSubmitOutput{Handle: handle, Outcome: result.Outcome.String(), Reason: result.Reason}
	if result.Receipt != nil {
		out.OperationID = result.Receipt.OperationID
		out.Guarantees = slices.Clone(result.Receipt.AcceptedGuarantees)
	}
	return out, nil
}

func (o *OA) submitWith(ctx context.Context, jobs *facade.JobsClient, handle string, id facade.RequestIdentity, in JobSubmitInput) (JobSubmitOutput, error) {
	profile, _ := jobwire.ParseProfile(in.Profile)
	request := jobwire.Request{Profile: profile, DurationMs: in.DurationMS, Image: jobwire.ImageRequest{Model: in.Model, Mode: jobwire.ModeGenerate, Prompt: in.Prompt, Size: in.Size, Count: in.Count, Guarantees: []jobwire.RequestGuarantee{jobwire.RequestGuaranteeHostedAllowed}, Credential: in.Credential}}
	submission := facade.Submission{Identity: id, Kind: inference.InferenceJobKind, Spec: jobwire.Encode(&jobwire.Document{Request: &request}), RequiredGuarantees: []string{inference.RecoverableUpstreamGuarantee}, Label: "MCP inference"}
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
	return out, nil
}

func (o *OA) restored(ctx context.Context, in JobHandleInput) (*facade.JobsClient, handleRecord, error) {
	rec, err := o.load(in.Handle)
	if err != nil {
		return nil, handleRecord{}, err
	}
	jobs, err := o.machine.RestoreJobs(ctx, rec.Binding)
	return jobs, rec, err
}

func (o *OA) Status(ctx context.Context, in JobHandleInput) (JobStatusOutput, error) {
	jobs, rec, err := o.restored(ctx, in)
	if err != nil {
		return JobStatusOutput{}, err
	}
	observed, err := jobs.ObserveWork(ctx, rec.Identity)
	if err != nil {
		return JobStatusOutput{}, err
	}
	out := JobStatusOutput{Outcome: observed.Outcome.String()}
	if observed.Snapshot == nil {
		return out, nil
	}
	s := observed.Snapshot
	out.State, out.Waiting = s.State.String(), s.Waiting
	out.Progress = map[string]int64{"done": s.Progress.Done, "total": s.Progress.Total}
	if s.Failure != nil {
		out.Failure = s.Failure.Message
	}
	if s.State == facade.WorkStateComplete {
		read, err := jobs.ReadResult(ctx, rec.Identity, 0, maxResultBytes)
		if err != nil {
			return JobStatusOutput{}, err
		}
		out.ResultOutcome = read.Outcome.String()
		if read.Outcome != facade.ResultOutcomeData {
			return out, nil
		}
		if read.Chunk != nil && !read.Chunk.EOF {
			return JobStatusOutput{}, errors.New("gateway: durable result exceeds 65536-byte MCP bound")
		}
		if read.Chunk != nil {
			doc, err := jobwire.Decode(read.Chunk.Data)
			if err != nil || doc.Result == nil {
				return JobStatusOutput{}, errors.New("gateway: invalid durable inference result")
			}
			out.Result = doc.Result
		}
	}
	return out, nil
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
