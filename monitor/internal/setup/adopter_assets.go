package setup

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"strings"
)

//go:embed adopter_assets.json
var adopterAssetsJSON []byte

const maxReleaseAssetBytes = 2 * 1024 * 1024 * 1024

type adopterAssetPins struct {
	Schema     int               `json:"schema"`
	ReleaseTag string            `json:"release_tag"`
	Apps       []adopterAssetPin `json:"apps"`
}

type adopterAssetPin struct {
	ID              string `json:"id"`
	BinaryZIP       string `json:"binary_zip"`
	BinarySHA256    string `json:"binary_sha256"`
	BinaryBytes     int64  `json:"binary_bytes"`
	SourceZIP       string `json:"source_zip"`
	SourceSHA256    string `json:"source_sha256"`
	SourceBytes     int64  `json:"source_bytes"`
	SourceCommit    string `json:"source_commit"`
	SourceURL       string `json:"source_url"`
	Signer          string `json:"signer"`
	Version         string `json:"version"`
	Platform        string `json:"platform"`
	PeakBytes       int64  `json:"peak_bytes"`
	License         string `json:"license"`
	Publisher       string `json:"publisher"`
	InstallIdentity string `json:"install_identity"`
	Action          string `json:"action"`
	// RecordsSHA256 is ComfyUI's private-Python RECORD-set pin.
	RecordsSHA256 string `json:"records_sha256,omitempty"`
}

var (
	assetNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.zip$`)
	assetSHA         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	assetCommit      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	assetTag         = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	assetIdentity    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

func exactJSONFields(value any) map[string]bool {
	fields := map[string]bool{}
	typeOf := reflect.TypeOf(value)
	for i := 0; i < typeOf.NumField(); i++ {
		name, _, _ := strings.Cut(typeOf.Field(i).Tag.Get("json"), ",")
		fields[name] = true
	}
	return fields
}

func checkJSONKeys(dec *json.Decoder, allowed map[string]bool) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]bool)
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] || (allowed != nil && !allowed[key]) {
				return fmt.Errorf("duplicate or unknown JSON key %v", keyToken)
			}
			seen[key] = true
			var childKeys map[string]bool
			if key == "apps" {
				childKeys = exactJSONFields(adopterAssetPin{})
			}
			if err := checkJSONKeys(dec, childKeys); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	case json.Delim('['):
		for dec.More() {
			if err := checkJSONKeys(dec, allowed); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	default:
		return nil
	}
}

func parseAdopterAssets(data []byte) (adopterAssetPins, error) {
	keys := json.NewDecoder(bytes.NewReader(data))
	if err := checkJSONKeys(keys, exactJSONFields(adopterAssetPins{})); err != nil {
		return adopterAssetPins{}, err
	}
	if _, err := keys.Token(); !errors.Is(err, io.EOF) {
		return adopterAssetPins{}, errors.New("unexpected JSON content after app pins")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var pins adopterAssetPins
	if err := dec.Decode(&pins); err != nil {
		return pins, err
	}
	if pins.Schema != 1 || !assetTag.MatchString(pins.ReleaseTag) || pins.Apps == nil {
		return pins, errors.New("invalid app pin schema, release tag or apps list")
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, app := range pins.Apps {
		if (app.ID != string(ComfyUI) && app.ID != string(PyTorch)) || ids[app.ID] {
			return pins, fmt.Errorf("unknown or duplicate adopter app %q", app.ID)
		}
		ids[app.ID] = true
		for _, name := range []string{app.BinaryZIP, app.SourceZIP} {
			if !assetNamePattern.MatchString(name) || names[name] {
				return pins, fmt.Errorf("invalid or duplicate adopter asset %q", name)
			}
			names[name] = true
		}
		if !assetSHA.MatchString(app.BinarySHA256) || !assetSHA.MatchString(app.SourceSHA256) ||
			app.BinaryBytes <= 0 || app.BinaryBytes >= maxReleaseAssetBytes ||
			app.SourceBytes <= 0 || app.SourceBytes >= maxReleaseAssetBytes ||
			!assetCommit.MatchString(app.SourceCommit) || app.Signer != "unsigned" ||
			app.Platform != "windows/amd64" || app.PeakBytes < app.BinaryBytes ||
			app.License == "" || app.Publisher == "" || app.Action != "archive_per_user" ||
			app.Version == "" || !assetIdentity.MatchString(app.InstallIdentity) ||
			(app.ID == string(ComfyUI)) != assetSHA.MatchString(app.RecordsSHA256) ||
			app.ID != string(ComfyUI) && app.RecordsSHA256 != "" {
			return pins, fmt.Errorf("invalid pin metadata for %q", app.ID)
		}
		artifact := app.artifact(pins.ReleaseTag)
		if err := artifact.validate(); err != nil {
			return pins, fmt.Errorf("%s: %w", app.ID, err)
		}
	}
	return pins, nil
}

func (app adopterAssetPin) artifact(tag string) *Artifact {
	return &Artifact{Version: app.Version, SourceCommit: app.SourceCommit,
		SourceURL: app.SourceURL, Platform: app.Platform,
		URL: releaseAssetURL(tag, app.BinaryZIP), SHA256: app.BinarySHA256,
		Bytes: app.BinaryBytes, PeakBytes: app.PeakBytes, License: app.License,
		Publisher: app.Publisher, Signature: app.Signer,
		InstallIdentity: app.InstallIdentity, Action: app.Action,
		SourceArchiveURL: releaseAssetURL(tag, app.SourceZIP), SourceArchiveSHA256: app.SourceSHA256,
		SourceArchiveBytes: app.SourceBytes, RecordsSHA256: app.RecordsSHA256}
}

func releaseAssetURL(tag, name string) string {
	return "https://github.com/openabstractions/redist/releases/download/" +
		url.PathEscape(tag) + "/" + url.PathEscape(name)
}

func projectAdopterAssets(c *Catalog, goos, goarch string, data []byte) error {
	pins, err := parseAdopterAssets(data)
	if err != nil {
		return err
	}
	if goos != "windows" || goarch != "amd64" {
		return nil
	}
	for _, app := range pins.Apps {
		componentID := app.ID + "-app"
		if app.ID == string(PyTorch) {
			componentID = "pytorch-engine"
		}
		for i := range c.Components {
			if strings.EqualFold(c.Components[i].ID, componentID) {
				c.Components[i].Pending = ""
				c.Components[i].Artifact = app.artifact(pins.ReleaseTag)
			}
		}
	}
	return nil
}
