package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	config "github.com/openabstractions/abstraction-config/go"
	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	configservice "github.com/openabstractions/abstraction-config/go/service"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
)

func TestPanelConfigServiceAdoption(t *testing.T) {
	own(t)
	if err := os.MkdirAll(filepath.Dir(config.UserPath()), 0700); err != nil {
		t.Fatal(err)
	}
	var policy atomic.Int32
	stop := panelConfigRuntimeWith(t, func(o *host.Options) {
		o.ConfigEditPolicy = func(_ context.Context, _ *identity.Peer) error {
			switch policy.Load() {
			case 1:
				return errors.New("denied")
			case 2:
				return configservice.ErrEditPolicyUnavailable
			}
			return nil
		}
	})
	h := (&servicePanel{}).handler("test-key")
	read := func() wire.UserSnapshot {
		t.Helper()
		r := panelRequest(t, h, "/settings", nil)
		var v wire.UserSnapshot
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &v) != nil {
			t.Fatalf("read: %d %s", r.Code, r.Body)
		}
		return v
	}
	observe := func(cursor string) wire.ConfigObservation {
		t.Helper()
		r := panelRequest(t, h, "/settings/observe?cursor="+cursor, nil)
		var v wire.ConfigObservation
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &v) != nil {
			t.Fatalf("observe: %d %s", r.Code, r.Body)
		}
		return v
	}
	replace := func(v wire.UserSnapshot) wire.UserReplaceResult {
		t.Helper()
		r := panelRequest(t, h, "/settings", v)
		var result wire.UserReplaceResult
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &result) != nil {
			t.Fatalf("replace: %d %s", r.Code, r.Body)
		}
		return result
	}
	first := observe("")
	if first.Outcome != wire.ConfigObservationOutcomeSnapshot || first.Snapshot == nil {
		t.Fatalf("first: %+v", first)
	}
	original := read()
	draft := original
	draft.Values.Store = "selected-by-user"
	applied := replace(draft)
	if applied.Outcome != wire.UserReplaceOutcomeApplied {
		t.Fatalf("apply: %+v", applied)
	}
	next := observe(first.Cursor)
	if next.Outcome != wire.ConfigObservationOutcomeSnapshot || next.Snapshot == nil || next.Snapshot.Store != "selected-by-user" {
		t.Fatalf("changed: %+v", next)
	}
	draft.Values.Store = "stale-edit"
	if result := replace(draft); result.Outcome != wire.UserReplaceOutcomeConflict || result.Snapshot.Values.Store != "selected-by-user" {
		t.Fatalf("conflict: %+v", result)
	}
	if gap := observe(strings.Repeat("0", 32) + first.Cursor[32:]); gap.Outcome != wire.ConfigObservationOutcomeGap {
		t.Fatalf("gap: %+v", gap)
	}
	for i, outcome := range []string{"forbidden", "unavailable"} {
		policy.Store(int32(i + 1))
		candidate := applied.Snapshot
		candidate.Values.Store = "must-not-land"
		result := replace(candidate)
		if result.Outcome.String() != outcome || result.Snapshot.Revision != "" {
			t.Fatalf("policy: %+v", result)
		}
		if current := read(); current.Values.Store != "selected-by-user" {
			t.Fatal("denied edit changed settings")
		}
	}
	for _, body := range []string{`{"Revision":"x","Values":{},"Extra":true}`, `{"Revision":"x","Values":{}} {}`} {
		req := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(body))
		req.Host = "127.0.0.1:8734"
		req.Header.Set("X-Panel-Key", "test-key")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("malformed edit: %d %s", w.Code, w.Body)
		}
	}
	stop()
	if r := panelRequest(t, h, "/settings", nil); r.Code != 503 {
		t.Fatalf("stopped service: %d %s", r.Code, r.Body)
	}
}
