package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	core "github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	client "github.com/openabstractions/abstraction-facade/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	wire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	logservice "github.com/openabstractions/abstraction-logging/go/service"
)

// panelLoggingRuntime hosts a runtime whose logging service keeps a file
// history, optionally narrowed by a history policy.
func panelLoggingRuntime(t *testing.T, policy logservice.HistoryPolicy) string {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("logging history requires Program-bound IPC; native macOS proof remains unavailable")
	}
	path := filepath.Join(t.TempDir(), "history", "records.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	sink, err := logging.OpenFileSink(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sink.Close() })
	panelConfigRuntimeWith(t, func(o *host.Options) {
		o.Sink = sink
		o.LogHistoryPolicy = policy
	})
	return path
}

// newTestPanel returns a panel whose own logging queue is drained when the test ends.
func newTestPanel(t *testing.T) *servicePanel {
	p := &servicePanel{}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		p.log.close(ctx)
	})
	return p
}

func stubSelection(t *testing.T, err error) {
	previous := selectInstalled
	selectInstalled = func(context.Context) (core.Selection, error) { return core.Selection{}, err }
	t.Cleanup(func() { selectInstalled = previous })
}

func sameFile(a, b string) bool {
	x, errX := os.Stat(a)
	y, errY := os.Stat(b)
	return errX == nil && errY == nil && os.SameFile(x, y)
}

func claim(program string) wire.Attestation {
	return wire.Attestation{By: "self", Hop: 0, Program: program, UID: -1, GID: -1, PID: int64(os.Getpid())}
}

func TestPanelLoggingAndIdentityWithNoRuntime(t *testing.T) {
	dir := own(t)
	principal, _, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	absent := client.NewVerified(listen.Endpoint(fmt.Sprintf("absent-panel-logging-%d", time.Now().UnixNano())), listen.ServerExpectation{Principal: principal, Program: exe})
	previous := panelMachine
	panelMachine = func() *facade.Machine { return absent }
	t.Cleanup(func() { panelMachine = previous })
	stubSelection(t, core.ErrNoTrustedInstallation)
	h := newTestPanel(t).handler("test-key")

	for _, path := range []string{"/logging", "/logging?follow=1&wait=0"} {
		r := panelRequest(t, h, path, nil)
		if r.Code != 503 || !strings.HasPrefix(r.Body.String(), "The runtime is absent: ") || !strings.Contains(r.Body.String(), "runtime_unavailable") {
			t.Fatalf("%s with no runtime: %d %s", path, r.Code, r.Body.String())
		}
	}
	r := panelRequest(t, h, "/identity", nil)
	view := decodePanel[identityView](t, r.Code, r.Body.Bytes())
	if view.Selection.Status != "UNTRUSTED" || view.Runtime.Outcome != "" || !view.Runtime.Absent || view.Runtime.PID != -1 {
		t.Fatalf("identity runtime with no runtime %+v %+v", view.Selection, view.Runtime)
	}
	if !view.Logging.Absent || view.Logging.Stamp != nil || view.Logging.Record != nil {
		t.Fatalf("identity logging with no runtime %+v", view.Logging)
	}
	if view.CapabilityError == "" || len(view.Capabilities) != len(panelContracts) {
		t.Fatalf("capabilities with no runtime %+v %s", view.Capabilities, view.CapabilityError)
	}
	for _, c := range view.Capabilities {
		if c.Status != "unobserved" {
			t.Fatalf("absent runtime reported %s for %s", c.Status, c.Contract)
		}
	}
	if len(view.Declarations) == 0 || len(view.Limits) == 0 || view.Ceiling.Transport == "" {
		t.Fatalf("platform facts missing %+v", view)
	}
	if state := newTestPanel(t); state.log.state().State != "not bound" {
		t.Fatal("fresh panel sink claimed a binding")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "machine" {
			t.Fatalf("absent runtime left a provider file: %s", entry.Name())
		}
	}
}

func TestPanelLoggingShowsHopsGapsAndFilters(t *testing.T) {
	own(t)
	panelLoggingRuntime(t, nil)
	h := newTestPanel(t).handler("test-key")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	writer, err := panelMachine().ResolveLog(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).Format("2006-01-02T15:04:05.000000Z") }
	relay := wire.Attestation{By: "so_peercred", Verified: true, Hop: 1, Exe: "/usr/bin/relayed-writer", UID: 1000, GID: 1000, PID: 42}
	for _, record := range []wire.Record{
		{Schema: 1, Time: at(0), Level: 0, Msg: "verified record", Identity: []wire.Attestation{claim("panel-test-writer")}},
		{Schema: 1, Time: at(time.Millisecond), Level: 0, Msg: "bare record"},
		{Schema: 1, Time: at(2 * time.Millisecond), Level: 0, Msg: "relayed record", Identity: []wire.Attestation{claim("relayed-writer"), relay}},
		{Schema: 1, Time: at(3 * time.Millisecond), Level: 4, Msg: logging.GapMessage, Identity: []wire.Attestation{claim("gap-writer")},
			Attrs: map[string]string{"dropped": "3", "since": at(-time.Second), "until": at(3 * time.Millisecond)}},
	} {
		if err := writer.WriteContext(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	var page logView
	for {
		r := panelRequest(t, h, "/logging?cursor=", nil)
		page = decodePanel[logView](t, r.Code, r.Body.Bytes())
		if len(page.Records) == 4 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("history never held the four records: %+v", page)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if page.Mode != "read" || page.Outcome != "page" || !page.AtEnd || page.Next == "" {
		t.Fatalf("read page %+v", page)
	}
	exe, _ := os.Executable()
	byMessage := map[string]logRecordView{}
	for _, record := range page.Records {
		byMessage[record.Message] = record
	}

	verified := byMessage["verified record"]
	if verified.Kind != "record" || verified.Program != "panel-test-writer" || len(verified.Hops) != 2 || !verified.Author {
		t.Fatalf("verified record %+v", verified)
	}
	if h0 := verified.Hops[0]; h0.Role != "writer claim" || h0.Standing != "claimed" || h0.Verified {
		t.Fatalf("writer claim hop %+v", h0)
	}
	if h1 := verified.Hops[1]; h1.Role != "writer" || h1.Standing != "established" || !h1.Verified || h1.By != "identity/"+runtime.GOOS || !sameFile(h1.Exe, exe) || h1.PID != os.Getpid() {
		t.Fatalf("service stamp hop %+v", h1)
	}

	bare := byMessage["bare record"]
	if len(bare.Hops) != 2 || bare.Hops[0].By != "unclaimed" || bare.Hops[0].Role != "no writer claim" || bare.Hops[0].Standing != "claimed" ||
		bare.Hops[1].Standing != "established" || bare.Attrs["logging.writer_claim"] != "absent" {
		t.Fatalf("unclaimed record %+v", bare)
	}

	relayed := byMessage["relayed record"]
	if len(relayed.Hops) != 3 || relayed.Hops[1].Standing != "asserted" || relayed.Hops[1].Role != "writer" ||
		relayed.Hops[2].Standing != "established" || relayed.Hops[2].Role != "relay" || relayed.Author {
		t.Fatalf("relayed record %+v", relayed)
	}

	gap := byMessage[logging.GapMessage]
	if gap.Kind != "sink_gap" || gap.Dropped != "3" || gap.Program != "gap-writer" || gap.Since == "" || gap.Until == "" {
		t.Fatalf("sink-loss gap record %+v", gap)
	}

	filtered := func(query string) logView {
		t.Helper()
		r := panelRequest(t, h, "/logging?cursor=&"+query, nil)
		return decodePanel[logView](t, r.Code, r.Body.Bytes())
	}
	if v := filtered("program=RELAYED"); len(v.Records) != 1 || v.Records[0].Message != "relayed record" || v.Hidden != 3 {
		t.Fatalf("program filter %+v", v)
	}
	if v := filtered("level=8"); len(v.Records) != 1 || v.Records[0].Kind != "sink_gap" {
		t.Fatalf("level filter must keep the sink-loss gap: %+v", v)
	}
	if v := filtered("since=" + url.QueryEscape(now.Add(time.Hour).Format(time.RFC3339))); len(v.Records) != 0 || v.Hidden != 4 {
		t.Fatalf("time filter %+v", v)
	}
	if v := filtered("until=" + url.QueryEscape(at(time.Millisecond))); len(v.Records) != 2 {
		t.Fatalf("until filter %+v", v)
	}
	for _, bad := range []string{"level=loud", "since=yesterday", "wait=60000&follow=1"} {
		if r := panelRequest(t, h, "/logging?"+bad, nil); r.Code != 400 {
			t.Fatalf("invalid query %s reached the service: %d", bad, r.Code)
		}
	}

	end := page.Next
	r := panelRequest(t, h, "/logging?follow=1&wait=50&cursor="+url.QueryEscape(end), nil)
	if v := decodePanel[logView](t, r.Code, r.Body.Bytes()); v.Mode != "follow" || v.Outcome != "page" || len(v.Records) != 0 || v.Next != end || !v.AtEnd {
		t.Fatalf("follow at end %+v", v)
	}
	if err := writer.LogContext(ctx, 8, "later record", nil); err != nil {
		t.Fatal(err)
	}
	r = panelRequest(t, h, "/logging?follow=1&wait=5000&cursor="+url.QueryEscape(end), nil)
	if v := decodePanel[logView](t, r.Code, r.Body.Bytes()); v.Outcome != "page" || len(v.Records) != 1 || v.Records[0].Message != "later record" || v.Records[0].LevelName != "ERROR" {
		t.Fatalf("follow delivered %+v", v)
	}

	r = panelRequest(t, h, "/logging?follow=1&wait=0&cursor="+url.QueryEscape("expired-epoch:0"), nil)
	if v := decodePanel[logView](t, r.Code, r.Body.Bytes()); v.Outcome != "gap" || len(v.Records) != 0 || v.Next != "expired-epoch:0" || v.AtEnd {
		t.Fatalf("observer gap %+v", v)
	}
	r = panelRequest(t, h, "/logging?cursor="+url.QueryEscape("expired-epoch:0"), nil)
	if v := decodePanel[logView](t, r.Code, r.Body.Bytes()); v.Outcome != "gap" || len(v.Records) != 0 {
		t.Fatalf("reader gap %+v", v)
	}
}

func TestPanelIdentityShowsHowServicesBoundThePanel(t *testing.T) {
	own(t)
	panelLoggingRuntime(t, nil)
	stubSelection(t, &core.UnsupportedPlatformError{Platform: "macos"})
	p := newTestPanel(t)
	h := p.handler("test-key")
	r := panelRequest(t, h, "/identity", nil)
	view := decodePanel[identityView](t, r.Code, r.Body.Bytes())
	principal, account, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	if view.Selection.Status != "PROOF_UNAVAILABLE" {
		t.Fatalf("selection %+v", view.Selection)
	}
	rt := view.Runtime
	want := account
	if principal.Kind == "posix" {
		want = fmt.Sprint(principal.UID)
	}
	if rt.Outcome != "observed" || rt.Account != want || !sameFile(rt.Program, exe) || rt.PID != int64(os.Getpid()) || rt.Mechanism != "identity/"+runtime.GOOS || len(rt.Attributes) != 5 {
		t.Fatalf("runtime caller %+v", rt)
	}
	limits := identity.Ceiling()
	if rt.Attributes[2].Attribute != "path" || rt.Attributes[2].Ceiling != limits.Best.Path.String() || rt.Transport != limits.Transport {
		t.Fatalf("runtime proof rungs %+v", rt.Attributes)
	}
	statuses := map[string]string{}
	for _, c := range view.Capabilities {
		statuses[c.Contract] = c.Status
	}
	if statuses["abstraction.logging/reader@1"] != "resolved" || statuses["abstraction.config/editor@1"] != "resolved" || statuses["abstraction.job/acceptance@1"] != "unavailable" {
		t.Fatalf("per-contract resolution for the panel %+v", statuses)
	}
	L := view.Logging
	if L.Outcome != "found" || L.Claim == nil || L.Claim.Program != panelProgram || L.Claim.Standing != "claimed" {
		t.Fatalf("logging identity claim %+v", L)
	}
	if L.Stamp == nil || L.Stamp.Standing != "established" || !sameFile(L.Stamp.Exe, exe) || L.Stamp.PID != os.Getpid() || L.Agrees == nil || !*L.Agrees {
		t.Fatalf("logging identity stamp %+v", L)
	}
	// A second check starts again at the history end.
	r = panelRequest(t, h, "/identity", nil)
	if again := decodePanel[identityView](t, r.Code, r.Body.Bytes()); again.Logging.Outcome != "found" || again.Logging.Record.Attrs["panel.probe"] == L.Record.Attrs["panel.probe"] {
		t.Fatalf("second identity check %+v", again.Logging)
	}
	r = panelRequest(t, h, "/logging?cursor=", nil)
	sink := decodePanel[logView](t, r.Code, r.Body.Bytes()).Sink
	if !sink.Adopted || sink.Accepted != 2 || sink.Dropped != 0 || sink.State != "delivering" {
		t.Fatalf("panel sink state %+v", sink)
	}
	if !bytes.Contains(r.Body.Bytes(), []byte(`"panel identity check"`)) {
		t.Fatal("the panel's own records are missing from history")
	}
}

// The identity check starts at the history end [reader@1 end cursor], so
// retained history it cannot page through does not decide it.
func TestPanelIdentityStartsAtTheHistoryEnd(t *testing.T) {
	own(t)
	path := panelLoggingRuntime(t, nil)
	stubSelection(t, core.ErrNoTrustedInstallation)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("not a retained record\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	h := newTestPanel(t).handler("test-key")
	if r := panelRequest(t, h, "/logging?cursor=", nil); decodePanel[logView](t, r.Code, r.Body.Bytes()).Outcome != "corrupt" {
		t.Fatalf("history from the start was readable: %s", r.Body.String())
	}
	r := panelRequest(t, h, "/identity", nil)
	if view := decodePanel[identityView](t, r.Code, r.Body.Bytes()); view.Logging.Outcome != "found" || view.Logging.Stamp == nil {
		t.Fatalf("identity check behind unreadable history %+v", view.Logging)
	}
	r = panelRequest(t, h, "/logging?follow=1&wait=50&cursor=end", nil)
	if v := decodePanel[logView](t, r.Code, r.Body.Bytes()); v.Mode != "follow" || v.Outcome != "page" || len(v.Records) != 0 || !v.AtEnd || v.Next == "" || v.Next == "end" {
		t.Fatalf("follow from the end %+v", v)
	}
}

func TestPanelIdentityRefusals(t *testing.T) {
	own(t)
	t.Run("untrusted runtime", func(t *testing.T) {
		panelLoggingRuntime(t, nil)
		stubSelection(t, core.ErrNoTrustedInstallation)
		principal, _, err := currentPrincipal()
		if err != nil {
			t.Fatal(err)
		}
		endpoint := os.Getenv("ABSTRACTION_RUNTIME_ENDPOINT")
		impostor := client.NewVerified(endpoint, listen.ServerExpectation{Principal: principal, Program: filepath.Join(t.TempDir(), "openabstractions.exe")})
		previous := panelMachine
		panelMachine = func() *facade.Machine { return impostor }
		t.Cleanup(func() { panelMachine = previous })
		h := newTestPanel(t).handler("test-key")
		r := panelRequest(t, h, "/identity", nil)
		view := decodePanel[identityView](t, r.Code, r.Body.Bytes())
		if view.Runtime.Outcome != "" || view.Runtime.Account != "" || !strings.Contains(view.Runtime.Error, "not trusted") {
			t.Fatalf("untrusted runtime echoed a caller %+v", view.Runtime)
		}
		if view.Logging.Stamp != nil || !strings.Contains(view.Logging.Error, "not trusted") {
			t.Fatalf("untrusted runtime logging %+v", view.Logging)
		}
		if _, err := impostor.ObserveCaller(context.Background()); !errors.Is(err, listen.ErrServerUntrusted) {
			t.Fatalf("caller echo from an untrusted server: %v", err)
		}
		if r := panelRequest(t, h, "/logging", nil); r.Code != 503 || !strings.Contains(r.Body.String(), "not trusted") {
			t.Fatalf("history from an untrusted runtime: %d %s", r.Code, r.Body.String())
		}
	})
	t.Run("history refused", func(t *testing.T) {
		panelLoggingRuntime(t, func(context.Context, *identity.Peer) error { return errors.New("not this panel") })
		stubSelection(t, core.ErrNoTrustedInstallation)
		h := newTestPanel(t).handler("test-key")
		r := panelRequest(t, h, "/identity", nil)
		view := decodePanel[identityView](t, r.Code, r.Body.Bytes())
		if view.Runtime.Outcome != "observed" {
			t.Fatalf("runtime echo under a history policy %+v", view.Runtime)
		}
		if view.Logging.Outcome != "error" || view.Logging.Absent || view.Logging.Stamp != nil || !strings.Contains(view.Logging.Error, "forbidden") {
			t.Fatalf("refused history showed a stamp %+v", view.Logging)
		}
		if r := panelRequest(t, h, "/logging", nil); r.Code != 503 || !strings.Contains(r.Body.String(), "forbidden") {
			t.Fatalf("refused history read: %d %s", r.Code, r.Body.String())
		}
	})
}

func TestPanelSelectionStatusWords(t *testing.T) {
	ok := core.Selection{Endpoint: "e", Server: listen.ServerExpectation{Principal: identity.User{Kind: "posix", UID: 1000, GID: -1}, Program: "/opt/oa/openabstractions"}}
	for _, c := range []struct {
		selected core.Selection
		err      error
		want     string
	}{
		{ok, nil, "TRUSTED"},
		{core.Selection{}, core.ErrNoTrustedInstallation, "UNTRUSTED"},
		{core.Selection{}, core.ErrAmbiguousInstallation, "UNTRUSTED"},
		{core.Selection{}, &core.UnsupportedPlatformError{Platform: "macos"}, "PROOF_UNAVAILABLE"},
		{core.Selection{}, core.ErrUnsupportedSelection, "PROOF_UNAVAILABLE"},
		{core.Selection{}, context.DeadlineExceeded, "TIMEOUT"},
	} {
		got := selectionStatus(c.selected, c.err)
		if got.Status != c.want || (c.err == nil && (got.Account != "1000" || got.Program != ok.Server.Program)) || (c.err != nil && got.Detail == "") {
			t.Fatalf("selection %v: %+v, want %s", c.err, got, c.want)
		}
	}
	if limits := identityLimits("darwin", nil, identity.Limits{Bindable: true}); !strings.Contains(limits[0], "protected calls are refused") {
		t.Fatalf("macOS limits %q", limits)
	}
}

func TestPanelPlatformDeclarationIsTheRepositoryCopy(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "docs", "platforms.json"))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("published Panel tree: docs/platforms.json is not beside it")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n")), bytes.ReplaceAll(platformDeclaration, []byte("\r\n"), []byte("\n"))) {
		t.Fatal("monitor/platforms.json differs from docs/platforms.json; copy docs/platforms.json to monitor/platforms.json")
	}
	for _, goos := range []string{"windows", "linux", "darwin"} {
		views, err := declarationsFor(goos)
		if err != nil || len(views) == 0 {
			t.Fatalf("no declaration for %s: %v", goos, err)
		}
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(platformDeclaration, &document); err != nil || document["platforms"] == nil {
		t.Fatalf("declaration shape: %v", err)
	}
}
