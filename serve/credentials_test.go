package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testCredentialSecret = "hf_RUNTIME_TEST_SECRET_9c41d7a2_EXAMPLE"

// gatedOrigin serves body only to a request carrying the bearer secret and
// answers 401 otherwise, recording the Authorization header of every request.
type gatedOrigin struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newGatedOrigin(t *testing.T, body []byte) *gatedOrigin {
	t.Helper()
	o := &gatedOrigin{}
	o.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.seen = append(o.seen, r.Header.Get("Authorization"))
		o.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testCredentialSecret {
			http.Error(w, "credential required", http.StatusUnauthorized)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(o.Close)
	return o
}

func (o *gatedOrigin) authorizations() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.seen...)
}

// credentialsRuntime starts an isolated in-process runtime whose credentials
// live in a test-unique store namespace (Windows) or the file store of its
// temporary state directory (elsewhere), and removes every item it created.
func credentialsRuntime(t *testing.T) (runtimeFlags, string) {
	t.Helper()
	options, _ := isolatedRuntime(t)
	if err := os.MkdirAll(filepath.Join(options.stateDir, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.stateDir, "credentials", credentialsBackendFile), []byte("file-0600\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, namespace, err := credentialsEndpoints(options)
	if err != nil || !strings.HasPrefix(namespace, "oa-runtime-") {
		t.Fatalf("test namespace %q: %v", namespace, err)
	}
	t.Cleanup(func() { removeStoreItems(t, namespace) })
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("startup: %v", err)
	case <-time.After(runtimeWait):
		cancel()
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
	t.Cleanup(func() {
		cancel()
		if err := awaitStopped(t, done, "runtime"); err != nil {
			t.Error(err)
		}
	})
	return options, namespace
}

// Service accounts run machine-scope runtimes, which hold no secrets in this
// version; a secret is never accepted as an argument, and usage refusals send
// nothing.
func TestCredentialsCommandRefusalsSendNothing(t *testing.T) {
	for _, account := range []string{"S-1-5-18", "S-1-5-19", "S-1-5-20", "0"} {
		if !machineScope(account) {
			t.Fatalf("%s is not machine scope", account)
		}
	}
	if machineScope("S-1-5-21-1-2-3-1001") || machineScope("1000") {
		t.Fatal("a user account read as machine scope")
	}
	unreachable := `\\.\pipe\oa-credentials-nobody`
	if os.PathSeparator != '\\' {
		unreachable = filepath.Join(t.TempDir(), "nobody.sock")
	}
	for _, args := range [][]string{
		{"add", "hf", "--secret", testCredentialSecret, "--target", "huggingface.co", "--for", "abstraction.download/http-execution@1"},
		{"add", "hf", "--target", "huggingface.co", "--for", "abstraction.download/http-execution@1", testCredentialSecret},
		{"add", "bad name", "--target", "huggingface.co", "--for", "abstraction.download/http-execution@1", "--from-stdin"},
		{"add", "hf", "--from-stdin"},
		{"allow", "hf", "--program", "relative/program"},
		{"list", "extra"},
		{"unknown"},
	} {
		out, diag, err := runCredentials(t, testCredentialSecret, append(args, "--endpoint", unreachable)...)
		assertExit(t, err, exitUsage, strings.Join(args, " "))
		if strings.Contains(out+diag+err.Error(), testCredentialSecret) {
			t.Fatalf("%v echoed the secret", args)
		}
	}
}

// requireUserScopeAdd judges a credentials add on a runtime this test process
// owns. A root runtime is machine scope and holds no secrets: its add must read
// no_secure_store, and the test ends there because nothing was stored.
func requireUserScopeAdd(t *testing.T, err error, transcript string) {
	t.Helper()
	if runtime.GOOS != "windows" && machineScope(strconv.Itoa(os.Getuid())) {
		if err == nil || !strings.Contains(err.Error(), "no_secure_store") {
			t.Fatalf("credentials add on a machine-scope runtime: %v\n%s", err, transcript)
		}
		t.Skip("running as root: the machine-scope runtime refused the add with no_secure_store; the rest needs a user account")
	}
	if err != nil {
		t.Fatalf("credentials add: %v\n%s", err, transcript)
	}
}

func runCredentials(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	err := credentialsCommand(args, strings.NewReader(stdin), &out, &diagnostics)
	return out.String(), diagnostics.String(), err
}

// A download names a registered credential; the runtime applies it to the
// request it sends, and the origin that answers 401 without it serves the
// bytes. The secret enters once, on stdin, and appears in no reply, command
// output, job record or log.
func TestDownloadAppliesARegisteredCredential(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, namespace := credentialsRuntime(t)
	endpoint := options.endpoint
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe = filepath.Clean(exe)
	body := []byte("gated model bytes")
	origin := newGatedOrigin(t, body)
	var transcript strings.Builder
	record := func(out, diag string, err error) error {
		transcript.WriteString(out + diag)
		if err != nil {
			transcript.WriteString(err.Error())
		}
		return err
	}

	if err := record(runCredentials(t, "", "add", "hf", "--target", "127.0.0.1", "--for", "abstraction.download/http-execution@1", "--endpoint", endpoint, "--timeout", "30s")); err == nil {
		t.Fatal("add without --from-stdin and without a terminal read a secret")
	}
	err = record(runCredentials(t, testCredentialSecret+"\n", "add", "hf", "--target", "127.0.0.1", "--for", "abstraction.download/http-execution@1", "--from-stdin", "--use-by", exe, "--endpoint", endpoint, "--timeout", "30s"))
	requireUserScopeAdd(t, err, transcript.String())
	if items := storeItems(t, namespace+"/"); len(items) != 1 && os.PathSeparator == '\\' {
		t.Fatalf("Credential Manager items under the test namespace: %v", items)
	}
	listed, diag, err := runCredentials(t, "", "list", "--endpoint", endpoint, "--json", "--timeout", "30s")
	record(listed, diag, err)
	if err != nil {
		t.Fatalf("credentials list: %v", err)
	}
	var page struct {
		Limits  struct{ SecureStore string }
		Records []struct {
			Name  string
			State string
		}
	}
	if err := json.Unmarshal([]byte(listed), &page); err != nil || len(page.Records) != 1 || page.Records[0].Name != "hf" || page.Records[0].State != "active" {
		t.Fatalf("list: %s %v", listed, err)
	}

	// Anonymous, the gated origin refuses and the work fails permanently.
	sink := t.TempDir()
	var out, diagnostics bytes.Buffer
	exit := assertExit(t, downloadCommand([]string{origin.URL + "/anonymous.bin", "--endpoint", endpoint, "--out", sink, "--json", "--timeout", "90s"}, &out, &diagnostics), exitEnded, "anonymous download of a gated source")
	record(out.String(), diagnostics.String(), exit)
	if got := origin.authorizations(); len(got) == 0 || got[len(got)-1] != "" {
		t.Fatalf("anonymous request carried %v", got)
	}

	// Naming the credential, the runtime applies it and the bytes arrive.
	out.Reset()
	diagnostics.Reset()
	err = downloadCommand([]string{origin.URL + "/gated.bin", "--credential", "hf", "--endpoint", endpoint, "--out", sink, "--json", "--timeout", "90s"}, &out, &diagnostics)
	record(out.String(), diagnostics.String(), err)
	if err != nil {
		t.Fatalf("download --credential hf: %v\n%s", err, diagnostics.String())
	}
	got, err := os.ReadFile(filepath.Join(sink, "gated.bin"))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("delivered %q: %v", got, err)
	}
	if seen := origin.authorizations(); seen[len(seen)-1] != "Bearer "+testCredentialSecret {
		t.Fatalf("origin saw %v", seen)
	}

	// Denying this program's apply rule refuses the next such download at
	// admission: invalid, credential:not_permitted:hf, and no work [JOB-A16].
	if err := record(runCredentials(t, "", "allow", "hf", "--program", exe, "--deny", "--endpoint", endpoint, "--timeout", "30s")); err != nil {
		t.Fatalf("credentials allow --deny: %v", err)
	}
	before := len(origin.authorizations())
	var jobs bytes.Buffer
	if err := jobsCommand([]string{"list", "--endpoint", endpoint, "--json"}, &jobs, &diagnostics); err != nil {
		t.Fatalf("jobs list: %v", err)
	}
	jobCount := jobs.Len()
	out.Reset()
	diagnostics.Reset()
	exit = assertExit(t, downloadCommand([]string{origin.URL + "/denied.bin", "--credential", "hf", "--endpoint", endpoint, "--out", sink, "--timeout", "90s"}, &out, &diagnostics), exitRefusedCall, "download with a denied credential")
	record(out.String(), diagnostics.String(), exit)
	if exit == nil || !strings.Contains(exit.Error(), "credential:not_permitted:hf") {
		t.Fatalf("admission refusal lacks its reason: %v", exit)
	}
	if len(origin.authorizations()) != before {
		t.Fatal("a refused credential still reached the origin")
	}
	jobs.Reset()
	if err := jobsCommand([]string{"list", "--endpoint", endpoint, "--json"}, &jobs, &diagnostics); err != nil || jobs.Len() != jobCount {
		t.Fatalf("a refused admission left work: %v\n%s", err, jobs.String())
	}
	record(jobs.String(), "", nil)

	audit, diag, err := runCredentials(t, "", "audit", "--endpoint", endpoint, "--timeout", "30s")
	record(audit, diag, err)
	if err != nil || !strings.Contains(audit, "applied") || !strings.Contains(audit, "refused") || !strings.Contains(audit, "denied") {
		t.Fatalf("audit: %v\n%s", err, audit)
	}
	if err := record(runCredentials(t, "", "revoke", "hf", "--endpoint", endpoint, "--timeout", "30s")); err != nil {
		t.Fatalf("credentials revoke: %v", err)
	}
	if items := storeItems(t, namespace+"/"); len(items) != 0 {
		t.Fatalf("revoke left Credential Manager items: %v", items)
	}

	// The secret is in no command output and in no file the runtime wrote.
	if strings.Contains(transcript.String(), testCredentialSecret) {
		t.Fatal("a command transcript carries the secret")
	}
	err = filepath.WalkDir(filepath.Dir(options.stateDir), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.Contains(path, string(filepath.Separator)+"items"+string(filepath.Separator)) {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && bytes.Contains(data, []byte(testCredentialSecret)) {
			t.Errorf("%s carries the secret", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if log, err := os.ReadFile(options.out); err == nil && bytes.Contains(log, []byte(testCredentialSecret)) {
		t.Fatal("the runtime log carries the secret")
	}
}
