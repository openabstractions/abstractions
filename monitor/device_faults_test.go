package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	wire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	"github.com/openabstractions/abstraction-resource/go/gpufault"
)

// faultRuntime hosts a runtime with a file history and a registry whose
// card providers are cards. It returns a logger that writes the way a
// provider does: slog through abstraction-logging's handler, resolved
// against this runtime, so the history service stamps this test binary as
// the verified writer.
func faultRuntime(t *testing.T, registry cardRegistry) *slog.Logger {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history", "records.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	sink, err := logging.OpenFileSink(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sink.Close() })
	endpoint := ""
	panelConfigRuntimeWith(t, func(o *host.Options) {
		o.Sink = sink
		serveCardRegistry(t, o, registry)
		endpoint = o.Endpoint
	})
	principal, _, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return slog.New(logging.NewResolvedHandler("lemonaded", endpoint, listen.ServerExpectation{Principal: principal, Program: exe}))
}

// readFaults polls /card/faults until the scan reached the history's end
// and done accepts the view.
func readFaults(t *testing.T, ctx context.Context, p *servicePanel, done func(gpuFaultView) bool) gpuFaultView {
	t.Helper()
	h := p.handler("test-key")
	for {
		r := panelRequest(t, h, "/card/faults", nil)
		view := decodePanel[gpuFaultView](t, r.Code, r.Body.Bytes())
		if view.Outcome == "page" && done(view) {
			return view
		}
		select {
		case <-ctx.Done():
			t.Fatalf("faults never settled: %+v", view)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

const (
	pausedLine  = "The graphics driver reset; sd-server was stopped and the request that was running is paused."
	restartLine = "The graphics driver reset; llama-server was stopped and restarts on the next request."
)

// A reset a card provider's Guard reports is one line on the Device page
// (RES-G6). The record is written by gpufault.Fault.Log through the real
// logging handler, as lemonaded and modelhostd write it: the line is the
// group's gpu_fault.line once the handler resolves the fault's LogValue.
// Newest first; a fault older than seven days and one with no sentence add
// nothing; the next read continues from the history's end.
func TestDeviceFaultsShowACardProvidersReset(t *testing.T) {
	own(t)
	logger := faultRuntime(t, cardRegistry{cards: []string{selfExe(t)}})
	p := newTestPanel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now()
	gpufault.Fault{At: now.Add(-8 * 24 * time.Hour), Source: "WerKernel 1001 WATCHDOG", Engines: []string{"old-engine"}, Line: "The graphics driver reset; old-engine was stopped and restarts on the next request."}.Log(logger)
	gpufault.Fault{At: now.Add(-time.Hour), Source: "WerKernel 1001 WATCHDOG"}.Log(logger)
	gpufault.Fault{At: now.Add(-time.Minute), Source: "WerKernel 1001 WATCHDOG", Engines: []string{"sd-server"},
		Quarantined: []gpufault.QuarantineEntry{{Key: "k1"}}, Line: pausedLine}.Log(logger)
	view := readFaults(t, ctx, p, func(v gpuFaultView) bool { return len(v.Faults) > 0 })
	if len(view.Faults) != 1 || view.Faults[0].Line != pausedLine || view.Faults[0].By == "" || view.Faults[0].At == "" {
		t.Fatalf("faults %+v", view.Faults)
	}
	gpufault.Fault{At: time.Now(), Source: "WerKernel 1001 WATCHDOG", Engines: []string{"llama-server"}, Line: restartLine}.Log(logger)
	view = readFaults(t, ctx, p, func(v gpuFaultView) bool { return len(v.Faults) == 2 })
	if view.Faults[0].Line != restartLine || view.Faults[1].Line != pausedLine {
		t.Fatalf("newest first %+v", view.Faults)
	}
}

// Another program's "gpu fault" record is not the Guard's: a verified writer
// whose registered declaration names no card: resource, or which is not
// registered at all, shows no line.
func TestDeviceFaultsIgnoreAnotherAppsFakeReset(t *testing.T) {
	own(t)
	logger := faultRuntime(t, cardRegistry{cards: []string{filepath.Join(t.TempDir(), "lemonaded.exe")}, others: []string{selfExe(t)}})
	p := newTestPanel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	gpufault.Fault{At: time.Now(), Source: "WerKernel 1001 WATCHDOG", Engines: []string{"sd-server"}, Line: pausedLine}.Log(logger)
	logger.Info("after the fake")
	h := p.handler("test-key")
	for {
		r := panelRequest(t, h, "/card/faults", nil)
		view := decodePanel[gpuFaultView](t, r.Code, r.Body.Bytes())
		if len(view.Faults) != 0 {
			t.Fatalf("another app's record made a line: %+v", view.Faults)
		}
		if view.Outcome == "page" && len(p.gpuFaultScan().faults) == 1 {
			break // the record was read and set aside
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the record was never read: %+v", view)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// A record whose writer the history service did not verify is not shown:
// the sentence would be any program's claim about the graphics driver.
func TestGPUFaultOfNeedsAVerifiedWriter(t *testing.T) {
	record := wire.Record{Schema: 1, Time: time.Now().UTC().Format(time.RFC3339Nano), Msg: gpuFaultMessage,
		Identity: []wire.Attestation{claim("modelhostd")}, Attrs: map[string]string{gpuFaultLineKey: "The graphics driver reset."}}
	if _, ok := gpuFaultOf(record); ok {
		t.Fatal("a writer claim alone made a fault line")
	}
}

// Without a runtime the faults read says so and shows nothing.
func TestDeviceFaultsUnavailableWithoutARuntime(t *testing.T) {
	own(t)
	h := newTestPanel(t).handler("test-key")
	r := panelRequest(t, h, "/card/faults", nil)
	view := decodePanel[gpuFaultView](t, r.Code, r.Body.Bytes())
	if view.Outcome != "unavailable" || len(view.Faults) != 0 {
		t.Fatalf("faults view %+v", view)
	}
}
