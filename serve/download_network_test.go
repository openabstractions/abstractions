package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-download/go/netcost"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

// withPlatformNetworkCost makes every runtime this test starts see source as
// its platform's network cost source.
func withPlatformNetworkCost(t *testing.T, source netcost.Source, err error) {
	t.Helper()
	previous := downloadserve.PlatformNetworkCost
	downloadserve.PlatformNetworkCost = func() (netcost.Source, error) { return source, err }
	t.Cleanup(func() { downloadserve.PlatformNetworkCost = previous })
}

// A runtime with no network cost source does not resolve for --unmetered: the
// guarantee is unmet at resolution and no identity is created (DL-N2).
func TestDownloadUnmeteredIsUnmetWithoutACostSource(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	withPlatformNetworkCost(t, nil, netcost.ErrUnavailable)
	endpoint := serviceRuntime(t)
	source := served(t, []byte("never fetched"))
	var out, diagnostics bytes.Buffer
	err := downloadCommand([]string{source, "--unmetered", "--endpoint", endpoint, "--json", "--timeout", "30s"}, &out, &diagnostics)
	exit := assertExit(t, err, exitNotResolved, "download --unmetered without a cost source")
	if !strings.Contains(exit.Error(), "unmet_requirements") {
		t.Fatalf("refusal does not name the unmet guarantee: %v", exit)
	}
	listed := runJSON(t, "jobs list", func(out, diag io.Writer) error {
		return jobsCommand([]string{"list", "--endpoint", endpoint, "--json"}, out, diag)
	})
	if len(listed.Snapshots) != 0 {
		t.Fatalf("an unmet request created work: %+v", listed.Snapshots)
	}
}

// download --unmetered on a metered path is accepted with network-cost@1 and
// waits; jobs list and show name the wait beside the label; the path turning
// unmetered completes it with the exact bytes (DL-N3 to DL-N5, JOB-A15).
func TestDownloadUnmeteredWaitsAndJobsShowWhy(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	fake := netcost.NewFake(netcost.Metered)
	withPlatformNetworkCost(t, fake, nil)
	endpoint := serviceRuntime(t)
	body := bytes.Repeat([]byte("unmetered "), 8192)
	sum := sha256.Sum256(body)
	source := served(t, body)

	submitted := runJSON(t, "download --unmetered --no-wait", func(out, diag io.Writer) error {
		return downloadCommand([]string{source, "--unmetered", "--sha256", hex.EncodeToString(sum[:]), "--endpoint", endpoint, "--no-wait", "--json"}, out, diag)
	})
	operation := submitted.Receipt.OperationID
	if operation == "" || !slices.Contains(submitted.Receipt.AcceptedGuarantees, downloadserve.NetworkCostGuarantee) {
		t.Fatalf("receipt lacks the network-cost guarantee: %+v", submitted.Receipt)
	}
	var shown decoded
	deadline := time.Now().Add(30 * time.Second)
	for {
		var out bytes.Buffer
		if err := jobsCommand([]string{"show", operation, "--endpoint", endpoint, "--json"}, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		var raw struct {
			Snapshot struct {
				State   string `json:"state"`
				Waiting string `json:"waiting"`
			} `json:"snapshot"`
		}
		if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		// The word is written while the lease is held; the work reads pending
		// once the lease is released.
		if raw.Snapshot.Waiting == "network:metered" && raw.Snapshot.State == "pending" {
			json.Unmarshal(out.Bytes(), &shown)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("jobs show never reported the wait: %s", out.Bytes())
		}
		time.Sleep(50 * time.Millisecond)
	}
	var table, text bytes.Buffer
	if err := jobsCommand([]string{"list", "--endpoint", endpoint}, &table, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.String(), "127.0.0.1 · thing.bin (derived) (waiting: network:metered)") {
		t.Fatalf("jobs list does not show the wait beside the label:\n%s", table.String())
	}
	if err := jobsCommand([]string{"show", operation, "--endpoint", endpoint}, &text, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "  waiting    network:metered\n") {
		t.Fatalf("jobs show:\n%s", text.String())
	}

	fake.Set(netcost.Unmetered)
	sink := filepath.Join(t.TempDir(), "thing.bin")
	waited := runJSON(t, "jobs wait", func(out, diag io.Writer) error {
		return jobsCommand([]string{"wait", operation, "--endpoint", endpoint, "--json", "--timeout", "60s"}, out, diag)
	})
	if waited.Snapshot == nil || waited.Snapshot.State != "complete" {
		t.Fatalf("waited work did not complete: %+v", waited.Snapshot)
	}
	runJSON(t, "jobs result", func(out, diag io.Writer) error {
		return jobsCommand([]string{"result", operation, "--out", sink, "--endpoint", endpoint, "--json"}, out, diag)
	})
	if got, err := os.ReadFile(sink); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("result differs: %v", err)
	}
}

// The waiting word stands beside the label in text and JSON; a word outside
// the contract's alphabet cannot reshape the table.
func TestJobsPrintTheWaitingWord(t *testing.T) {
	waiting := labelled("waiting", "example.com · big.gguf", true)
	waiting.State, waiting.Waiting = api.WorkStatePending, "network:metered"
	hostile := labelled("hostile", "x", false)
	hostile.Waiting = "two\nlines"
	var table bytes.Buffer
	if err := printSnapshots(&table, []api.OperationSnapshot{waiting, hostile}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.String(), "example.com · big.gguf (derived) (waiting: network:metered)") || !strings.Contains(table.String(), "x (waiting: unnamed)") {
		t.Fatalf("table:\n%s", table.String())
	}
	var document bytes.Buffer
	if err := writeJSON(&document, projectSnapshot(&waiting)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(document.String(), `"waiting":"network:metered"`) {
		t.Fatalf("json: %s", document.String())
	}
}
