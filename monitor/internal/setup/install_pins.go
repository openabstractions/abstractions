package setup

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// install_pins.json is the append-only record of every exact app package pin
// a Setup build has carried. A release that changes a package pin appends
// the new pin here; every other recorded pin for this platform becomes a
// PreviousInstallPins entry, so installed trees of earlier pins stay
// recognizable without a hand-written carry-forward list.
//
//go:embed install_pins.json
var installPinsJSON []byte

type installPinRecord struct {
	Schema int               `json:"schema"`
	Pins   []KnownInstallPin `json:"pins"`
}

func parseInstallPins(data []byte) ([]KnownInstallPin, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record installPinRecord
	if err := decoder.Decode(&record); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected content after the install pin record")
	}
	if record.Schema != 1 || record.Pins == nil {
		return nil, errors.New("install pin record needs schema 1 and a pins list")
	}
	type folder struct{ component, identity, version string }
	seen := map[folder]string{}
	for i, pin := range record.Pins {
		switch pin.ComponentID {
		case "comfyui-app", "lemonade-engine", "thenoise-engine", "pytorch-engine":
		default:
			return nil, fmt.Errorf("install pin %d names unknown component %q", i, pin.ComponentID)
		}
		if err := pin.Artifact.validate(); err != nil {
			return nil, fmt.Errorf("install pin %d: %w", i, err)
		}
		if pin.Artifact.Action != "archive_per_user" {
			return nil, fmt.Errorf("install pin %d has no portable archive identity", i)
		}
		// A version names one folder. Different bytes under a reused version
		// would make the new package overwrite or shadow the installed one.
		key := folder{pin.ComponentID, pin.Artifact.InstallIdentity, foldName(pin.Artifact.Version)}
		if prior, ok := seen[key]; ok && prior != pin.Artifact.SHA256 {
			return nil, fmt.Errorf("install pin %d reuses version %q of %s for different bytes", i, pin.Artifact.Version, pin.ComponentID)
		}
		seen[key] = pin.Artifact.SHA256
	}
	return record.Pins, nil
}

// previousInstallPins returns the recorded pins for this target that differ
// from the catalog's current pin of the same component.
func previousInstallPins(c Catalog, goos, goarch string, data []byte) ([]KnownInstallPin, error) {
	pins, err := parseInstallPins(data)
	if err != nil {
		return nil, err
	}
	current := map[string]Artifact{}
	for _, component := range c.Components {
		if component.Artifact != nil {
			current[component.ID] = *component.Artifact
		}
	}
	var previous []KnownInstallPin
	for _, pin := range pins {
		if pin.Artifact.Platform != goos+"/"+goarch {
			continue
		}
		if now, ok := current[pin.ComponentID]; ok && now == pin.Artifact {
			continue
		}
		previous = append(previous, pin)
	}
	return previous, nil
}
