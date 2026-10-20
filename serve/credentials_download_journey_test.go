package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/fileid"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// The real labelled app is a separate native peer. Only this test's operator
// setup holds dummy values; the child receives a name and ordinary metadata.
func TestPrivateReleaseExampleThroughNativeRuntime(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("this receiver owns only Windows DPAPI-file and Linux file-0600 stores; Mac receiving remains separate")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("native transport cannot prove this example's Program")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	module, err := filepath.Abs("../examples/rights-playground")
	if err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(t.TempDir(), "private-release-download")
	if runtime.GOOS == "windows" {
		app += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "-C", module, "build", "-o", app, "./cmd/private-release-download")
	// Use current AppModel sources in the private workspace and the example's
	// declared module dependencies in the standalone charter checkout.
	workspace := filepath.Clean(filepath.Join(module, "..", "..", "go.work"))
	switch _, err := os.Stat(workspace); {
	case err == nil:
		build.Env = append(os.Environ(), "GOWORK="+workspace)
	case os.IsNotExist(err):
		build.Env = append(os.Environ(), "GOWORK=off")
	default:
		t.Fatal(err)
	}
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual example: %v\n%s", err, out)
	}
	options, _ := isolatedRuntime(t)
	stop := startInferenceRuntime(t, options)
	defer stop()
	runtimeProgram, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runtimeProgram = filepath.Clean(runtimeProgram)
	first, second := "PK1_NATIVE_DUMMY_FIRST_4d1b", "PK1_NATIVE_DUMMY_SECOND_9a2c"
	var transcript bytes.Buffer
	checkSecret := func(data []byte) {
		t.Helper()
		if bytes.Contains(data, []byte(first)) || bytes.Contains(data, []byte(second)) {
			t.Fatal("dummy credential disclosed outside its owned secret store")
		}
	}
	operator := func(value string, args ...string) string {
		t.Helper()
		out, diag, err := runCredentials(t, value, args...)
		checkSecret([]byte(out + diag))
		transcript.WriteString(out + diag)
		if err != nil {
			checkSecret([]byte(err.Error()))
			t.Fatalf("fixture operator: %v", err)
		}
		return out
	}
	operator(first+"\n", "add", "private-release", "--target", "127.0.0.1", "--for", "abstraction.download/http-execution@1", "--from-stdin", "--endpoint", options.endpoint, "--timeout", "30s")
	// The example explicitly requests credentials@2. Grant its typed named Use
	// through the actual native operator; the legacy --use-by grant is separate.
	machine := client.NewVerified(options.endpoint, runtimeExpectation(t))
	policy, err := machine.ResolveRightsSelectorOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	target, err := credentials.PrepareCredentialTarget("private-release")
	if err != nil {
		t.Fatal(err)
	}
	selection := rwire.PermissionSelection{Permission: rwire.PermissionKey{Action: credentials.ActionApply, ResourceProfile: credentials.ScopedUseResourceProfile}, Selector: rwire.ResourceSelector{Kind: rwire.ResourceSelectorKindOne, Target: target}}
	subject := rwire.Subject{Account: probeSelf().Account, Program: app}
	current, err := policy.ReadSelectorRuleContext(ctx, subject, selection)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := policy.SetSelectorRuleContext(ctx, current.Revision, rwire.SelectorRule{Subject: subject, Selection: selection, Permit: true}, 0, "P-K1 owned fixture")
	if err != nil || permit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("typed native Use grant: %v %v", permit.Outcome, err)
	}
	listed := operator("", "list", "--endpoint", options.endpoint, "--json")
	var page struct{ Limits struct{ SecureStore string } }
	if err := json.Unmarshal([]byte(listed), &page); err != nil {
		t.Fatal(err)
	}
	wantStore := "file-0600"
	if runtime.GOOS == "windows" {
		wantStore = "windows-dpapi-file"
	}
	if page.Limits.SecureStore != wantStore {
		t.Fatalf("owned file store: got %q want %q", page.Limits.SecureStore, wantStore)
	}
	items := filepath.Join(options.stateDir, "credentials", "items")
	entries, err := os.ReadDir(items)
	if err != nil || len(entries) != 1 {
		t.Fatalf("one owned credential file required: %v", err)
	}
	var plainItems fileid.FileID
	if wantStore == "file-0600" {
		info, err := os.Stat(filepath.Join(items, entries[0].Name()))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("Linux credential file must be mode0600")
		}
		plainItems, err = fileid.Resolve(ctx, fileid.Self, items, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			t.Fatal(err)
		}
	}
	body := []byte("private native release fixture\n")
	sum := sha256.Sum256(body)
	var mu sync.Mutex
	expected, protected, foreign := first, 0, 0
	var foreignURL string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(req.Host, "localhost:") {
			foreign++
			if req.Header.Get("Authorization") != "" {
				t.Error("foreign redirect received credential")
			}
		} else {
			protected++
			if req.Header.Get("Authorization") != "Bearer "+expected {
				http.Error(w, "fixture credential required", http.StatusUnauthorized)
				return
			}
			if req.URL.Path == "/redirect" {
				http.Redirect(w, req, foreignURL+"/asset", http.StatusTemporaryRedirect)
				return
			}
		}
		http.ServeContent(w, req, "asset", time.Time{}, bytes.NewReader(body))
	}))
	defer origin.Close()
	foreignURL = strings.Replace(origin.URL, "127.0.0.1", "localhost", 1)
	// Explicit finite child environment: no inherited credential-bearing keys.
	env := []string{}
	for _, key := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	checkSecret([]byte(strings.Join(env, "\n")))
	child := func(args ...string) ([]byte, error) {
		t.Helper()
		checkSecret([]byte(strings.Join(args, "\n")))
		command := exec.CommandContext(ctx, app, args...)
		command.Env, command.Dir = env, filepath.Dir(app)
		out, err := command.CombinedOutput()
		checkSecret(out)
		transcript.Write(out)
		if command.ProcessState == nil || !command.ProcessState.Exited() {
			t.Fatal("example child was not reaped")
		}
		return out, err
	}
	base := []string{"--endpoint", options.endpoint, "--runtime-program", runtimeProgram}
	history, err := child(append(append([]string{}, base...), "--history")...)
	if err != nil || len(bytes.TrimSpace(history)) == 0 {
		t.Fatalf("native history: %v\n%s", err, history)
	}
	epoch := strings.TrimSpace(string(history))
	perform := func(key, path string, success bool) {
		t.Helper()
		output := filepath.Join(t.TempDir(), "asset.bin")
		args := append(append([]string{}, base...), "--credential", "private-release", "--url", origin.URL+path, "--bytes", fmt.Sprint(len(body)), "--sha256", hex.EncodeToString(sum[:]), "--out", output, "--epoch", epoch, "--key", key)
		out, err := child(args...)
		if success {
			if err != nil {
				t.Fatalf("native example %s: %v\n%s", key, err, out)
			}
			got, err := os.ReadFile(output)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatal("native example did not save verified bytes")
			}
		} else {
			if err == nil || !bytes.Contains(out, []byte("download admission: invalid")) {
				t.Fatalf("revoked native example lacked typed admission refusal: %v\n%s", err, out)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("refused child wrote output")
			}
		}
	}
	perform("first", "/asset", true)
	operator(second+"\n", "rotate", "private-release", "--from-stdin", "--endpoint", options.endpoint)
	mu.Lock()
	expected = second
	mu.Unlock()
	perform("replacement", "/asset", true)
	perform("redirect", "/redirect", true)
	mu.Lock()
	before, redirected := protected, foreign
	mu.Unlock()
	if redirected == 0 {
		t.Fatal("foreign-host redirect control did not run")
	}
	operator("", "revoke", "private-release", "--endpoint", options.endpoint)
	perform("revoked", "/asset", false)
	mu.Lock()
	after := protected
	mu.Unlock()
	if after != before {
		t.Fatal("revoked example reached protected HTTP source")
	}
	audit := operator("", "audit", "--endpoint", options.endpoint, "--json")
	if !strings.Contains(audit, "revoked") || !strings.Contains(audit, "applied") {
		t.Fatal("native service audit lacks application/revocation evidence")
	}
	stop() // flush and close runtime-owned records before inspecting them.
	checkSecret(transcript.Bytes())
	for _, root := range []string{options.stateDir, options.out} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// Linux's explicitly selected secret-store item is the one intended
			// plaintext destination. It is not an app/log/job/audit record.
			if entry.IsDir() {
				if !plainItems.IsZero() {
					current, err := fileid.Resolve(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
					if err != nil {
						return err
					}
					if current.Equal(plainItems) {
						return filepath.SkipDir
					}
				}
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			checkSecret(data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Log("actual example children reaped; native file-store apply/replace/redirect/revoke and output/argv/env/record non-disclosure controls passed")
}
