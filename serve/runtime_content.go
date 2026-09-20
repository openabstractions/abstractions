package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	storage "github.com/openabstractions/abstraction-storage/go"
	service "github.com/openabstractions/abstraction-storage/go/service"
	"os"
	"path/filepath"
	"sync"
)

const contentReadAction = "abstraction.storage/content.read"
const contentWriteAction = "abstraction.storage/content.write"
const runtimeContentLimit int64 = 16 << 20
const runtimeGeneratedContentLimit int64 = 64 << 20
const speechOutputResource = "output:speech"

type runtimeContent struct {
	mu                sync.Mutex
	store             *storage.ContentStore
	rights            *runtimeRights
	endpoint, records string
}

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
	if digest != "sha256:"+hex.EncodeToString(sum[:]) {
		return inference.ContentUnknown
	}
	decision := c.rights.decide(ctx, rwire.Subject{Account: subject.Account, Program: subject.Program}, contentWriteAction, digest)
	switch decision.Outcome {
	case rwire.DecisionOutcomePermitted:
	case rwire.DecisionOutcomeUnavailable:
		return inference.ContentUnavailable
	default:
		return inference.ContentForbidden
	}
	if ctx.Err() != nil {
		return inference.ContentUnavailable
	}
	return c.commitBytes(digest, data)
}

func (c *runtimeContent) commitBytes(digest string, data []byte) inference.ContentOutcome {
	c.mu.Lock()
	defer c.mu.Unlock()
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
		_ = f.Close()
		_ = os.Remove(path)
		return inference.ContentUnavailable
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(path)
		return inference.ContentUnavailable
	}
	if err := c.store.Commit(ref); err != nil {
		_ = os.Remove(path)
		return inference.ContentUnavailable
	}
	return inference.ContentResolved
}

// prepareWrite authorizes generated output before paid work and returns a
// single-use commit capability that rechecks the same rule for revocation.
func (c *runtimeContent) prepareWrite(ctx context.Context, subject inference.Subject, profile, mediaType string, maxBytes int64) (inference.ContentCommitter, inference.ContentOutcome) {
	if c == nil || c.rights == nil || (profile != "speech" && profile != "live" && profile != "image") || mediaType == "" || maxBytes < 1 || maxBytes > runtimeGeneratedContentLimit {
		return nil, inference.ContentUnavailable
	}
	if subject.Account != c.rights.owner || subject.Program == "" {
		return nil, inference.ContentForbidden
	}
	bound := rwire.Subject{Account: subject.Account, Program: subject.Program}
	authorized := func(ctx context.Context) inference.ContentOutcome {
		switch c.rights.decide(ctx, bound, contentWriteAction, "output:"+profile).Outcome {
		case rwire.DecisionOutcomePermitted:
			return inference.ContentResolved
		case rwire.DecisionOutcomeUnavailable:
			return inference.ContentUnavailable
		default:
			return inference.ContentForbidden
		}
	}
	if outcome := authorized(ctx); outcome != inference.ContentResolved {
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
		if digest != "sha256:"+hex.EncodeToString(sum[:]) {
			return inference.ContentUnknown
		}
		return c.commitBytes(digest, data)
	}, inference.ContentResolved
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
	endpoint := options.endpoint + "-content"
	if options.endpoint == "" {
		endpoint, err = bootstrap.Endpoint("storage-content-v1")
	}
	if err != nil {
		return nil, err
	}
	return &runtimeContent{store: store, rights: rights, endpoint: endpoint, records: filepath.Join(root, "writer-requests.json")}, nil
}

func (c *runtimeContent) configure(o *host.Options) {
	if c == nil {
		return
	}
	o.Storage = c.store
	o.StorageEndpoint = c.endpoint
	o.StoragePolicy = host.ContentPolicyFromRights(c.rights.decider(), contentReadAction)
	o.StorageWritePolicy = host.ContentPolicyFromRights(c.rights.decider(), contentWriteAction)
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
		decision := c.rights.decide(ctx, rwire.Subject{Account: subject.Account, Program: subject.Program}, contentReadAction, digest)
		switch decision.Outcome {
		case rwire.DecisionOutcomePermitted:
			return nil
		case rwire.DecisionOutcomeUnavailable:
			return service.ErrPolicyUnavailable
		default:
			return errors.New("content access denied")
		}
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
