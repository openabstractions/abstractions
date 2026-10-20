package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

var errAcceptanceUncertain = errors.New("Use acceptance/effect remains uncertain")

type useIntent struct {
	Version    int            `json:"version"`
	Submission api.Submission `json:"submission"`
	Existing   bool           `json:"-"`
}

func uncertain(id api.RequestIdentity) error {
	return fmt.Errorf("%w; retained key %s in epoch %s", errAcceptanceUncertain, id.Key, id.HistoryEpoch)
}

func prepareIntent(ctx context.Context, jobs jobClient, origin string, f fixture, guarantees []string, path string) (useIntent, error) {
	if data, err := os.ReadFile(path); err == nil {
		var intent useIntent
		if len(data) > 16<<10 || json.Unmarshal(data, &intent) != nil || !validIntent(intent, f, guarantees) {
			return intent, errors.New("retained Use intent is invalid; preserved without a new submission")
		}
		intent.Existing = true
		return intent, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return useIntent{}, err
	}
	window, err := jobs.GetHistoryWindow(ctx)
	if err != nil {
		return useIntent{}, err
	}
	key, err := freshKey()
	if err != nil {
		return useIntent{}, err
	}
	digest := sha256.Sum256(f.artifact)
	spec := request.Encode(&request.Request{Artifact: request.Artifact{Size: int64(len(f.artifact)), Digest: "sha256:" + hex.EncodeToString(digest[:])}, Sources: []request.Source{{Scheme: "http", Locator: origin + "/" + f.label + "?intent=" + key, Credential: f.name}}})
	intent := useIntent{Version: 1, Submission: api.Submission{Identity: api.RequestIdentity{Key: key, HistoryEpoch: window.HistoryEpoch}, Kind: "download", Spec: spec, RequiredGuarantees: guarantees, Label: "Playground Use " + f.label}}
	data, err := json.Marshal(intent)
	if err != nil {
		return intent, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return intent, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return intent, errors.New("another retained Use intent exists; repeat to reconcile it")
	}
	written, writeErr := file.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return intent, errors.New("Use intent could not be retained; no submission was sent")
	}
	return intent, nil
}

func validIntent(intent useIntent, f fixture, guarantees []string) bool {
	s := intent.Submission
	if intent.Version != 1 || s.Kind != "download" || !strings.HasPrefix(s.Identity.Key, "rights-playground-") || s.Identity.HistoryEpoch == "" || !slices.Equal(s.RequiredGuarantees, guarantees) {
		return false
	}
	spec, err := request.Decode([]byte(s.Spec))
	if err != nil || len(spec.Sources) != 1 || spec.Sources[0].Credential != f.name || spec.Sources[0].Scheme != "http" || spec.Artifact.Size != int64(len(f.artifact)) {
		return false
	}
	digest := sha256.Sum256(f.artifact)
	if spec.Artifact.Digest != "sha256:"+hex.EncodeToString(digest[:]) {
		return false
	}
	u, err := url.Parse(spec.Sources[0].Locator)
	return err == nil && u.Scheme == "http" && u.Hostname() == "127.0.0.1" && u.Port() != "" && u.User == nil && u.Path == "/"+f.label && u.Query().Get("intent") == s.Identity.Key && !bytes.Contains([]byte(s.Spec), f.secret)
}

func clearIntent(path string) error {
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("operation is terminal but local intent cleanup failed: %w", err)
	}
	return nil
}
