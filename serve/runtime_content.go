package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	cas "github.com/openabstractions/abstraction-cas/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	logging "github.com/openabstractions/abstraction-logging/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	storage "github.com/openabstractions/abstraction-storage/go"
	digestfmt "github.com/openabstractions/abstraction-storage/go/digest"
	service "github.com/openabstractions/abstraction-storage/go/service"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const contentReadAction = "abstraction.storage/content.read"
const contentWriteAction = "abstraction.storage/content.write"
const runtimeContentLimit int64 = 16 << 20
const runtimeGeneratedContentLimit int64 = 64 << 20
const speechOutputResource = "output:speech"
const generatedProvenanceLimit int64 = 64 << 10

type generatedOutput struct {
	Account string `json:"account"`
	Program string `json:"program"`
	Digest  string `json:"digest"`
	Profile string `json:"profile"`
	// Published is the last publication of these bytes by this subject under
	// this profile, RFC 3339 in UTC. An entry written before generated output
	// was tmp has none; the first generated-output pass writes it (INF-V5).
	Published string `json:"published,omitempty"`
	// RetentionMs is the producing operation's result retention.
	RetentionMs int64 `json:"retention_ms,omitempty"`
}

// sameOutput reports whether two entries name one subject's output of one
// profile; publication times may differ.
func sameOutput(a, b generatedOutput) bool {
	return a.Account == b.Account && a.Program == b.Program && a.Digest == b.Digest && a.Profile == b.Profile
}

type generatedOutputIndex struct {
	Version int               `json:"version"`
	Outputs []generatedOutput `json:"outputs"`
}

func (c *runtimeContent) generatedPath(digest string) string {
	return filepath.Join(c.provenance, digest[7:9], digest[7:]+".json")
}

func decodeGeneratedOutputs(raw []byte) (generatedOutputIndex, error) {
	if len(raw) == 0 {
		return generatedOutputIndex{Version: 1}, nil
	}
	var index generatedOutputIndex
	if err := json.Unmarshal(raw, &index); err != nil || index.Version != 1 {
		return generatedOutputIndex{}, errors.New("content: invalid generated output provenance")
	}
	for _, output := range index.Outputs {
		// The program was normalized when the entry was written; a read checks
		// its syntax only (rights validStoredSubject).
		if output.Account == "" || !identity.ValidSubjectProgram(output.Program) || !validGeneratedDigest(output.Digest) || !validOutputProfile(output.Profile) ||
			(output.Published != "" && !validRecordTime(output.Published)) || output.RetentionMs < 0 {
			return generatedOutputIndex{}, errors.New("content: invalid generated output provenance")
		}
	}
	return index, nil
}

// validOutputProfile names the output profiles generated content is written
// and read back under: speech, live, an image's and a video job's clip
// (inference INF-S7).
func validOutputProfile(profile string) bool {
	return profile == "speech" || profile == "live" || profile == inference.OutputImage || profile == inference.OutputVideo
}

func validGeneratedDigest(digest string) bool {
	if len(digest) != 71 || digest[:7] != "sha256:" {
		return false
	}
	_, err := hex.DecodeString(digest[7:])
	return err == nil
}

func (c *runtimeContent) recordGenerated(ctx context.Context, entry generatedOutput) error {
	return cas.ChangeLimitContext(ctx, c.generatedPath(entry.Digest), generatedProvenanceLimit, func(raw []byte) ([]byte, error) {
		index, err := decodeGeneratedOutputs(raw)
		if err != nil {
			return nil, err
		}
		// A publication of bytes already named restarts their clock: the
		// lifetime runs from the last time the output was made (INF-V5).
		for i, existing := range index.Outputs {
			if sameOutput(existing, entry) {
				index.Outputs[i] = entry
				return json.Marshal(index)
			}
		}
		index.Outputs = append(index.Outputs, entry)
		return json.Marshal(index)
	})
}

// Generated provenance records publication, not a right. Both the exact
// digest decision and the current profile decision are evaluated on every read.
// inputProfile names the input profile of the request the runtime reads for
// (inference.InputProfileFrom); only such a read honors the subject's own
// request input of that profile (STO-W12, INF-V4). "" honors none.
// The storage reader decides the IPC open and every chunk here, as a composed
// read does; each permitted decision marks the digest as being read, which
// holds it from the generated-output pass (runtime_content_outputs.go).
func (c *runtimeContent) authorizeRead(ctx context.Context, subject rwire.Subject, digest, inputProfile string) error {
	err := c.decideRead(ctx, subject, digest, inputProfile)
	if err == nil {
		c.noteRead(digest)
	}
	return err
}

func (c *runtimeContent) decideRead(ctx context.Context, subject rwire.Subject, digest, inputProfile string) error {
	if c == nil || c.rights == nil || subject.Account != c.rights.owner || !identity.ValidSubjectProgram(subject.Program) {
		return errors.New("content access denied")
	}
	switch c.decision(ctx, subject, contentReadAction, digest) {
	case rwire.DecisionOutcomePermitted:
		return nil
	case rwire.DecisionOutcomeNotGranted:
		// Only an absent exact-digest rule can use generated-output provenance.
	case rwire.DecisionOutcomeUnavailable:
		return service.ErrPolicyUnavailable
	default:
		return errors.New("content access denied")
	}
	if !validGeneratedDigest(digest) {
		return errors.New("content access denied")
	}
	raw, err := cas.ReadLimitContext(ctx, c.generatedPath(digest), generatedProvenanceLimit)
	if err != nil {
		return service.ErrPolicyUnavailable
	}
	index, err := decodeGeneratedOutputs(raw)
	if err != nil {
		return service.ErrPolicyUnavailable
	}
	unavailable := false
	for _, entry := range index.Outputs {
		if entry.Account != subject.Account || !identity.SameSubjectProgram(entry.Program, subject.Program) || entry.Digest != digest {
			continue
		}
		switch c.decision(ctx, subject, contentReadAction, "output:"+entry.Profile) {
		case rwire.DecisionOutcomePermitted:
			return nil
		case rwire.DecisionOutcomeUnavailable:
			unavailable = true
		}
	}
	if validInputProfile(inputProfile) {
		switch permitted, err := c.authorizeInputRead(ctx, subject, digest, inputProfile); {
		case err != nil:
			return err
		case permitted:
			return nil
		}
	}
	if unavailable {
		return service.ErrPolicyUnavailable
	}
	return errors.New("content access denied")
}

type runtimeContent struct {
	mu                sync.Mutex
	store             *storage.ContentStore
	rights            *runtimeRights
	endpoint, records string
	provenance        string
	// inputs holds the request input records (runtime_content_inputs.go).
	inputs   string
	inputsMu sync.Mutex
	holders  []inputHolder
	// outputRetention is the result retention of the operations that publish
	// generated output; zero reads defaultOutputRetention.
	outputRetention time.Duration
	// readTmp reads the person's tmp_max_retention for one generated-output
	// pass, with the configuration level that set it; nil removes nothing.
	readTmp func(context.Context) (time.Duration, string, error)
	// lastOutputPass and outputCursor pace the generated-output pass
	// (runtime_content_outputs.go); only the retention loop touches them.
	lastOutputPass time.Time
	outputCursor   string
	// reads is the last permitted read decision of each digest.
	readsMu sync.Mutex
	reads   map[string]time.Time
	now     func() time.Time
	sink    logging.Sink
	report  func(error)
	// checkArchiveRestoreSpace replaces the local Download space check in fixtures.
	checkArchiveRestoreSpace func(string, int64) error
	// decide replaces the rights decision in tests; nil uses rights.
	decide func(ctx context.Context, subject rwire.Subject, action, resource string) rwire.DecisionOutcome
}

// write stores bytes the gateway window received as request input. The
// verified bytes are decided as an IPC upload's publication is (STO-W12).
// The store lock is held from the presence check through publication, so a
// request input record owns only bytes this write published and the sweep
// never decides between them.
func (c *runtimeContent) write(ctx context.Context, subject inference.Subject, digest string, data []byte) inference.ContentOutcome {
	if c == nil || c.rights == nil {
		return inference.ContentUnavailable
	}
	if subject.Account != c.rights.owner || subject.Program == "" {
		return inference.ContentForbidden
	}
	if int64(len(data)) > runtimeContentLimit {
		return inference.ContentTooLarge
	}
	sum := sha256.Sum256(data)
	if digest != digestfmt.Hex(sum[:]) {
		return inference.ContentUnknown
	}
	outcome, profile := c.publishInput(ctx, subject, digest, data)
	if profile != "" {
		// The upload was refused for want of its input profile: the paired
		// first-use question is admitted after the store lock is released,
		// and the window's hold names and waits on it (INF-V4, INF-W10,
		// INF-W11).
		c.rights.askInput(ctx, subject, profile)
	}
	return outcome
}

// publishInput decides and publishes one window upload under the store lock.
// A refusal for want of the input profile its head names reports that
// profile.
func (c *runtimeContent) publishInput(ctx context.Context, subject inference.Subject, digest string, data []byte) (inference.ContentOutcome, string) {
	head := data[:min(len(data), service.HeadBytes)]
	c.mu.Lock()
	defer c.mu.Unlock()
	_, existing := c.store.Find(digest)
	profile, err := c.authorizeWriteProfile(ctx, rwire.Subject{Account: subject.Account, Program: subject.Program}, digest,
		service.WriteCall{Stage: service.WritePublish, Size: int64(len(data)), Head: head, Existing: existing})
	switch {
	case err == nil:
	case errors.Is(err, service.ErrPolicyUnavailable):
		return inference.ContentUnavailable, ""
	default:
		return inference.ContentForbidden, profile
	}
	if ctx.Err() != nil {
		return inference.ContentUnavailable, ""
	}
	return c.commitBytesLocked(digest, data), ""
}

func (c *runtimeContent) commitBytes(digest string, data []byte) inference.ContentOutcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commitBytesLocked(digest, data)
}

// commitBytesLocked publishes verified bytes; the caller holds c.mu.
func (c *runtimeContent) commitBytesLocked(digest string, data []byte) inference.ContentOutcome {
	if _, ok := c.store.Find(digest); ok {
		return inference.ContentResolved
	}
	ref, err := c.store.Place(digest, int64(len(data)))
	if err != nil {
		return inference.ContentUnavailable
	}
	path := c.store.Path(ref)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return inference.ContentUnavailable
	}
	if _, err = f.Write(data); err != nil {
		//unchecked: best-effort cleanup on a path that already returns ContentUnavailable
		_ = f.Close()
		//unchecked: best-effort cleanup on a path that already returns ContentUnavailable
		_ = os.Remove(path)
		return inference.ContentUnavailable
	}
	if err = f.Close(); err != nil {
		//unchecked: best-effort cleanup on a path that already returns ContentUnavailable
		_ = os.Remove(path)
		return inference.ContentUnavailable
	}
	if err := c.store.Commit(ref); err != nil {
		//unchecked: best-effort cleanup on a path that already returns ContentUnavailable
		_ = os.Remove(path)
		return inference.ContentUnavailable
	}
	return inference.ContentResolved
}

// prepareWrite authorizes generated output before paid work and returns a
// single-use commit capability that rechecks the same rule for revocation.
// Every generated output passes here, a synchronous call's (image@1,
// speech@1) and a job's alike, so a preflight refused for want of the output
// profile admits the paired first-use question here (askOutput, INF-S7). The
// call is refused at once and succeeds on retry after an Allow. The recheck
// at publication asks nothing.
func (c *runtimeContent) prepareWrite(ctx context.Context, subject inference.Subject, profile, mediaType string, maxBytes int64) (inference.ContentCommitter, inference.ContentOutcome) {
	if c == nil || c.rights == nil || !validOutputProfile(profile) || mediaType == "" || maxBytes < 1 || maxBytes > runtimeGeneratedContentLimit {
		return nil, inference.ContentUnavailable
	}
	if subject.Account != c.rights.owner || !identity.ValidSubjectProgram(subject.Program) {
		return nil, inference.ContentForbidden
	}
	bound := rwire.Subject{Account: subject.Account, Program: subject.Program}
	authorized := func(ctx context.Context) inference.ContentOutcome {
		for _, action := range []string{contentWriteAction, contentReadAction} {
			switch c.rights.decide(ctx, bound, action, "output:"+profile).Outcome {
			case rwire.DecisionOutcomePermitted:
			case rwire.DecisionOutcomeUnavailable:
				return inference.ContentUnavailable
			default:
				return inference.ContentForbidden
			}
		}
		return inference.ContentResolved
	}
	if outcome := authorized(ctx); outcome != inference.ContentResolved {
		if outcome == inference.ContentForbidden {
			c.rights.askOutput(ctx, subject, profile)
		}
		return nil, outcome
	}
	var mu sync.Mutex
	used := false
	return func(commitCtx context.Context, digest string, data []byte) inference.ContentOutcome {
		mu.Lock()
		defer mu.Unlock()
		if used {
			return inference.ContentUnavailable
		}
		used = true
		if commitCtx.Err() != nil {
			return inference.ContentUnavailable
		}
		if int64(len(data)) > maxBytes || int64(len(data)) > runtimeGeneratedContentLimit {
			return inference.ContentTooLarge
		}
		if outcome := authorized(commitCtx); outcome != inference.ContentResolved {
			return outcome
		}
		sum := sha256.Sum256(data)
		if digest != digestfmt.Hex(sum[:]) {
			return inference.ContentUnknown
		}
		// The bytes and their publication time are written under the store
		// lock the generated-output pass removes under, so the pass never
		// removes bytes a publication has just named.
		c.mu.Lock()
		defer c.mu.Unlock()
		if outcome := c.commitBytesLocked(digest, data); outcome != inference.ContentResolved {
			return outcome
		}
		entry := generatedOutput{Account: subject.Account, Program: identity.NormalizeSubjectProgram(subject.Program), Digest: digest, Profile: profile,
			Published: c.stamp(), RetentionMs: c.outputRetentionMs()}
		if err := c.recordGenerated(commitCtx, entry); err != nil {
			return inference.ContentUnavailable
		}
		return inference.ContentResolved
	}, inference.ContentResolved
}

// runtimeContentEndpoint is the one name of the storage content listener.
func runtimeContentEndpoint(options runtimeFlags) (string, error) {
	return options.companionEndpoint("storage-content-v1", "", "content")
}

func composeContent(options runtimeFlags, state string, rights *runtimeRights) (*runtimeContent, error) {
	root := filepath.Join(state, "content")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	store, err := storage.NewContentStore("runtime-content", root)
	if err != nil {
		return nil, err
	}
	endpoint, err := runtimeContentEndpoint(options)
	if err != nil {
		return nil, err
	}
	return &runtimeContent{store: store, rights: rights, endpoint: endpoint, records: filepath.Join(root, "writer-requests.json"), provenance: filepath.Join(root, "generated-outputs"),
		inputs: filepath.Join(root, "request-inputs")}, nil
}

func (c *runtimeContent) configure(o *host.Options) {
	if c == nil {
		return
	}
	o.Storage = c.store
	o.StorageEndpoint = c.endpoint
	o.StoragePolicy = func(ctx context.Context, peer *identity.Peer, digest string) error {
		subject, err := rightsclient.SubjectFromPeer(peer)
		if err != nil {
			return service.ErrPolicyUnavailable
		}
		return c.authorizeRead(ctx, subject, digest, "")
	}
	// Each writer call names its stage (STO-W11); an exact-digest rule or an
	// input profile decides it (STO-W12).
	o.StorageWritePolicy = func(ctx context.Context, peer *identity.Peer, digest string) error {
		subject, err := rightsclient.SubjectFromPeer(peer)
		if err != nil {
			return service.ErrPolicyUnavailable
		}
		call, _ := service.WriteCallFrom(ctx)
		return c.authorizeWrite(ctx, subject, digest, call)
	}
	o.StorageWriteLimit = runtimeContentLimit
	o.StorageWriteRecordPath = c.records
	o.RightsActions = append(o.RightsActions, contentReadAction, contentWriteAction)
}

func (c *runtimeContent) read(ctx context.Context, subject inference.Subject, digest string, limit int64) ([]byte, inference.ContentOutcome) {
	if c == nil || c.rights == nil {
		return nil, inference.ContentUnavailable
	}
	if subject.Account != c.rights.owner || subject.Program == "" {
		return nil, inference.ContentForbidden
	}
	if limit > runtimeGeneratedContentLimit {
		limit = runtimeGeneratedContentLimit
	}
	data, status := service.ReadAuthorized(ctx, c.store, func(ctx context.Context, digest string) error {
		return c.authorizeRead(ctx, rwire.Subject{Account: subject.Account, Program: subject.Program}, digest, inference.InputProfileFrom(ctx))
	}, digest, limit)
	switch status {
	case "read":
		return data, inference.ContentResolved
	case "not_found", "invalid":
		return nil, inference.ContentUnknown
	case "forbidden":
		return nil, inference.ContentForbidden
	case "too_large":
		return nil, inference.ContentTooLarge
	default:
		return nil, inference.ContentUnavailable
	}
}
