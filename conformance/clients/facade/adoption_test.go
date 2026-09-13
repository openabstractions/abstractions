package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	"github.com/openabstractions/abstraction-facade/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

// The application accepts generated capability interfaces. Provider roots and
// engine selection belong exclusively to the composition fixture below.
func adoptSubmit(t *testing.T, c api.RecoverableAcceptance, key, url string, body []byte, guarantees []string) (api.RequestIdentity, api.AcceptanceResult) {
	t.Helper()
	h, err := c.GetHistoryWindow()
	if err != nil {
		t.Fatal(err)
	}
	id := api.RequestIdentity{Key: key, HistoryEpoch: h.HistoryEpoch}
	spec := request.Encode(&request.Request{Artifact: request.Artifact{Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(body)), Size: int64(len(body))}, Sources: []request.Source{{Scheme: "http", Locator: url}}})
	result, err := c.Submit(api.Submission{Identity: id, Kind: "download", Spec: spec, RequiredGuarantees: guarantees})
	if err != nil {
		t.Fatal(err)
	}
	return id, result
}
func adoptResult(t *testing.T, ctx context.Context, c api.OperationControl, id api.RequestIdentity, want []byte) {
	t.Helper()
	for {
		result, err := c.ObserveWork(id)
		if err != nil {
			t.Fatal(err)
		}
		if result.Snapshot == nil {
			t.Fatalf("observation: %+v", result)
		}
		if result.Snapshot.State == api.WorkStateComplete {
			break
		}
		if result.Snapshot.State == "failed" {
			t.Fatalf("work failed: %+v", result.Snapshot)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	var got []byte
	var operation string
	var total int64 = -1
	for {
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
		r, err := c.ReadResult(id, int64(len(got)), 65536)
		if err != nil || r.Chunk == nil {
			t.Fatalf("read: %+v %v", r, err)
		}
		chunk := r.Chunk
		if chunk.Offset != int64(len(got)) || chunk.Total < 0 || chunk.Total > int64(len(want)) || int64(len(chunk.Data)) > chunk.Total-chunk.Offset || (len(chunk.Data) == 0 && !chunk.Eof) || chunk.Eof != (chunk.Offset+int64(len(chunk.Data)) == chunk.Total) {
			t.Fatalf("invalid/nonprogressing chunk: %+v", chunk)
		}
		if total >= 0 && (total != chunk.Total || operation != chunk.Receipt.OperationId) {
			t.Fatal("result identity/total changed")
		}
		total = chunk.Total
		operation = chunk.Receipt.OperationId
		got = append(got, chunk.Data...)
		if r.Chunk.Eof {
			break
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatal("result differs")
	}
}

func TestExplicitAdoptionWrapperRetainsAcceptedOwner(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("runtime service requires Program peer proof unavailable on current macOS identity backend")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	for _, name := range []string{"HOME", "APPDATA", "XDG_CONFIG_HOME", "ProgramData"} {
		t.Setenv(name, dir)
	}
	body := bytes.Repeat([]byte("existing HTTP engine\x00\n"), 4000)
	var calls atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseSource := func() { releaseOnce.Do(func() { close(release) }) }
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = w.Write(body)
	}))
	defer source.Close()
	defer releaseSource()
	// Explicitly supplied existing engine: no listener, discovery or daemon.
	p, err := acceptanceprovider.OpenWithExecutor(filepath.Join(dir, "local-provider"), "explicit-local-owner", downloadserve.HTTPExecution{})
	if err != nil {
		t.Fatal(err)
	}
	execCtx, stopExec := context.WithCancel(ctx)
	defer stopExec()
	execDone := make(chan error, 1)
	go func() { execDone <- p.Execute(execCtx) }()
	defer func() {
		stopExec()
		select {
		case <-execDone:
		case <-time.After(5 * time.Second):
			t.Error("local engine did not stop")
		}
	}()
	local := api.NewRecoverableAcceptanceClient(&api.RecoverableAcceptanceDispatcher{Handler: p.Bind("explicit-application")})
	localOps := api.NewOperationControlClient(&api.OperationControlDispatcher{Handler: p.BindOperations("explicit-application")})
	_, refused := adoptSubmit(t, local, "unsupported", source.URL, body, request.DownstreamRecoveryGuarantees)
	if refused.Outcome != "definitely_not_accepted" || refused.Receipt != nil || calls.Load() != 0 {
		t.Fatalf("unsupported promise: %+v requests=%d", refused, calls.Load())
	}
	id, accepted := adoptSubmit(t, local, "already-accepted", source.URL, body, acceptanceprovider.Guarantees())
	if accepted.Receipt == nil || accepted.Receipt.LogicalOwner != "explicit-local-owner" {
		t.Fatalf("local acceptance: %+v", accepted)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	endpoint := func(name string) string {
		if runtime.GOOS == "windows" {
			return fmt.Sprintf(`\\.\pipe\oa-adopt-%d-%s`, time.Now().UnixNano(), name)
		}
		return filepath.Join(dir, name+".sock")
	}
	o := host.Options{Endpoint: endpoint("resolver"), LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), JobEndpoint: endpoint("jobs"), JobRoot: filepath.Join(dir, "service-provider"), JobOwner: "new-service-owner", JobExecutor: downloadserve.HTTPExecution{}}
	machine := client.New(o.Endpoint)
	if _, err := machine.ResolveJobs(ctx, client.Requirements{}); err == nil {
		t.Fatal("absent runtime resolved")
	}
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	hostCtx, stopHost := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- h.Serve(hostCtx) }()
	defer func() {
		stopHost()
		_ = h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("host did not stop")
		}
	}()
	selected, err := machine.ResolveJobs(ctx, client.Requirements{Guarantees: acceptanceprovider.Guarantees()})
	if err != nil {
		t.Fatal(err)
	}
	future := selected.WithContext(ctx)
	futureOps := future
	next, nextAccepted := adoptSubmit(t, future, "future-work", source.URL, body, acceptanceprovider.Guarantees())
	if nextAccepted.Receipt == nil || nextAccepted.Receipt.LogicalOwner != "new-service-owner" {
		t.Fatalf("service acceptance: %+v", nextAccepted)
	}
	unchanged, err := local.Reconcile(id)
	if err != nil || !reflect.DeepEqual(unchanged.Receipt, accepted.Receipt) {
		t.Fatalf("local owner/receipt changed: %+v %v", unchanged, err)
	}
	releaseSource()
	adoptResult(t, ctx, localOps, id, body)
	adoptResult(t, ctx, futureOps, next, body)
	if calls.Load() != 2 {
		t.Fatalf("external effects=%d", calls.Load())
	}
}
