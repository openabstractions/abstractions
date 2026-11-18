package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	inference "github.com/openabstractions/abstraction-inference/go"
)

// published080Records is the frozen 0.8.0 shape of every closed record a
// registry@1, registry@2 or applications@3 reply reaches
// (testdata/published_080_closed_records.json, read from the published
// definitions). A published 0.8.0 reader refuses a member a closed record
// does not declare (CONV-V1), so a reply this tree writes on a published
// method must use only these members.
type published080Records struct {
	Source  string `json:"source"`
	Records map[string]struct {
		Closed  bool              `json:"closed"`
		Members map[string]string `json:"members"`
	} `json:"records"`
}

func loadPublished080(t *testing.T) published080Records {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "published_080_closed_records.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out published080Records
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Records) == 0 {
		t.Fatalf("frozen 0.8.0 records: %v", err)
	}
	return out
}

// read reports every member the published reader of record would refuse in
// value: a member a closed record does not declare, at any depth.
func (p published080Records) read(record string, value any, at string, refused *[]string) {
	r, known := p.Records[record]
	if !known {
		return
	}
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			p.read(record, item, at+"[]", refused)
		}
	case map[string]any:
		for member, inner := range v {
			nested, declared := r.Members[member]
			if !declared {
				if r.Closed {
					*refused = append(*refused, at+"."+member)
				}
				continue
			}
			if nested != "" {
				p.read(nested, inner, at+"."+member, refused)
			}
		}
	}
}

// replyPayload is the method answer inside one generated service reply frame.
func replyPayload(t *testing.T, frame []byte) any {
	t.Helper()
	var reply map[string]json.RawMessage
	if err := json.Unmarshal(frame, &reply); err != nil {
		t.Fatalf("reply frame: %v: %s", err, frame)
	}
	var ok bool
	if json.Unmarshal(reply["ok"], &ok) != nil || !ok {
		t.Fatalf("reply refused: %s", frame)
	}
	var payload struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal(reply["payload"], &payload); err != nil || payload.Value == nil {
		t.Fatalf("reply payload: %v: %s", err, frame)
	}
	return payload.Value
}

// capturedFrames records each reply frame a generated dispatcher answers.
type capturedFrames struct {
	exchange func([]byte) ([]byte, error)
	replies  [][]byte
}

func (c *capturedFrames) ExchangeFrame(frame []byte) ([]byte, error) {
	reply, err := c.exchange(frame)
	c.replies = append(c.replies, reply)
	return reply, err
}

func (c *capturedFrames) last(t *testing.T) []byte {
	t.Helper()
	if len(c.replies) == 0 {
		t.Fatal("no reply frame")
	}
	return c.replies[len(c.replies)-1]
}

// publishedRegistryV1 and publishedRegistryV2 serve the runtime registry's
// registry@1 and registry@2 reads and Standings through the generated
// dispatchers, as the runtime's endpoint does for its bound caller.
type publishedRegistryV1 struct {
	wire.Registry
	r      *runtimeRegistry
	caller inference.Subject
}
type publishedRegistryV2 struct {
	wire.RegistryV2
	r      *runtimeRegistry
	caller inference.Subject
}

func (p publishedRegistryV1) Declarations() (wire.DeclarationList, error) {
	return p.r.Declarations(context.Background(), p.caller), nil
}
func (p publishedRegistryV1) Observe(cursor string, waitMs int64) (wire.DeclarationObservation, error) {
	return p.r.Observe(context.Background(), p.caller, cursor, waitMs), nil
}
func (p publishedRegistryV2) Declarations() (wire.DeclarationListV2, error) {
	return p.r.DeclarationsV2(context.Background(), p.caller), nil
}
func (p publishedRegistryV2) Observe(cursor string, waitMs int64) (wire.DeclarationObservationV2, error) {
	return p.r.ObserveV2(context.Background(), p.caller, cursor, waitMs), nil
}
func (p publishedRegistryV2) DeclarationsPinned() (wire.PinnedDeclarationList, error) {
	return p.r.DeclarationsPinned(context.Background(), p.caller), nil
}
func (p publishedRegistryV2) ObservePinned(cursor string, waitMs int64) (wire.PinnedDeclarationObservation, error) {
	return p.r.ObservePinned(context.Background(), p.caller, cursor, waitMs), nil
}
func (p publishedRegistryV2) Standings() (wire.ProviderStandingList, error) {
	return p.r.Standings(context.Background(), p.caller), nil
}

// 0.8.1 release blocker (architect ruling 2026-11-23, decisions/
// RULING-2026-11-23-081-wire-compat.md): a 0.8.0 reader of registry@1 and
// registry@2 decodes every published read the runtime answers. The
// runtime's product standing reaches a caller only through registry@2
// Standings, keyed by declaration name and program.
func TestPublished080ReaderDecodesEveryRegistryRead(t *testing.T) {
	published := loadPublished080(t)
	r, caller := registryObserverFixture(t)
	ctx := context.Background()
	d, trust := registryRemoteFixture(t, "pinned")
	if added := r.DeclarePinned(ctx, caller, r.DeclarationsPinned(ctx, caller).Revision, wire.PinnedDeclaration{Declaration: d, RemoteServerKeySHA256: trust.ServerKeySHA256}); added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal(added)
	}
	v1 := &capturedFrames{exchange: (&wire.RegistryDispatcher{Handler: publishedRegistryV1{r: r, caller: caller}}).ExchangeFrame}
	v2 := &capturedFrames{exchange: (&wire.RegistryV2Dispatcher{Handler: publishedRegistryV2{r: r, caller: caller}}).ExchangeFrame}
	c1, c2 := wire.NewRegistryClient(v1), wire.NewRegistryV2Client(v2)
	reads := []struct {
		name, record string
		frames       *capturedFrames
		call         func() (int, error)
	}{
		{"registry@1 Declarations", "DeclarationList", v1, func() (int, error) { l, err := c1.Declarations(); return len(l.Declarations), err }},
		{"registry@1 Observe", "DeclarationObservation", v1, func() (int, error) { l, err := c1.Observe("", 0); return len(l.Declarations), err }},
		{"registry@2 Declarations", "DeclarationListV2", v2, func() (int, error) { l, err := c2.Declarations(); return len(l.Declarations), err }},
		{"registry@2 Observe", "DeclarationObservationV2", v2, func() (int, error) { l, err := c2.Observe("", 0); return len(l.Declarations), err }},
		{"registry@2 DeclarationsPinned", "PinnedDeclarationList", v2, func() (int, error) { l, err := c2.DeclarationsPinned(); return len(l.Declarations), err }},
		{"registry@2 ObservePinned", "PinnedDeclarationObservation", v2, func() (int, error) { l, err := c2.ObservePinned("", 0); return len(l.Declarations), err }},
	}
	for _, read := range reads {
		if n, err := read.call(); err != nil || n != 1 {
			t.Fatalf("%s: %d declarations, %v", read.name, n, err)
		}
		var refused []string
		answer := replyPayload(t, read.frames.last(t))
		published.read(read.record, answer, read.record, &refused)
		if len(refused) != 0 {
			t.Errorf("%s: a 0.8.0 reader refuses %v", read.name, refused)
		}
		// The frozen reader refuses what the 0.8.0 decoder refuses: the
		// standing member the 0.8.1 candidate first sent on these reads.
		raw, _ := json.Marshal(answer)
		var grown map[string]any
		if err := json.Unmarshal(raw, &grown); err != nil {
			t.Fatal(err)
		}
		entry := grown["declarations"].([]any)[0].(map[string]any)
		if state, nested := entry["state"].(map[string]any); nested {
			entry = state
		}
		entry["standing"] = "product"
		refused = nil
		published.read(read.record, grown, read.record, &refused)
		if len(refused) != 1 {
			t.Errorf("%s: the frozen 0.8.0 reader accepts a grown DeclarationState: %v", read.name, refused)
		}
	}
	standings, err := c2.Standings()
	if err != nil || standings.Outcome != wire.DeclarationListOutcomePage || len(standings.Standings) != 1 {
		t.Fatalf("Standings: %+v %v", standings, err)
	}
	if s := standings.Standings[0]; s.Name != d.Name || s.Program != d.Program || s.Standing != wire.ProductStandingThirdParty {
		t.Fatalf("Standings carries %+v for %s", s, d.Name)
	}
}
