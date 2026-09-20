package main

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	facade "github.com/openabstractions/abstraction-facade/go"
)

// credentialEdit is one add, rotate or revoke the person asked the Panel to
// make. Secret travels once in the POST body; the handler zeroes it after the
// call and never returns, logs or names it in an error. add carries an empty
// expected revision; rotate and revoke carry the revision a listing showed.
type credentialEdit struct {
	Action           string   `json:"action"`
	Name             string   `json:"name"`
	Kind             string   `json:"kind"`
	Header           string   `json:"header"`
	Targets          []string `json:"targets"`
	Consumers        []string `json:"consumers"`
	Expires          string   `json:"expires"`
	Secret           string   `json:"secret"`
	ExpectedRevision string   `json:"expected_revision"`
}

var credentialNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func validCredentialName(s string) bool { return credentialNamePattern.MatchString(s) }

func validCredentialKind(kind, header string) bool {
	switch kind {
	case "bearer":
		return header == ""
	case "header":
		return rightsText(header, 128)
	}
	return false
}

func validCredentialScope(values []string, min, max int) bool {
	if len(values) < min || len(values) > max {
		return false
	}
	for _, v := range values {
		if !rightsText(v, 253) {
			return false
		}
	}
	return true
}

// valid reports whether e names a request the holder should be asked to
// perform, checking what serve/credentials.go checks before a call: a
// well-formed name, at least one target and one consumer, a kind that matches
// its header, and a secret of 1..MaxSecretBytes bytes.
func (e credentialEdit) valid(secretLen int) bool {
	if !validCredentialName(e.Name) || len(e.ExpectedRevision) > 128 {
		return false
	}
	switch e.Action {
	case "add":
		return e.ExpectedRevision == "" && validCredentialKind(e.Kind, e.Header) &&
			validCredentialScope(e.Targets, 1, 16) && validCredentialScope(e.Consumers, 1, 8) &&
			secretLen >= 1 && secretLen <= credentials.MaxSecretBytes
	case "rotate":
		return e.ExpectedRevision != "" && secretLen >= 1 && secretLen <= credentials.MaxSecretBytes
	case "revoke":
		return e.ExpectedRevision != ""
	}
	return false
}

// credentialExpiry converts an RFC 3339 expiry into the wire's
// rfc3339-micros grammar, the way serve/credentials.go does; empty stays empty.
func credentialExpiry(value string) (string, bool) {
	if value == "" {
		return "", true
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", false
	}
	return at.UTC().Format("2006-01-02T15:04:05.000000Z"), true
}

func zeroCredentialSecret(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// credentials lists, adds, rotates and revokes named secrets through the
// resolved holder (panelMachine().ResolveCredentials). The secret is typed
// once into the request and never appears in a reply: typed outcomes (stored,
// conflict, no_secure_store, ...) are returned unchanged and carry only
// metadata. Resolution and transport failures go through panelError. Allowing
// or denying a program to apply a named credential is a rights rule on
// resource credential:<name>, made through /rights like the rest of the
// Panel's policy edits; this handler does not duplicate that path.
func (p *servicePanel) credentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "GET or POST required", 405)
		return
	}
	var edit credentialEdit
	var secret []byte
	if r.Method == "POST" {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&edit); err != nil {
			http.Error(w, "invalid credential edit", 400)
			return
		}
		secret, edit.Secret = []byte(edit.Secret), ""
		if !edit.valid(len(secret)) {
			zeroCredentialSecret(secret)
			http.Error(w, "invalid credential edit", 400)
			return
		}
	} else if cursor := r.URL.Query().Get("cursor"); len(cursor) > 256 || !utf8.ValidString(cursor) {
		http.Error(w, "invalid credential cursor", 400)
		return
	}
	var expires string
	if edit.Action == "add" || edit.Action == "rotate" {
		var ok bool
		if expires, ok = credentialExpiry(edit.Expires); !ok {
			zeroCredentialSecret(secret)
			http.Error(w, "invalid credential expiry", 400)
			return
		}
	}
	defer zeroCredentialSecret(secret)
	ctx, cancel := panelCall(r)
	defer cancel()
	holder, err := panelMachine().ResolveCredentials(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	if edit.Action != "" {
		p.logAction(ctx, "credentials."+edit.Action, map[string]string{"credentials.name": edit.Name})
	}
	var result any
	switch edit.Action {
	case "":
		result, err = holder.List(ctx, r.URL.Query().Get("cursor"), 32)
	case "add":
		result, err = holder.Store(ctx, "", cwire.Registration{Name: edit.Name, Kind: edit.Kind, Header: edit.Header,
			Scope: cwire.Scope{Targets: edit.Targets, Consumers: edit.Consumers}, Expires: expires, Secret: secret})
	case "rotate":
		result, err = holder.Rotate(ctx, edit.ExpectedRevision, cwire.Rotation{Name: edit.Name, Expires: expires, Secret: secret})
	case "revoke":
		result, err = holder.Revoke(ctx, edit.ExpectedRevision, edit.Name)
	}
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, result)
}

// credentialsPageHandler serves the credentials page itself, keyed the way /
// is: the per-run key in the query string, not the header guard() expects
// from the page's own fetch calls.
func (p *servicePanel) credentialsPageHandler(key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/credentials-page" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("k") != key {
			http.Error(w, "panel key required", 403)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, credentialsPage)
	}
}
