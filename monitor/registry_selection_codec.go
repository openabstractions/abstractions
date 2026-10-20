package main

import (
	"bytes"
	"encoding/json"

	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
)

// The edit is a page composition; candidates retain their public codec.
type registrySelectionEnvelope registrySelectionEdit

func (e *registrySelectionEdit) UnmarshalJSON(raw []byte) error {
	var envelope struct {
		*registrySelectionEnvelope
		Candidates []json.RawMessage `json:"candidates"`
	}
	envelope.registrySelectionEnvelope = (*registrySelectionEnvelope)(e)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return err
	}
	e.Candidates = nil
	for _, raw := range envelope.Candidates {
		candidate, err := iwire.DecodeSelectionCandidateV2(raw)
		if err != nil {
			return err
		}
		e.Candidates = append(e.Candidates, *candidate)
	}
	return nil
}

func (e registrySelectionEdit) MarshalJSON() ([]byte, error) {
	candidates := make([]json.RawMessage, 0, len(e.Candidates))
	for i := range e.Candidates {
		candidates = append(candidates, iwire.EncodeSelectionCandidateV2(&e.Candidates[i]))
	}
	return json.Marshal(struct {
		registrySelectionEnvelope
		Candidates []json.RawMessage `json:"candidates"`
	}{registrySelectionEnvelope(e), candidates})
}
