package main

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/go/privatedir/privatetest"

	download "github.com/openabstractions/abstraction-download/go"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

const gib = 1 << 30

func TestManagedDownloadFreeSpaceFloor(t *testing.T) {
	for _, test := range []struct {
		name        string
		free, total uint64
		additional  int64
		refused     bool
	}{
		{"small volume boundary", 6 * gib, 40 * gib, 6*gib - 40*gib/50, false},
		{"small volume below its 2% floor", 6 * gib, 40 * gib, 6*gib - 40*gib/50 + 1, true},
		{"large volume floor capped at 1 GiB", 12 * gib, 1000 * gib, 11 * gib, false},
		{"large volume below the floor", 12 * gib, 1000 * gib, 11*gib + 1, true},
		{"below the comfort reserve only warns", 12 * gib, 1000 * gib, 2 * gib, false},
		{"already below the floor", gib / 2, 1000 * gib, 1, true},
		{"no new bytes", gib / 2, 1000 * gib, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := checkDownloadReserve("work", test.additional, func(path string) (uint64, uint64, error) {
				if path != "work" {
					t.Fatal(path)
				}
				return test.free, test.total, nil
			})
			if test.refused {
				if !errors.Is(err, download.ErrDiskFloor) || !download.Permanent(err) {
					t.Fatal("expected terminal space refusal", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// diskSpaceTest serves the managed native download executor through the
// acceptance provider with a disk of the given size.
type diskSpaceTest struct {
	provider *acceptanceprovider.Provider
	requests atomic.Int64
	url      string
}

func newDiskSpaceTest(t *testing.T, disk func(folder string) (free, total uint64, err error), serve http.HandlerFunc) *diskSpaceTest {
	t.Helper()
	d := &diskSpaceTest{}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.requests.Add(1)
		serve(w, r)
	}))
	t.Cleanup(upstream.Close)
	d.url = upstream.URL
	oldTransport := http.DefaultTransport
	transport := oldTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // controlled httptest certificate
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	read := localDownloadDiskSpace
	localDownloadDiskSpace = disk
	t.Cleanup(func() { localDownloadDiskSpace = read })
	root := privatetest.Dir(t)
	executor, err := managedDownloadExecutor(root, nil, "native", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.provider, err = acceptanceprovider.OpenWithExecutor(root, "runtime-download", executor); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d *diskSpaceTest) run(t *testing.T, size int64) *api.OperationSnapshot {
	t.Helper()
	history, err := d.provider.Bind("caller").GetHistoryWindow()
	if err != nil {
		t.Fatal(err)
	}
	raw := request.Encode(&request.Request{Artifact: request.Artifact{Size: size}, Sources: []request.Source{{Scheme: "https", Locator: d.url}}})
	identity := api.RequestIdentity{Key: "disk-space", HistoryEpoch: history.HistoryEpoch}
	accepted, err := d.provider.Bind("caller").Submit(api.Submission{Identity: identity, Kind: download.Kind, Spec: raw, RequiredGuarantees: []string{acceptanceprovider.GuaranteeReconciliation}})
	if err != nil || accepted.Receipt == nil {
		t.Fatalf("submit = %+v err=%v", accepted, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		runDownloadExecutorPass(t, d.provider)
		observed, err := d.provider.BindOperations("caller").ObserveWork(identity)
		if err != nil || observed.Snapshot == nil {
			t.Fatalf("observed = %+v err=%v", observed, err)
		}
		if state := observed.Snapshot.State.String(); state == "complete" || state == "failed" || time.Now().After(deadline) {
			return observed.Snapshot
		}
	}
}

// The owner's disk on 2026-11-22: 12 GiB free on a 1 TB volume.
func ownerDisk(string) (uint64, uint64, error) { return 12 * gib, 1000 * gib, nil }

func TestManagedDownloadOfThirtyOneBytesRunsWithTwelveGiBFree(t *testing.T) {
	body := []byte("thirty-one bytes of a small file")[:31]
	d := newDiskSpaceTest(t, ownerDisk, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	snapshot := d.run(t, int64(len(body)))
	if snapshot.State.String() != "complete" {
		t.Fatalf("snapshot = %+v failure = %+v", *snapshot, snapshot.Failure)
	}
}

func TestManagedDownloadLargerThanFreeSpaceIsRefusedNamingItsSize(t *testing.T) {
	d := newDiskSpaceTest(t, ownerDisk, func(http.ResponseWriter, *http.Request) { t.Error("a refused download reached its source") })
	snapshot := d.run(t, 20*gib)
	if snapshot.State.String() != "failed" || snapshot.Failure == nil {
		t.Fatalf("snapshot = %+v", *snapshot)
	}
	want := "not enough disk space: this download needs 20.0 GiB and the disk has 12.0 GiB free; OpenAbstractions keeps 1.0 GiB free"
	if snapshot.Failure.Message != want || snapshot.Failure.Classification != api.FailureClassPermanent {
		t.Fatalf("failure = %+v", *snapshot.Failure)
	}
	if d.requests.Load() != 0 {
		t.Fatal("the source was asked for a refused download", d.requests.Load())
	}
}

func TestManagedDownloadOfUnknownSizeStopsAtTheFloor(t *testing.T) {
	const room = 40 << 20 // free bytes above the 1 GiB floor before the download
	var written atomic.Int64
	disk := func(folder string) (uint64, uint64, error) {
		entries, err := os.ReadDir(folder)
		if err != nil {
			return 0, 0, err
		}
		var used int64
		for _, entry := range entries {
			if info, err := entry.Info(); err == nil {
				used += info.Size()
			}
		}
		written.Store(used)
		return uint64(gib + room - used), 1000 * gib, nil
	}
	chunk := []byte(strings.Repeat("x", 1<<20))
	d := newDiskSpaceTest(t, disk, func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 100; i++ {
			if _, err := w.Write(chunk); err != nil || r.Context().Err() != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	})
	snapshot := d.run(t, 0)
	if snapshot.State.String() != "failed" || snapshot.Failure == nil || !strings.HasPrefix(snapshot.Failure.Message, "not enough disk space: ") {
		t.Fatalf("snapshot = %+v failure = %+v", *snapshot, snapshot.Failure)
	}
	if got := written.Load(); got <= 0 || got > room {
		t.Fatalf("the download wrote %d bytes with %d free above the floor", got, room)
	}
}
