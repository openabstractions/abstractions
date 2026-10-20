package main

// Private applications@3 persistence. Callers supply transport-attested scope
// and codec-canonical body bytes, including unchanged opaque protected bytes.
// Admission is storage only. The caller must quietly recheck current authority,
// program/manifest/instance and owner generation immediately before using a
// dispatch token or protected reply. No handler or policy runs under the CAS lock.
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
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	rawcas "github.com/openabstractions/abstraction-cas/go"
	cas "github.com/openabstractions/abstraction-cas/go/api"
)

const (
	applicationV3ReceiptIndexLimit   = 8 << 20
	applicationV3ReceiptFileLimit    = 3 << 20
	applicationV3ReceiptGlobalBytes  = 192 << 20
	applicationV3ReceiptSubjectBytes = 48 << 20
)

var (
	errApplicationV3ReceiptInvalid  = errors.New("applications@3: invalid receipt input")
	errApplicationV3ReceiptUnknown  = errors.New("applications@3: receipt unknown")
	errApplicationV3ReceiptConflict = errors.New("applications@3: receipt body conflict")
	errApplicationV3ReceiptCapacity = errors.New("applications@3: receipt capacity")
	errApplicationV3ReceiptCorrupt  = errors.New("applications@3: receipt evidence unavailable")
)

type applicationV3ReceiptScope struct{ Account, Program, Method string }
type applicationV3ReceiptBinding struct{ Application, Manifest, Revision, RuntimeGeneration, Instance, InstanceEpoch, InnerKey string }
type applicationV3ReceiptRequest struct {
	Scope         applicationV3ReceiptScope
	Key           string
	Binding       applicationV3ReceiptBinding
	CanonicalBody []byte
}
type applicationV3ReceiptEntry struct {
	Scope                     applicationV3ReceiptScope
	Key, Body, Owner          string
	Binding                   applicationV3ReceiptBinding
	Accepted, Issued, Expires int64
	Phase                     string
	Inflight                  bool
	Bytes                     int64
	ResultHash                string
}
type applicationV3ReceiptIndex struct {
	Version int
	Floor   int64
	Entries map[string]applicationV3ReceiptEntry
}
type applicationV3ReceiptResult struct {
	Version int
	Entry   applicationV3ReceiptEntry
	Reply   []byte
}
type applicationV3ReceiptOutcome struct {
	State   string
	Reply   []byte
	Expires int64
}

// A token is private to the creating live store and cannot be reconstructed by
// reopening its index. Equality/recovery never returns another dispatch token.
type applicationV3ReceiptToken struct{ id, owner, body string }
type applicationV3Receipts struct {
	dir, owner              string
	now                     func() time.Time
	mu                      sync.Mutex
	live                    map[string]applicationV3ReceiptToken
	indexStore, resultStore cas.BoundedFileStore
	initialize              bool
	// collected is when expired receipts were last removed (collect).
	collected time.Time
	// Owned fixture seams simulate storage failure/crash; production leaves nil.
	afterResult func() error
}

func applicationV3ReceiptDigest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func applicationV3ReceiptID(s applicationV3ReceiptScope, key string) string {
	return applicationV3ReceiptDigest(struct {
		Scope applicationV3ReceiptScope
		Key   string
	}{s, key})
}
func applicationV3ReceiptSubject(s applicationV3ReceiptScope) string {
	return applicationV3ReceiptDigest(struct{ Account, Program string }{s.Account, s.Program})
}
func applicationV3ReceiptBody(r applicationV3ReceiptRequest) string {
	return applicationV3ReceiptDigest(struct {
		Profile string
		Binding applicationV3ReceiptBinding
		Body    []byte
	}{"applications@3", r.Binding, r.CanonicalBody})
}
func applicationV3ReceiptIssued(key string) (int64, error) {
	p := strings.Split(key, ".")
	if len(p) != 3 || p[0] != "a3" || len(key) > 128 {
		return 0, errApplicationV3ReceiptInvalid
	}
	n, e := strconv.ParseInt(p[1], 10, 64)
	if e != nil || n <= 0 || strconv.FormatInt(n, 10) != p[1] {
		return 0, errApplicationV3ReceiptInvalid
	}
	b, e := base64.RawURLEncoding.DecodeString(p[2])
	if e != nil || len(b) != 24 || base64.RawURLEncoding.EncodeToString(b) != p[2] {
		return 0, errApplicationV3ReceiptInvalid
	}
	return n, nil
}
func applicationV3ReceiptLife(method string) int64 {
	switch method {
	case "Call", "Activate":
		return int64(24 * time.Hour / time.Millisecond)
	case "Announce":
		return int64(10 * time.Minute / time.Millisecond)
	}
	return 0
}
func applicationV3ReceiptValidScope(s applicationV3ReceiptScope) bool {
	return s.Account != "" && s.Program != "" && utf8.ValidString(s.Account) && utf8.ValidString(s.Program) && applicationV3ReceiptLife(s.Method) > 0
}
func applicationV3ReceiptValidBinding(b applicationV3ReceiptBinding) bool {
	for _, v := range []string{b.Application, b.Manifest, b.Revision, b.RuntimeGeneration, b.Instance, b.InstanceEpoch, b.InnerKey} {
		if !utf8.ValidString(v) {
			return false
		}
	}
	return true
}
func applicationV3ReceiptHex(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func applicationV3ReceiptDecode(b []byte, v any) error {
	if json.Unmarshal(b, v) != nil {
		return errApplicationV3ReceiptCorrupt
	}
	canonical, e := json.Marshal(v)
	if e != nil || !bytes.Equal(canonical, b) {
		return errApplicationV3ReceiptCorrupt
	}
	return nil
}

func openApplicationV3Receipts(state string) (*applicationV3Receipts, error) {
	if !filepath.IsAbs(state) {
		return nil, errApplicationV3ReceiptInvalid
	}
	var token [24]byte
	if _, e := rand.Read(token[:]); e != nil {
		return nil, e
	}
	s := &applicationV3Receipts{dir: filepath.Join(state, "applications-v3", "receipts"), owner: hex.EncodeToString(token[:]), now: time.Now, live: map[string]applicationV3ReceiptToken{}, indexStore: cas.BoundedFileStore{MaxBytes: applicationV3ReceiptIndexLimit}, resultStore: cas.BoundedFileStore{MaxBytes: applicationV3ReceiptFileLimit}}
	if e := rawcas.LocalPath(s.dir); e != nil {
		return nil, e
	}
	if e := os.MkdirAll(filepath.Dir(s.dir), 0700); e != nil {
		return nil, e
	}
	if e := os.Mkdir(s.dir, 0700); e == nil {
		s.initialize = true
	} else if !errors.Is(e, os.ErrExist) {
		return nil, e
	}
	if e := os.MkdirAll(filepath.Join(s.dir, "results"), 0700); e != nil {
		return nil, e
	}
	e := s.change(context.Background(), func(i *applicationV3ReceiptIndex) error { return nil })
	if e != nil {
		return nil, e
	}
	s.initialize = false
	return s, nil
}

// applicationV3ReceiptCollectEvery is how often admission removes expired
// receipts. Every edit validates each kept receipt, so the kept count sets
// the cost of every Announce, Call and Activate.
const applicationV3ReceiptCollectEvery = time.Minute

// collect removes expired receipts at most once a collection interval. A
// failure keeps them until the next interval; the edits that follow still
// validate the index.
func (s *applicationV3Receipts) collect(ctx context.Context) {
	s.mu.Lock()
	now := s.now()
	due := s.collected.IsZero() || now.Sub(s.collected) >= applicationV3ReceiptCollectEvery
	if due {
		s.collected = now
	}
	s.mu.Unlock()
	if due {
		//unchecked: expired receipts stay until the next interval; admission still validates the index
		_ = s.gc(ctx)
	}
}
func (s *applicationV3Receipts) resultPath(id string) string {
	return filepath.Join(s.dir, "results", id+".json")
}
func (s *applicationV3Receipts) result(e applicationV3ReceiptEntry, id string) ([]byte, []byte, error) {
	v, err := s.resultStore.Read(s.resultPath(id))
	if err != nil {
		return nil, nil, errApplicationV3ReceiptCorrupt
	}
	if v.Data == nil {
		return nil, nil, nil
	}
	var r applicationV3ReceiptResult
	if applicationV3ReceiptDecode(v.Data, &r) != nil || r.Version != 1 || r.Entry != e || len(r.Reply) > 1<<20 {
		return nil, nil, errApplicationV3ReceiptCorrupt
	}
	return v.Data, r.Reply, nil
}

// Every operation takes index then deterministic result locks. Storage-only
// result I/O is inside the existing native CAS edit lock, including reads and GC.
func (s *applicationV3Receipts) change(ctx context.Context, edit func(*applicationV3ReceiptIndex) error) error {
	return rawcas.ChangeLimitContext(ctx, filepath.Join(s.dir, "index.json"), s.indexStore.MaxBytes, func(b []byte) ([]byte, error) {
		i := applicationV3ReceiptIndex{Version: 1, Entries: map[string]applicationV3ReceiptEntry{}}
		if b != nil {
			if applicationV3ReceiptDecode(b, &i) != nil {
				return nil, errApplicationV3ReceiptCorrupt
			}
		}
		if e := s.validate(&i, b == nil); e != nil {
			return nil, e
		}
		if e := edit(&i); e != nil {
			return nil, e
		}
		if e := s.bounds(&i); e != nil {
			return nil, e
		}
		out, e := json.Marshal(i)
		if e != nil {
			return nil, e
		}
		reserve := 0
		for _, entry := range i.Entries {
			if entry.Inflight {
				reserve += 70
			}
		}
		if len(out)+reserve > applicationV3ReceiptIndexLimit {
			return nil, errApplicationV3ReceiptCapacity
		}
		return out, nil
	})
}
func (s *applicationV3Receipts) validate(i *applicationV3ReceiptIndex, fresh bool) error {
	if fresh && !s.initialize {
		return errApplicationV3ReceiptCorrupt
	}
	if i.Version != 1 || i.Floor < 0 || i.Entries == nil {
		return errApplicationV3ReceiptCorrupt
	}
	// Reject impossible aggregate reservations before reading result payloads.
	if s.bounds(i) != nil {
		return errApplicationV3ReceiptCorrupt
	}
	entries, e := os.ReadDir(s.dir)
	if e != nil {
		return e
	}
	for _, d := range entries {
		if d.Name() != "index.json" && d.Name() != "index.json.lock" && d.Name() != "results" {
			return errApplicationV3ReceiptCorrupt
		}
		info, e := d.Info()
		if e != nil || info.Mode()&os.ModeSymlink != 0 {
			return errApplicationV3ReceiptCorrupt
		}
	}
	files, e := os.ReadDir(filepath.Join(s.dir, "results"))
	if e != nil {
		return e
	}
	for _, d := range files {
		n := d.Name()
		id := strings.TrimSuffix(strings.TrimSuffix(n, ".lock"), ".json")
		if !applicationV3ReceiptHex(id) || (n != id+".json" && n != id+".json.lock") {
			return errApplicationV3ReceiptCorrupt
		}
		if _, ok := i.Entries[id]; !ok || fresh {
			return errApplicationV3ReceiptCorrupt
		}
		info, e := d.Info()
		if e != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errApplicationV3ReceiptCorrupt
		}
		if n == id+".json" && info.Size() > applicationV3ReceiptFileLimit {
			return errApplicationV3ReceiptCorrupt
		}
		entry := i.Entries[id]
		if n == id+".json" && (entry.Phase == "unknown" || (entry.Phase == "fenced" && info.Size() != entry.Bytes)) {
			return errApplicationV3ReceiptCorrupt
		}
	}
	for id, e := range i.Entries {
		issued, err := applicationV3ReceiptIssued(e.Key)
		life := applicationV3ReceiptLife(e.Scope.Method)
		if !applicationV3ReceiptValidScope(e.Scope) || !applicationV3ReceiptValidBinding(e.Binding) || id != applicationV3ReceiptID(e.Scope, e.Key) || err != nil || issued != e.Issued || e.Accepted <= 0 || e.Accepted-i.Floor > 300000 || e.Accepted < e.Issued || life > math.MaxInt64-e.Accepted || e.Expires != e.Accepted+life || !applicationV3ReceiptHex(e.Body) || e.Owner == "" {
			return errApplicationV3ReceiptCorrupt
		}
		if e.Phase != "pending" && e.Phase != "complete" && e.Phase != "unknown" && e.Phase != "fenced" {
			return errApplicationV3ReceiptCorrupt
		}
		if (e.Phase == "pending" && !e.Inflight) || (e.Phase != "pending" && e.Inflight) || (e.Phase == "unknown" && e.Bytes != 0) {
			return errApplicationV3ReceiptCorrupt
		}
		if e.Phase == "fenced" && e.Expires > i.Floor {
			return errApplicationV3ReceiptCorrupt
		}
		if e.Inflight {
			if e.Bytes != applicationV3ReceiptFileLimit || e.Phase == "complete" {
				return errApplicationV3ReceiptCorrupt
			}
		} else if e.Bytes < 0 || e.Bytes > applicationV3ReceiptFileLimit {
			return errApplicationV3ReceiptCorrupt
		}
		if e.Phase == "complete" {
			// Result envelope records the pending association, not mutable publication fields.
			original := e
			original.Phase = "pending"
			original.Inflight = true
			original.Bytes = applicationV3ReceiptFileLimit
			original.ResultHash = ""
			raw, _, err := s.result(original, id)
			if err != nil || raw == nil || int64(len(raw)) != e.Bytes {
				return errApplicationV3ReceiptCorrupt
			}
			sum := sha256.Sum256(raw)
			if hex.EncodeToString(sum[:]) != e.ResultHash {
				return errApplicationV3ReceiptCorrupt
			}
		} else if e.ResultHash != "" {
			return errApplicationV3ReceiptCorrupt
		} else if e.Phase == "pending" {
			if _, _, err := s.result(e, id); err != nil {
				return err
			}
		}
	}
	if s.bounds(i) != nil {
		return errApplicationV3ReceiptCorrupt
	}
	return nil
}
func (s *applicationV3Receipts) bounds(i *applicationV3ReceiptIndex) error {
	if len(i.Entries) > 16384 {
		return errApplicationV3ReceiptCapacity
	}
	type count struct {
		n, flight int
		bytes     int64
	}
	subjects := map[string]count{}
	global := count{}
	for _, e := range i.Entries {
		if e.Bytes < 0 || e.Bytes > applicationV3ReceiptFileLimit {
			return errApplicationV3ReceiptCapacity
		}
		subject := applicationV3ReceiptSubject(e.Scope)
		c := subjects[subject]
		c.n++
		global.n++
		c.bytes += e.Bytes
		global.bytes += e.Bytes
		if e.Inflight {
			c.flight++
			global.flight++
		}
		subjects[subject] = c
		if c.n > 4096 || c.flight > 16 || c.bytes > applicationV3ReceiptSubjectBytes {
			return errApplicationV3ReceiptCapacity
		}
	}
	if len(subjects) > 256 || global.flight > 64 || global.bytes > applicationV3ReceiptGlobalBytes {
		return errApplicationV3ReceiptCapacity
	}
	return nil
}
func (s *applicationV3Receipts) floor(i *applicationV3ReceiptIndex) error {
	n := s.now().UTC().UnixMilli()
	if n <= 0 {
		return errApplicationV3ReceiptUnknown
	}
	if n > i.Floor {
		i.Floor = n
	}
	return nil
}

func (s *applicationV3Receipts) admit(ctx context.Context, r applicationV3ReceiptRequest) (applicationV3ReceiptOutcome, *applicationV3ReceiptToken, error) {
	issued, e := applicationV3ReceiptIssued(r.Key)
	if e != nil || !applicationV3ReceiptValidScope(r.Scope) || !applicationV3ReceiptValidBinding(r.Binding) || len(r.CanonicalBody) > 1<<20 {
		return applicationV3ReceiptOutcome{}, nil, errApplicationV3ReceiptInvalid
	}
	s.collect(ctx)
	id, body := applicationV3ReceiptID(r.Scope, r.Key), applicationV3ReceiptBody(r)
	out := applicationV3ReceiptOutcome{}
	var token *applicationV3ReceiptToken
	s.mu.Lock()
	defer s.mu.Unlock()
	e = s.change(ctx, func(i *applicationV3ReceiptIndex) error {
		if e := s.floor(i); e != nil {
			return e
		}
		if old, ok := i.Entries[id]; ok {
			if old.Body != body {
				return errApplicationV3ReceiptConflict
			}
			out.Expires = old.Expires
			if i.Floor >= old.Expires {
				out.State = "unknown"
				return nil
			}
			out.State = old.Phase
			if old.Phase == "complete" {
				pending := old
				pending.Phase = "pending"
				pending.Inflight = true
				pending.Bytes = applicationV3ReceiptFileLimit
				pending.ResultHash = ""
				_, reply, e := s.result(pending, id)
				if e != nil {
					return e
				}
				out.Reply = reply
			} else if old.Phase == "pending" {
				raw, reply, e := s.result(old, id)
				if e != nil {
					return e
				}
				if raw != nil {
					sum := sha256.Sum256(raw)
					old.Phase = "complete"
					old.Inflight = false
					old.Bytes = int64(len(raw))
					old.ResultHash = hex.EncodeToString(sum[:])
					i.Entries[id] = old
					out.State = "complete"
					out.Reply = reply
				} else if _, live := s.live[id]; !live {
					out.State = "unknown"
				}
			}
			return nil
		}
		if issued < i.Floor-300000 || issued > i.Floor && issued-i.Floor > 300000 {
			out.State = "unknown"
			return nil
		}
		accepted := i.Floor
		if issued > accepted {
			accepted = issued
		}
		life := applicationV3ReceiptLife(r.Scope.Method)
		if accepted > math.MaxInt64-life {
			return errApplicationV3ReceiptUnknown
		}
		entry := applicationV3ReceiptEntry{Scope: r.Scope, Key: r.Key, Body: body, Owner: s.owner, Binding: r.Binding, Accepted: accepted, Issued: issued, Expires: accepted + life, Phase: "pending", Inflight: true, Bytes: applicationV3ReceiptFileLimit}
		metadata, err := json.Marshal(applicationV3ReceiptResult{Version: 1, Entry: entry})
		if err != nil {
			return err
		}
		// nil encodes as null (4B); a maximum native reply is quoted base64.
		if len(metadata)+base64.StdEncoding.EncodedLen(1<<20)-2 > applicationV3ReceiptFileLimit {
			return errApplicationV3ReceiptCapacity
		}
		i.Entries[id] = entry
		out = applicationV3ReceiptOutcome{State: "pending", Expires: entry.Expires}
		t := applicationV3ReceiptToken{id: id, owner: s.owner, body: body}
		token = &t
		return nil
	})
	if e != nil {
		return applicationV3ReceiptOutcome{}, nil, e
	}
	if token != nil {
		s.live[id] = *token
	}
	return out, token, nil
}

// finish receives the exact encoded native reply after the actual handler has
// returned. It publishes no domain effect and cannot mint a new dispatch token.
func (s *applicationV3Receipts) finish(ctx context.Context, t applicationV3ReceiptToken, reply []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	live, ok := s.live[t.id]
	if !ok || live != t || t.owner != s.owner {
		return errApplicationV3ReceiptUnknown
	}
	defer delete(s.live, t.id)
	unknown := false
	err := s.change(ctx, func(i *applicationV3ReceiptIndex) error {
		e, ok := i.Entries[t.id]
		if !ok || e.Owner != s.owner || e.Body != t.body || e.Phase != "pending" {
			return errApplicationV3ReceiptUnknown
		}
		if err := s.floor(i); err != nil {
			return err
		}
		if i.Floor >= e.Expires {
			unknown = true
			e.Phase = "unknown"
			e.Inflight = false
			e.Bytes = 0
			i.Entries[t.id] = e
			return nil
		}
		if len(reply) > 1<<20 {
			unknown = true
			e.Phase = "unknown"
			e.Inflight = false
			e.Bytes = 0
			i.Entries[t.id] = e
			return nil
		}
		raw, err := json.Marshal(applicationV3ReceiptResult{Version: 1, Entry: e, Reply: reply})
		if err != nil {
			return err
		}
		if len(reply) > 1<<20 || len(raw) > applicationV3ReceiptFileLimit {
			unknown = true
			e.Phase = "unknown"
			e.Inflight = false
			e.Bytes = 0
			i.Entries[t.id] = e
			return nil
		}
		base, err := s.resultStore.Read(s.resultPath(t.id))
		if err != nil {
			return err
		}
		if base.Data != nil && !bytes.Equal(base.Data, raw) {
			return errApplicationV3ReceiptCorrupt
		}
		if err = s.resultStore.WriteContext(ctx, s.resultPath(t.id), base, raw); err != nil {
			return err
		}
		if s.afterResult != nil {
			if err = s.afterResult(); err != nil {
				return err
			}
		}
		sum := sha256.Sum256(raw)
		e.Phase = "complete"
		e.Inflight = false
		e.Bytes = int64(len(raw))
		e.ResultHash = hex.EncodeToString(sum[:])
		i.Entries[t.id] = e
		return nil
	})
	if err != nil {
		return err
	}
	if unknown {
		return errApplicationV3ReceiptUnknown
	}
	return nil
}

// gc fences expired history in one durable edit, then removes only deterministic
// result files in another edit. Unknown in-flight owners retain their quota;
// another generation is never interpreted as native process closure.
func (s *applicationV3Receipts) gc(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.change(ctx, func(i *applicationV3ReceiptIndex) error {
		if e := s.floor(i); e != nil {
			return e
		}
		for id, e := range i.Entries {
			if i.Floor >= e.Expires && !e.Inflight {
				e.Phase = "fenced"
				e.ResultHash = ""
				i.Entries[id] = e
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return s.change(ctx, func(i *applicationV3ReceiptIndex) error {
		for id, e := range i.Entries {
			if e.Phase != "fenced" {
				continue
			}
			for _, p := range []string{s.resultPath(id), s.resultPath(id) + ".lock"} {
				if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			delete(i.Entries, id)
		}
		return nil
	})
}

// ownerClosed is called only after the native lifecycle has independently proved
// that this owner has no live handler. It never infers closure from a new token.
func (s *applicationV3Receipts) ownerClosed(ctx context.Context, owner string) error {
	if owner == "" {
		return errApplicationV3ReceiptInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner == s.owner && len(s.live) > 0 {
		return fmt.Errorf("applications@3: owner still has live handlers")
	}
	return s.change(ctx, func(i *applicationV3ReceiptIndex) error {
		for id, e := range i.Entries {
			if e.Owner == owner && e.Inflight {
				raw, _, err := s.result(e, id)
				if err != nil {
					return err
				}
				e.Inflight = false
				if raw != nil {
					sum := sha256.Sum256(raw)
					e.Phase = "complete"
					e.Bytes = int64(len(raw))
					e.ResultHash = hex.EncodeToString(sum[:])
				} else {
					e.Phase = "unknown"
					e.Bytes = 0
				}
				i.Entries[id] = e
			}
		}
		return nil
	})
}
