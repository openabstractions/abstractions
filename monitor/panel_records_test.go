package main

import (
	"encoding/json"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	rwire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
)

type observedCardTable struct {
	Resources []cardResourceState
	Titles    map[string]string
}

func decodeCardTable(t *testing.T, code int, body []byte) observedCardTable {
	t.Helper()
	carrier := decodePanel[cardTableView](t, code, body)
	view := observedCardTable{Titles: carrier.Titles}
	for _, raw := range carrier.Resources {
		record, err := rwire.DecodeResourceState([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		view.Resources = append(view.Resources, *record)
	}
	return view
}

type observedSelection struct {
	Read   *iwire.SelectionReadV2
	Change *iwire.SelectionChange
}

func decodeRegistrySelection(t *testing.T, code int, body []byte) observedSelection {
	t.Helper()
	carrier := decodePanel[registrySelectionResult](t, code, body)
	view := observedSelection{}
	if carrier.Read != "" {
		var err error
		view.Read, err = iwire.DecodeSelectionReadV2([]byte(carrier.Read))
		if err != nil {
			t.Fatal(err)
		}
	}
	if carrier.Change != "" {
		var err error
		view.Change, err = iwire.DecodeSelectionChange([]byte(carrier.Change))
		if err != nil {
			t.Fatal(err)
		}
	}
	return view
}

type observedCaller struct {
	fwire.CallerObservation
	Absent bool
	Error  string
}
type observedIdentity struct {
	identityView
	Runtime observedCaller
}

func decodeIdentityView(t *testing.T, code int, body []byte) observedIdentity {
	t.Helper()
	carrier := decodePanel[identityView](t, code, body)
	view := observedIdentity{identityView: carrier, Runtime: observedCaller{Absent: carrier.Runtime.Absent, Error: carrier.Runtime.Error}}
	if carrier.Runtime.Record != "" {
		record, err := fwire.DecodeCallerObservation([]byte(carrier.Runtime.Record))
		if err != nil {
			t.Fatal(err)
		}
		view.Runtime.CallerObservation = *record
	}
	return view
}

func TestPanelRecordCodecRoutesRemainStatic(t *testing.T) {
	h := newTestPanel(t).handler("test-key")
	for _, path := range []string{"/record-codecs/panel-records.mjs", "/record-codecs/sdk/abstraction-resource/javascript/js/abstraction/resource/internal.mjs", "/record-codecs/sdk/abstraction-logging/javascript/js/abstraction/logging/internal.mjs"} {
		r := panelRequest(t, h, path, nil)
		if r.Code != 200 || r.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
			t.Fatal(path, r.Code, r.Header())
		}
	}
	if r := panelRequest(t, h, "/record-codecs/sdk/../../panel-application.json", nil); r.Code == 200 {
		t.Fatal("codec route escaped its static closure")
	}
}

func TestPanelRegistryCandidatesUseGeneratedCodec(t *testing.T) {
	for _, raw := range []string{`{"edit":"put","name":"work","candidates":[{"model":"a","placement":"foreign"}]}`, `{"edit":"put","name":"work","candidates":[{"model":"a","model":"b","placement":"local"}]}`} {
		var edit registrySelectionEdit
		if err := json.Unmarshal([]byte(raw), &edit); err == nil {
			t.Fatal("invalid native candidate admitted", raw)
		}
	}
	var edit registrySelectionEdit
	if err := json.Unmarshal([]byte(`{"edit":"put","name":"work","candidates":[{"model":"a","placement":"local","provider":"native"}]}`), &edit); err != nil || !edit.valid() || edit.Candidates[0].Provider != "native" {
		t.Fatal(edit, err)
	}
}
