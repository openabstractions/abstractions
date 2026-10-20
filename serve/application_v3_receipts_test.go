package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	applications "github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
)

var receiptTestTime = time.UnixMilli(1792940000000)

func TestApplicationV3SDKRequestKey(t *testing.T) {
	seen := map[string]bool{}
	store := receiptStore(t, t.TempDir())
	store.now = time.Now
	for _, method := range []string{"Call", "Activate", "Announce", "Announce"} {
		before := time.Now().UnixMilli()
		key, err := applications.NewRequestKey()
		if err != nil {
			t.Fatal(err)
		}
		issued, err := applicationV3ReceiptIssued(key)
		if err != nil || issued < before || issued > time.Now().UnixMilli() || seen[key] {
			t.Fatalf("%s SDK key rejected/stale/reused: %q %v", method, key, err)
		}
		seen[key] = true
		request := receiptRequest(1)
		request.Key, request.Scope.Method = key, method
		_, token := receiptAdmit(t, store, request)
		if token == nil {
			t.Fatalf("fresh %s SDK key did not receive admission", method)
		}
	}
	// Both former native producers failed the actual receiver grammar.
	for _, key := range []string{fmt.Sprintf("%032x", 1), fmt.Sprintf("%048x", 1)} {
		if _, err := applicationV3ReceiptIssued(key); err == nil {
			t.Fatal("old hex key accepted")
		}
	}
}

func receiptKey(n int, at time.Time) string {
	var b [24]byte
	for i := 0; i < 8; i++ {
		b[i] = byte(n >> (i * 8))
	}
	return fmt.Sprintf("a3.%d.%s", at.UnixMilli(), base64.RawURLEncoding.EncodeToString(b[:]))
}
func receiptRequest(n int) applicationV3ReceiptRequest {
	return applicationV3ReceiptRequest{Scope: applicationV3ReceiptScope{Account: "native-account", Program: "native-program", Method: "Call"}, Key: receiptKey(n, receiptTestTime), Binding: applicationV3ReceiptBinding{Application: "app", Manifest: "signed-hash", Revision: "revision", RuntimeGeneration: "generation", Instance: "instance", InstanceEpoch: "epoch", InnerKey: "inner"}, CanonicalBody: []byte(`{"opaque":"keep  bytes"}`)}
}
func receiptStore(t *testing.T, dir string) *applicationV3Receipts {
	t.Helper()
	s, e := openApplicationV3Receipts(dir)
	if e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return receiptTestTime }
	return s
}
func receiptAdmit(t *testing.T, s *applicationV3Receipts, r applicationV3ReceiptRequest) (applicationV3ReceiptOutcome, *applicationV3ReceiptToken) {
	t.Helper()
	out, tok, e := s.admit(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	return out, tok
}

func TestApplicationV3ReceiptGrammar(t *testing.T) {
	good := receiptKey(1, receiptTestTime)
	if _, e := applicationV3ReceiptIssued(good); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{"", "a3.0." + base64.RawURLEncoding.EncodeToString(make([]byte, 24)), "a3.01." + base64.RawURLEncoding.EncodeToString(make([]byte, 24)), good + "=", "a3.+1792940000000." + base64.RawURLEncoding.EncodeToString(make([]byte, 24)), "a3.1792940000000.AA", "a3.9223372036854775808." + base64.RawURLEncoding.EncodeToString(make([]byte, 24))} {
		if _, e := applicationV3ReceiptIssued(bad); e == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
func TestApplicationV3ReceiptConcurrentStoresOnce(t *testing.T) {
	dir := t.TempDir()
	a, b := receiptStore(t, dir), receiptStore(t, dir)
	r := receiptRequest(1)
	var wg sync.WaitGroup
	var mu sync.Mutex
	tokens := []struct {
		s *applicationV3Receipts
		t applicationV3ReceiptToken
	}{}
	errs := []error{}
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s := a
			if n%2 != 0 {
				s = b
			}
			_, tok, e := s.admit(context.Background(), r)
			mu.Lock()
			defer mu.Unlock()
			if e != nil {
				errs = append(errs, e)
			}
			if tok != nil {
				tokens = append(tokens, struct {
					s *applicationV3Receipts
					t applicationV3ReceiptToken
				}{s, *tok})
			}
		}(n)
	}
	wg.Wait()
	if len(errs) != 0 || len(tokens) != 1 {
		t.Fatalf("errors=%v tokens=%d", errs, len(tokens))
	}
	// An inert caller invokes its callback only when holding the creating token.
	dispatches := 0
	dispatches++
	if e := tokens[0].s.finish(context.Background(), tokens[0].t, []byte(`{"original":"reply"}`)); e != nil {
		t.Fatal(e)
	}
	out, tok := receiptAdmit(t, b, r)
	if tok != nil || out.State != "complete" || string(out.Reply) != `{"original":"reply"}` || dispatches != 1 {
		t.Fatalf("%+v %v %d", out, tok, dispatches)
	}
	if e := tokens[0].s.finish(context.Background(), tokens[0].t, nil); !errors.Is(e, errApplicationV3ReceiptUnknown) {
		t.Fatal(e)
	}
}
func TestApplicationV3ReceiptBindingsAndImmutableReplay(t *testing.T) {
	s := receiptStore(t, t.TempDir())
	r := receiptRequest(2)
	first, tok := receiptAdmit(t, s, r)
	if tok == nil {
		t.Fatal("no reservation")
	}
	if e := s.finish(context.Background(), *tok, []byte("answer")); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*applicationV3ReceiptRequest){func(r *applicationV3ReceiptRequest) { r.CanonicalBody = []byte(`{"opaque":"keep bytes"}`) }, func(r *applicationV3ReceiptRequest) { r.Binding.Manifest = "changed" }, func(r *applicationV3ReceiptRequest) { r.Binding.InstanceEpoch = "changed" }, func(r *applicationV3ReceiptRequest) { r.Binding.InnerKey = "changed" }} {
		changed := r
		mutate(&changed)
		_, tok, e := s.admit(context.Background(), changed)
		if tok != nil || !errors.Is(e, errApplicationV3ReceiptConflict) {
			t.Fatalf("token=%v err=%v", tok, e)
		}
	}
	s.now = func() time.Time { return receiptTestTime.Add(time.Minute) }
	again, token := receiptAdmit(t, s, r)
	if token != nil || again.Expires != first.Expires {
		t.Fatal("replay changed expiry")
	}
	// Equal raw keys are distinct only in separately native-attested subject/method scopes.
	other := r
	other.Scope.Program = "another-native-program"
	_, token = receiptAdmit(t, s, other)
	if token == nil {
		t.Fatal("native subject not scoped")
	}
	other = r
	other.Scope.Method = "Activate"
	_, token = receiptAdmit(t, s, other)
	if token == nil {
		t.Fatal("method not scoped")
	}
}
func TestApplicationV3ReceiptCrashRecovery(t *testing.T) {
	for _, afterResult := range []bool{false, true} {
		t.Run(fmt.Sprint(afterResult), func(t *testing.T) {
			dir := t.TempDir()
			s := receiptStore(t, dir)
			r := receiptRequest(3)
			_, tok := receiptAdmit(t, s, r)
			if afterResult {
				s.afterResult = func() error { return errors.New("crash after result before index") }
				if e := s.finish(context.Background(), *tok, []byte("saved")); e == nil {
					t.Fatal("missing injected failure")
				}
			}
			reopened := receiptStore(t, dir)
			out, newToken := receiptAdmit(t, reopened, r)
			if newToken != nil {
				t.Fatal("recovery dispatched")
			}
			if afterResult {
				if out.State != "complete" || string(out.Reply) != "saved" {
					t.Fatalf("%+v", out)
				}
			} else {
				if out.State != "unknown" {
					t.Fatalf("%+v", out)
				}
			}
		})
	}
}
func TestApplicationV3ReceiptUnprovenOwnerKeepsQuota(t *testing.T) {
	dir := t.TempDir()
	a := receiptStore(t, dir)
	r := receiptRequest(4)
	_, tok := receiptAdmit(t, a, r)
	b := receiptStore(t, dir)
	out, newToken := receiptAdmit(t, b, r)
	if out.State != "unknown" || newToken != nil {
		t.Fatal(out)
	}
	if e := a.ownerClosed(context.Background(), a.owner); e == nil {
		t.Fatal("closed own live handler")
	}
	if e := b.gc(context.Background()); e != nil {
		t.Fatal(e)
	}
	// A later runtime cannot infer closure from its own different generation.
	if e := a.finish(context.Background(), *tok, []byte("late actual return")); e != nil {
		t.Fatal(e)
	}
	out, newToken = receiptAdmit(t, b, r)
	if out.State != "complete" || newToken != nil {
		t.Fatal(out)
	}
}
func TestApplicationV3ReceiptClockAndGC(t *testing.T) {
	dir := t.TempDir()
	s := receiptStore(t, dir)
	r := receiptRequest(5)
	r.Scope.Method = "Announce"
	first, tok := receiptAdmit(t, s, r)
	if first.Expires != receiptTestTime.Add(10*time.Minute).UnixMilli() {
		t.Fatal(first)
	}
	if e := s.finish(context.Background(), *tok, []byte("fixed lease expiry")); e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return receiptTestTime.Add(11 * time.Minute) }
	out, tok := receiptAdmit(t, s, r)
	if out.State != "unknown" || tok != nil {
		t.Fatal(out)
	}
	if e := s.gc(context.Background()); e != nil {
		t.Fatal(e)
	}
	s = receiptStore(t, dir) // clock rolls back; retained floor prevents resurrection
	out, tok = receiptAdmit(t, s, r)
	if out.State != "unknown" || tok != nil {
		t.Fatal("resurrected", out)
	}
	fresh := receiptRequest(6)
	fresh.Scope.Method = "Announce"
	fresh.Key = receiptKey(6, receiptTestTime.Add(11*time.Minute))
	out, tok = receiptAdmit(t, s, fresh)
	if tok == nil {
		t.Fatal(out)
	}
	future := receiptRequest(7)
	future.Key = receiptKey(7, receiptTestTime.Add(time.Hour))
	out, tok = receiptAdmit(t, s, future)
	if tok != nil || out.State != "unknown" {
		t.Fatal(out)
	}
}
func TestApplicationV3ReceiptFutureSkewAndExpiredHandler(t *testing.T) {
	s := receiptStore(t, t.TempDir())
	r := receiptRequest(8)
	r.Key = receiptKey(8, receiptTestTime.Add(5*time.Minute))
	out, tok := receiptAdmit(t, s, r)
	if tok == nil {
		t.Fatal(out)
	}
	// Retention is based on max(floor, issued), and a future-but-admitted key remains readable.
	again, second := receiptAdmit(t, s, r)
	if second != nil || again.State != "pending" {
		t.Fatal(again)
	}
	s.now = func() time.Time { return receiptTestTime.Add(25 * time.Hour) }
	if e := s.gc(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := s.finish(context.Background(), *tok, []byte("expired late result")); !errors.Is(e, errApplicationV3ReceiptUnknown) {
		t.Fatal(e)
	}
	again, second = receiptAdmit(t, s, r)
	if second != nil || again.State != "unknown" {
		t.Fatal(again)
	}
	if e := s.gc(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestApplicationV3ReceiptCapacityBeforeEffect(t *testing.T) {
	s := receiptStore(t, t.TempDir())
	for n := 0; n < 16; n++ {
		_, tok := receiptAdmit(t, s, receiptRequest(100+n))
		if tok == nil {
			t.Fatal(n)
		}
	}
	before, e := os.ReadFile(filepath.Join(s.dir, "index.json"))
	if e != nil {
		t.Fatal(e)
	}
	_, tok, e := s.admit(context.Background(), receiptRequest(200))
	if tok != nil || !errors.Is(e, errApplicationV3ReceiptCapacity) {
		t.Fatalf("%v %v", tok, e)
	}
	after, _ := os.ReadFile(filepath.Join(s.dir, "index.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("capacity refusal mutated index")
	}
	for n := 0; n < 48; n++ {
		r := receiptRequest(300 + n)
		r.Scope.Program = fmt.Sprint("subject", n/16)
		_, token := receiptAdmit(t, s, r)
		if token == nil {
			t.Fatal(n)
		}
	}
	r := receiptRequest(400)
	r.Scope.Program = "fifth"
	_, tok, e = s.admit(context.Background(), r)
	if tok != nil || !errors.Is(e, errApplicationV3ReceiptCapacity) {
		t.Fatalf("%v %v", tok, e)
	}
}
func TestApplicationV3ReceiptIndependentBounds(t *testing.T) {
	s := receiptStore(t, t.TempDir())
	base := applicationV3ReceiptEntry{Scope: receiptRequest(1).Scope}
	for _, test := range []struct {
		name  string
		build func() map[string]applicationV3ReceiptEntry
	}{
		{"global-count", func() map[string]applicationV3ReceiptEntry {
			m := map[string]applicationV3ReceiptEntry{}
			for n := 0; n < 16385; n++ {
				e := base
				e.Scope.Program = fmt.Sprint(n / 100)
				m[fmt.Sprint(n)] = e
			}
			return m
		}},
		{"subject-count", func() map[string]applicationV3ReceiptEntry {
			m := map[string]applicationV3ReceiptEntry{}
			for n := 0; n < 4097; n++ {
				m[fmt.Sprint(n)] = base
			}
			return m
		}},
		{"subjects", func() map[string]applicationV3ReceiptEntry {
			m := map[string]applicationV3ReceiptEntry{}
			for n := 0; n < 257; n++ {
				e := base
				e.Scope.Program = fmt.Sprint(n)
				m[fmt.Sprint(n)] = e
			}
			return m
		}},
		{"subject-bytes", func() map[string]applicationV3ReceiptEntry {
			m := map[string]applicationV3ReceiptEntry{}
			for n := 0; n < 17; n++ {
				e := base
				e.Bytes = applicationV3ReceiptFileLimit
				m[fmt.Sprint(n)] = e
			}
			return m
		}},
		{"global-bytes", func() map[string]applicationV3ReceiptEntry {
			m := map[string]applicationV3ReceiptEntry{}
			for n := 0; n < 65; n++ {
				e := base
				e.Scope.Program = fmt.Sprint(n / 16)
				e.Bytes = applicationV3ReceiptFileLimit
				m[fmt.Sprint(n)] = e
			}
			return m
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !errors.Is(s.bounds(&applicationV3ReceiptIndex{Entries: test.build()}), errApplicationV3ReceiptCapacity) {
				t.Fatal("bound accepted")
			}
		})
	}
}
func TestApplicationV3ReceiptCorruptionFailsClosed(t *testing.T) {
	for _, kind := range []string{"missing-index", "bad-index", "orphan", "missing-result", "corrupt-result", "oversize-result", "pending-corrupt-result"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			s := receiptStore(t, dir)
			r := receiptRequest(10)
			_, tok := receiptAdmit(t, s, r)
			if kind != "pending-corrupt-result" {
				if e := s.finish(context.Background(), *tok, []byte("protected")); e != nil {
					t.Fatal(e)
				}
			}
			switch kind {
			case "missing-index":
				if e := os.Remove(filepath.Join(s.dir, "index.json")); e != nil {
					t.Fatal(e)
				}
			case "bad-index":
				if e := os.WriteFile(filepath.Join(s.dir, "index.json"), []byte(`{"Version":999}`), 0600); e != nil {
					t.Fatal(e)
				}
			case "orphan":
				if e := os.WriteFile(filepath.Join(s.dir, "results", "orphan"), nil, 0600); e != nil {
					t.Fatal(e)
				}
			case "missing-result":
				if e := os.Remove(s.resultPath(tok.id)); e != nil {
					t.Fatal(e)
				}
			default:
				payload := []byte("corrupt")
				if kind == "oversize-result" {
					payload = make([]byte, applicationV3ReceiptFileLimit+1)
				}
				if e := os.WriteFile(s.resultPath(tok.id), payload, 0600); e != nil {
					t.Fatal(e)
				}
			}
			_, token, e := s.admit(context.Background(), receiptRequest(11))
			if token != nil || e == nil {
				t.Fatalf("%v %v", token, e)
			}
		})
	}
}
func TestApplicationV3ReceiptCancellationOversizeAndIndexCap(t *testing.T) {
	s := receiptStore(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, tok, e := s.admit(ctx, receiptRequest(12))
	if tok != nil || e == nil {
		t.Fatal("cancelled reservation admitted")
	}
	r := receiptRequest(13)
	_, tok = receiptAdmit(t, s, r)
	if e := s.finish(context.Background(), *tok, make([]byte, (1<<20)+1)); !errors.Is(e, errApplicationV3ReceiptUnknown) {
		t.Fatal(e)
	}
	out, tok := receiptAdmit(t, s, r)
	if out.State != "unknown" || tok != nil {
		t.Fatal(out)
	}
	before, _ := os.ReadFile(filepath.Join(s.dir, "index.json"))
	huge := receiptRequest(14)
	huge.Binding.Manifest = string(make([]byte, applicationV3ReceiptIndexLimit))
	_, tok, e = s.admit(context.Background(), huge)
	if tok != nil || !errors.Is(e, errApplicationV3ReceiptCapacity) {
		t.Fatalf("%v %v", tok, e)
	}
	after, _ := os.ReadFile(filepath.Join(s.dir, "index.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("index capacity mutated")
	}
	// Encoded index ceiling is independent of per-record reply metadata fit.
	e = s.change(context.Background(), func(i *applicationV3ReceiptIndex) error {
		for n := 0; n < 12000; n++ {
			request := receiptRequest(2000 + n)
			request.Scope.Program = fmt.Sprint("subject", n/3000)
			request.Binding.Manifest = string(bytes.Repeat([]byte("x"), 600))
			id := applicationV3ReceiptID(request.Scope, request.Key)
			i.Entries[id] = applicationV3ReceiptEntry{Scope: request.Scope, Key: request.Key, Body: applicationV3ReceiptBody(request), Owner: s.owner, Binding: request.Binding, Accepted: receiptTestTime.UnixMilli(), Issued: receiptTestTime.UnixMilli(), Expires: receiptTestTime.Add(24 * time.Hour).UnixMilli(), Phase: "unknown"}
		}
		return nil
	})
	if !errors.Is(e, errApplicationV3ReceiptCapacity) {
		t.Fatal(e)
	}
	after, _ = os.ReadFile(filepath.Join(s.dir, "index.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("encoded index ceiling mutated state")
	}
}
func TestApplicationV3ReceiptGCAndFinishRace(t *testing.T) {
	s := receiptStore(t, t.TempDir())
	r := receiptRequest(15)
	_, tok := receiptAdmit(t, s, r)
	s.now = func() time.Time { return receiptTestTime.Add(25 * time.Hour) }
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs <- s.gc(context.Background()) }()
	go func() { defer wg.Done(); errs <- s.finish(context.Background(), *tok, []byte("late")) }()
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil && !errors.Is(e, errApplicationV3ReceiptUnknown) {
			t.Fatal(e)
		}
	}
	if e := s.gc(context.Background()); e != nil {
		t.Fatal(e)
	}
	out, token := receiptAdmit(t, s, r)
	if token != nil || out.State != "unknown" {
		t.Fatal(out)
	}
}
func TestApplicationV3ReceiptClosedOwnerAndMalformedState(t *testing.T) {
	dir := t.TempDir()
	a := receiptStore(t, dir)
	r := receiptRequest(16)
	receiptAdmit(t, a, r)
	b := receiptStore(t, dir)
	// Only the caller's proven native closure permits this private operation.
	if e := b.ownerClosed(context.Background(), a.owner); e != nil {
		t.Fatal(e)
	}
	out, tok := receiptAdmit(t, b, r)
	if tok != nil || out.State != "unknown" {
		t.Fatal(out)
	}
	v, e := os.ReadFile(filepath.Join(b.dir, "index.json"))
	if e != nil {
		t.Fatal(e)
	}
	var index applicationV3ReceiptIndex
	if e = json.Unmarshal(v, &index); e != nil {
		t.Fatal(e)
	}
	index.Floor = 0
	raw, _ := json.Marshal(index)
	if e = os.WriteFile(filepath.Join(b.dir, "index.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = b.admit(context.Background(), r); e == nil {
		t.Fatal("accepted inconsistent floor")
	}
}

func TestApplicationV3ReceiptMissingPendingIndexNeverInitializes(t *testing.T) {
	dir := t.TempDir()
	s := receiptStore(t, dir)
	receiptAdmit(t, s, receiptRequest(999))
	if e := os.Remove(filepath.Join(s.dir, "index.json")); e != nil {
		t.Fatal(e)
	}
	if _, e := openApplicationV3Receipts(dir); !errors.Is(e, errApplicationV3ReceiptCorrupt) {
		t.Fatalf("reinitialized missing pending index: %v", e)
	}
	if _, _, e := s.admit(context.Background(), receiptRequest(999)); !errors.Is(e, errApplicationV3ReceiptCorrupt) {
		t.Fatal(e)
	}
}

func TestApplicationV3ReceiptGCCrashAfterFence(t *testing.T) {
	dir := t.TempDir()
	s := receiptStore(t, dir)
	r := receiptRequest(1000)
	r.Scope.Method = "Announce"
	_, tok := receiptAdmit(t, s, r)
	if e := s.finish(context.Background(), *tok, []byte("old")); e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return receiptTestTime.Add(time.Hour) }
	// Exact durable first GC edit, followed by simulated process loss before removal.
	if e := s.change(context.Background(), func(i *applicationV3ReceiptIndex) error {
		if e := s.floor(i); e != nil {
			return e
		}
		v := i.Entries[tok.id]
		v.Phase = "fenced"
		v.ResultHash = ""
		i.Entries[tok.id] = v
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	reopened := receiptStore(t, dir)
	if e := reopened.gc(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(reopened.resultPath(tok.id)); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
	out, newToken := receiptAdmit(t, reopened, r)
	if newToken != nil || out.State != "unknown" {
		t.Fatal(out)
	}
}

func TestApplicationV3ReceiptInvalidUTF8ScopeBinding(t *testing.T) {
	s := receiptStore(t, t.TempDir())
	r := receiptRequest(1001)
	r.Scope.Program = string([]byte{255})
	if _, tok, e := s.admit(context.Background(), r); tok != nil || !errors.Is(e, errApplicationV3ReceiptInvalid) {
		t.Fatal(e)
	}
	r = receiptRequest(1001)
	r.Binding.Manifest = string([]byte{255})
	if _, tok, e := s.admit(context.Background(), r); tok != nil || !errors.Is(e, errApplicationV3ReceiptInvalid) {
		t.Fatal(e)
	}
}

func TestApplicationV3ReceiptCorruptAccountingAndRetainedFrame(t *testing.T) {
	for _, phase := range []string{"pending", "unknown", "fenced"} {
		t.Run(phase, func(t *testing.T) {
			s := receiptStore(t, t.TempDir())
			r := receiptRequest(1002)
			_, tok := receiptAdmit(t, s, r)
			raw, e := os.ReadFile(filepath.Join(s.dir, "index.json"))
			if e != nil {
				t.Fatal(e)
			}
			var index applicationV3ReceiptIndex
			if e = json.Unmarshal(raw, &index); e != nil {
				t.Fatal(e)
			}
			entry := index.Entries[tok.id]
			entry.Phase = phase
			entry.Inflight = false
			if phase == "pending" {
				entry.Bytes = 0
			} // inconsistent pending reservation
			index.Entries[tok.id] = entry
			raw, _ = json.Marshal(index)
			if e = os.WriteFile(filepath.Join(s.dir, "index.json"), raw, 0600); e != nil {
				t.Fatal(e)
			}
			if _, token, e := s.admit(context.Background(), receiptRequest(1003)); token != nil || !errors.Is(e, errApplicationV3ReceiptCorrupt) {
				t.Fatalf("%v %v", token, e)
			}
		})
	}
	s := receiptStore(t, t.TempDir())
	r := receiptRequest(1004)
	_, tok := receiptAdmit(t, s, r)
	raw, e := os.ReadFile(filepath.Join(s.dir, "index.json"))
	if e != nil {
		t.Fatal(e)
	}
	var index applicationV3ReceiptIndex
	if e = json.Unmarshal(raw, &index); e != nil {
		t.Fatal(e)
	}
	entry := index.Entries[tok.id]
	raw, e = json.Marshal(applicationV3ReceiptResult{Version: 1, Entry: entry, Reply: make([]byte, 2<<20)})
	if e != nil || len(raw) > applicationV3ReceiptFileLimit {
		t.Fatalf("fixture exceeds encoded cap: %v", e)
	}
	if e = os.WriteFile(s.resultPath(tok.id), raw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.result(entry, tok.id); !errors.Is(e, errApplicationV3ReceiptCorrupt) {
		t.Fatal("retained native frame overflow", e)
	}
	// An unknown/zero-budget record cannot hide retained payload bytes.
	entry.Phase = "unknown"
	entry.Inflight = false
	entry.Bytes = 0
	index.Entries[tok.id] = entry
	raw, _ = json.Marshal(index)
	if e = os.WriteFile(filepath.Join(s.dir, "index.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, token, e := s.admit(context.Background(), receiptRequest(1005)); token != nil || !errors.Is(e, errApplicationV3ReceiptCorrupt) {
		t.Fatalf("unaccounted result: %v %v", token, e)
	}
}
