package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

// This private resource is embedded before Authenticode signing. The native
// verifier supplies publisher evidence; the document makes no publisher claim.
func agentRelease(catalogue []byte, channel, version string) ([]byte, error) {
	if len(channel) < 1 || len(channel) > 32 || channel[0] < 'a' || channel[0] > 'z' {
		return nil, errors.New("agent release requires an explicit channel")
	}
	for _, c := range channel {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return nil, errors.New("invalid agent release channel")
		}
	}
	parts := strings.Split(version, ".")
	var release [4]uint16
	if len(parts) != len(release) {
		return nil, errors.New("agent release requires MAJOR.MINOR.PATCH.SEQUENCE")
	}
	for i, part := range parts {
		v, err := strconv.ParseUint(part, 10, 16)
		if err != nil || strconv.FormatUint(v, 10) != part {
			return nil, errors.New("agent release requires canonical 16-bit version components")
		}
		release[i] = uint16(v)
	}
	if release == [4]uint16{} {
		return nil, errors.New("agent release version must be nonzero")
	}
	var listed struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(catalogue, &listed); err != nil {
		return nil, err
	}
	if len(listed.Tools) == 0 || len(listed.Tools) > 128 {
		return nil, errors.New("agent release tool count bound")
	}
	names := map[string]bool{}
	for _, raw := range listed.Tools {
		var tool struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		}
		if json.Unmarshal(raw, &tool) != nil || tool.Name == "" || len(tool.Name) > 128 || names[tool.Name] || len(tool.InputSchema) == 0 {
			return nil, errors.New("invalid agent release tool declaration")
		}
		names[tool.Name] = true
	}
	// These model needs declare the setup uses. Runtime tools still request
	// their own existing authority; the declaration grants no permission.
	needs := registration.Needs{Models: []registration.ModelNeed{
		{Modality: "chat", RequiredFeatures: []string{}, Placement: registration.PlacementAny},
		{Modality: "image", RequiredFeatures: []string{}, Placement: registration.PlacementAny},
		{Modality: "video", RequiredFeatures: []string{}, Placement: registration.PlacementAny},
	}}
	document := map[string]any{
		"version": 1, "component": "openabstractions-mcp", "channel": channel, "release": release,
		"requests": []json.RawMessage{}, "needs": json.RawMessage(registration.EncodeNeeds(&needs)),
		"verbs": []json.RawMessage{}, "tools": listed.Tools,
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	// Match the native parser's recursive object-key order and numeric spelling.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var canonical any
	if err := decoder.Decode(&canonical); err != nil {
		return nil, err
	}
	raw, err = json.Marshal(canonical)
	if len(raw) > 262144 {
		return nil, fmt.Errorf("agent release exceeds 262144 bytes: %d", len(raw))
	}
	return raw, err
}
