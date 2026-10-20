package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/openabstractions/abstraction-identity/fileid"
)

// Set by the installer producer after the final provider images are staged.
// An ordinary development build carries no maintenance authority.
var shippedProviderImages string

type shippedImageReviewCandidate struct{ Key, Revision, Cause string }

func parseShippedProviderImages(encoded string) (map[string]string, error) {
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > 1<<20 {
		return nil, errors.New("shipped images: record too large")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(raw) != encoded {
		return nil, errors.New("shipped images: invalid encoding")
	}
	var record struct {
		Version int               `json:"version"`
		Images  map[string]string `json:"images"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&record); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("shipped images: trailing data")
	}
	if record.Version != 1 || len(record.Images) == 0 {
		return nil, errors.New("shipped images: unsupported record")
	}
	for name, digest := range record.Images {
		if strings.ContainsAny(name, "\\:") {
			return nil, errors.New("shipped images: invalid relative name")
		}
		for _, component := range strings.Split(name, "/") {
			if component == "" || component == "." || component == ".." || fileid.SafeName(component) != nil {
				return nil, errors.New("shipped images: invalid relative name")
			}
		}
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != digest {
			return nil, errors.New("shipped images: invalid digest")
		}
	}
	// Canonical JSON also refuses duplicate keys and alternate spellings.
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(map[string]any{"version": record.Version, "images": record.Images})
	canonical := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	// JSON is UTF-8; the producer emits these valid filename characters
	// literally rather than JavaScript's optional source-text escapes.
	canonical = bytes.ReplaceAll(canonical, []byte(`\u2028`), []byte("\u2028"))
	canonical = bytes.ReplaceAll(canonical, []byte(`\u2029`), []byte("\u2029"))
	if !bytes.Equal(raw, canonical) {
		return nil, errors.New("shipped images: noncanonical record")
	}
	return record.Images, nil
}
