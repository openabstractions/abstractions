package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// This opt-in adopter proof is user story S2 (research/adoption/user-stories-2026-09-22.md):
// a hosted credential registered and granted through the Panel, used by
// OpenCode with the value never visible to it, then revoked through the Panel
// so the next call stops before provider I/O. Every credential and rights edit
// goes through the running Abstraction Panel's own HTTP handlers
// (monitor/credentials_panel.go, monitor/rights_panel.go), not the CLI: the
// Panel is the real "Abstraction Panel.exe" binary, run as a separate process
// against the isolated runtime, driven with a plain net/http client the way a
// browser page open on the Panel's URL would drive it.
func TestOpenCodeCredentialJourneyThroughPanel(t *testing.T) {
	opencode, addon, library := os.Getenv("OA_OPENCODE_BINARY"), os.Getenv("OA_IPC_NODE"), os.Getenv("OA_IPC_LIBRARY")
	if opencode == "" || addon == "" || library == "" {
		t.Skip("set OA_OPENCODE_BINARY, OA_IPC_NODE, and OA_IPC_LIBRARY for the Panel-driven OpenCode credential proof")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	for _, path := range []string{opencode, addon, library} {
		if !filepath.IsAbs(path) {
			t.Fatalf("fixture path must be absolute: %s", path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	opencode = filepath.Clean(opencode)
	const credName = "openrouter"

	upstream := newHostedUpstream(t)
	options, _ := isolatedRuntime(t)
	hosts := fmt.Sprintf(`{"local":[],"hosted":[{"name":%q,"base":%q,"wire":"openai-compatible","credential":%q}],"ceilings":{%q:{"tokens_per_day":100000}}}`,
		credName, upstream.URL+"/api/v1", credName, credName)
	if err := os.MkdirAll(filepath.Join(options.stateDir, "inference"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.stateDir, "inference", inferenceHostsFile), []byte(hosts), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, namespace, err := credentialsEndpoints(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeStoreItems(t, namespace) })

	runtimeProgram, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runtimeProgram = filepath.Clean(runtimeProgram)

	// The Panel must exist beside the runtime's own executable, under the
	// exact sibling name serve/runtime_credentials.go's operatorSiblings
	// looks for, before the runtime starts: composeRights reads
	// operatorPrograms() once at startup (serve/runtime_rights.go
	// composeRights), so a Panel built or placed afterward would never be
	// recognized as an operator program.
	panelExe := buildAbstractionPanelBeside(t, runtimeProgram)
	startInferenceRuntime(t, options)

	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}

	panelAddr := freeLoopbackPort(t)
	var panelStdout, panelStderr safeBuffer
	panelCmd := exec.Command(panelExe, "-addr", panelAddr, "-open=false",
		"-runtime-endpoint", options.endpoint, "-runtime-program", runtimeProgram)
	stdoutPipe, err := panelCmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	panelCmd.Stderr = &panelStderr
	if err := panelCmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if panelCmd.Process != nil {
			_ = panelCmd.Process.Kill()
		}
		_ = panelCmd.Wait()
	})
	reader := bufio.NewReader(stdoutPipe)
	startupLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("Panel did not print its startup line: %v\nstderr:\n%s", err, panelStderr.String())
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				panelStdout.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	const startupPrefix = "service control panel: "
	startupLine = strings.TrimSpace(startupLine)
	if !strings.HasPrefix(startupLine, startupPrefix) {
		t.Fatalf("unexpected Panel startup line: %q", startupLine)
	}
	parsedURL, err := url.Parse(strings.TrimPrefix(startupLine, startupPrefix))
	if err != nil {
		t.Fatal(err)
	}
	panelKey := parsedURL.Query().Get("k")
	panelBase := parsedURL.Scheme + "://" + parsedURL.Host
	if panelKey == "" || panelBase == "http://" {
		t.Fatalf("Panel printed no usable URL: %q", startupLine)
	}

	httpClient := &http.Client{Timeout: 20 * time.Second}
	var transcript strings.Builder
	doPanel := func(method, path string, body any) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(raw)
		}
		req, err := http.NewRequest(method, panelBase+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Panel-Key", panelKey)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		transcript.WriteString(fmt.Sprintf("%s %s -> %d %s\n", method, path, resp.StatusCode, data))
		if bytes.Contains(data, []byte(testCredentialSecret)) {
			t.Fatalf("Panel reply to %s %s carried the secret", method, path)
		}
		return resp.StatusCode, data
	}

	// 1. List credentials (empty). Mirrors clicking "List credentials".
	status, body := doPanel(http.MethodGet, "/credentials?cursor=", nil)
	empty := decodePanelReply[cwire.MetadataPage](t, status, body)
	if empty.Outcome != cwire.PageOutcomePage || len(empty.Records) != 0 {
		t.Fatalf("initial credentials list: %+v", empty)
	}

	// 2. Add the credential. The secret travels once, in this POST body, on
	// the Panel's own loopback connection; it is never a command-line
	// argument and never echoed back.
	status, body = doPanel(http.MethodPost, "/credentials", panelCredentialEdit{
		Action:  "add",
		Name:    credName,
		Kind:    "bearer",
		Targets: []string{router.Target(upstream.URL)},
		Consumers: []string{
			inference.Contract,
			router.CredentialConsumer,
		},
		Secret:           testCredentialSecret,
		ExpectedRevision: "",
	})
	added := decodePanelReply[cwire.StoreResult](t, status, body)
	if added.Outcome != cwire.StoreOutcomeStored {
		t.Fatalf("add %s: %+v", credName, added)
	}

	// 3. List again to read the stored revision the way the page's Rotate and
	// Revoke buttons do ("Use for revoke").
	status, body = doPanel(http.MethodGet, "/credentials?cursor=", nil)
	afterAdd := decodePanelReply[cwire.MetadataPage](t, status, body)
	var credRevision string
	for _, record := range afterAdd.Records {
		if record.Name == credName {
			credRevision = record.Revision
		}
	}
	if credRevision == "" {
		t.Fatalf("credential %s missing after add: %+v", credName, afterAdd)
	}

	// 4. Allow the exact OpenCode executable through the Panel's rights page.
	// Two rules gate a hosted completion, both exact-program rights edits
	// through the same /rights endpoint the credentials page's "Allow or deny
	// a program" form uses: whether the program may complete inference on
	// this host at all (inference.ActionComplete on inference.ResourceHost,
	// serve/runtime_inference.go cfg.Decide; unset, it raises the same
	// rights.first_use question G2 describes), and whether it may apply this
	// named credential (credentials.ActionApply on credential:<name>, the
	// credentials page's own grant). This is the S2 grant: OpenCode's own
	// program identity, nothing broader.
	grantRule := func(program, action, resource string) {
		t.Helper()
		status, body := doPanel(http.MethodGet, "/rights?cursor=", nil)
		page := decodePanelReply[rwire.PolicyPage](t, status, body)
		if page.Outcome != rwire.PolicyPageOutcomePage {
			t.Fatalf("list rights policy: %+v", page)
		}
		status, body = doPanel(http.MethodPost, "/rights", panelRightsEdit{
			Edit: "set", Revision: page.Revision, Account: account.Uid, Program: program,
			Action: action, Resource: resource, Permit: true,
		})
		edit := decodePanelReply[rwire.PolicyEdit](t, status, body)
		if edit.Outcome != rwire.PolicyEditOutcomeApplied {
			t.Fatalf("allow %s %s on %s: %+v", program, action, resource, edit)
		}
	}
	grantRule(opencode, inference.ActionComplete, inference.ResourceHost(credName))
	grantRule(opencode, credentials.ActionApply, credentials.ResourceFor(credName))
	// The router reads a hosted host's own listing as the runtime's own
	// program (openabstractions-flat/abstraction-router/go/router.go
	// readHosted, serve/runtime_inference.go composeInference UseCredentials),
	// which happens once per survey age on the first chat request. Without
	// this grant the hosted host never comes up and OpenCode's completion
	// fails before it reaches the credential question S2 is about; this is
	// the runtime's own installed program, not a person's application, and
	// its grant is not part of the S2 story a user acts on.
	grantRule(runtimeProgram, credentials.ActionApply, credentials.ResourceFor(credName))

	// 5. Run OpenCode once: the hosted route succeeds, OpenCode never sees the
	// secret, and the controlled upstream sees it applied.
	providerIndex := stageOpenCodeProvider(t)
	project := t.TempDir()
	config := map[string]any{
		"provider": map[string]any{"openabstractions": map[string]any{
			"name": "OpenAbstractions", "npm": fileURL(providerIndex),
			"options": map[string]any{"runtimeEndpoint": options.endpoint, "scope": "remote",
				"guarantees": []string{inference.GuaranteeHosted}, "credential": credName},
			"models": map[string]any{"anthropic/claude-sonnet-5": map[string]any{"name": "Controlled hosted fixture",
				"limit": map[string]any{"context": 32768, "output": 4096}}},
		}},
		"model": "openabstractions/anthropic/claude-sonnet-5",
	}
	rawConfig, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawConfig, []byte(testCredentialSecret)) {
		t.Fatal("OpenCode configuration contains the credential value")
	}
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), rawConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	env := isolatedOpenCodeEnvironment(t, t.TempDir(), addon, library)
	run := func() []byte {
		t.Helper()
		cmd := exec.Command(opencode, "--pure", "run", "--auto", "--print-logs", "--log-level", "DEBUG",
			"--model", "openabstractions/anthropic/claude-sonnet-5", "--format", "json", "--dir", project,
			"Reply with the controlled provider response.")
		cmd.Dir, cmd.Env = project, env
		out, _ := cmd.CombinedOutput()
		if bytes.Contains(out, []byte(testCredentialSecret)) {
			t.Fatal("OpenCode output contains the credential value")
		}
		return out
	}

	completed := run()
	if !bytes.Contains(completed, []byte("Hello!")) {
		t.Fatalf("OpenCode hosted credential request through the Panel-granted rights: %s", completed)
	}
	beforeRevoke := upstream.authorizations()
	if len(beforeRevoke) == 0 {
		t.Fatal("OpenCode made no request to the controlled upstream")
	}
	for _, header := range beforeRevoke {
		if header != "Bearer "+testCredentialSecret {
			t.Fatalf("controlled upstream received %d unexpected Authorization headers", len(beforeRevoke))
		}
	}

	// 6. Revoke through the Panel, at the revision the list showed.
	status, body = doPanel(http.MethodPost, "/credentials", panelCredentialEdit{
		Action: "revoke", Name: credName, ExpectedRevision: credRevision,
	})
	revoked := decodePanelReply[cwire.RevokeResult](t, status, body)
	if revoked.Outcome != cwire.RevokeOutcomeRevoked {
		t.Fatalf("revoke %s: %+v", credName, revoked)
	}

	// 7. Run OpenCode again: the typed refusal happens before provider I/O.
	refused := run()
	if !bytes.Contains(refused, []byte("credential:revoked:"+credName)) && !bytes.Contains(refused, []byte("router:not-here")) {
		t.Fatalf("OpenCode output lacks the post-revocation refusal:\n%s", refused)
	}
	if got := upstream.authorizations(); len(got) != len(beforeRevoke) {
		t.Fatalf("revoked request changed controlled upstream call count from %d to %d", len(beforeRevoke), len(got))
	}

	// 8. Final list, confirming the revoked state the way "List credentials"
	// would show it.
	status, body = doPanel(http.MethodGet, "/credentials?cursor=", nil)
	final := decodePanelReply[cwire.MetadataPage](t, status, body)
	revokedSeen := false
	for _, record := range final.Records {
		if record.Name == credName {
			revokedSeen = record.State == cwire.StateRevoked
		}
	}
	if !revokedSeen {
		t.Fatalf("final credentials list does not show %s revoked: %+v", credName, final)
	}

	// Audit and log evidence: attribution to the exact OpenCode executable,
	// and the secret absent everywhere it could have leaked.
	credentialAudit, diagnostics, err := runCredentials(t, "", "audit", "--endpoint", options.endpoint, "--json", "--timeout", "30s")
	if err != nil {
		t.Fatalf("credentials audit: %v\n%s%s", err, credentialAudit, diagnostics)
	}
	if strings.Contains(credentialAudit+diagnostics, testCredentialSecret) {
		t.Fatal("credential audit contains the credential value")
	}
	var credentialRecords struct {
		Entries []struct {
			Event, Outcome string
			Subject        struct{ Program string }
		}
	}
	if json.Unmarshal([]byte(credentialAudit), &credentialRecords) != nil {
		t.Fatalf("credential audit did not decode: %s", credentialAudit)
	}
	var applied, refusedAudit bool
	for _, entry := range credentialRecords.Entries {
		if samePrograms(entry.Subject.Program, opencode) {
			applied = applied || entry.Event == "applied" && entry.Outcome == "applied"
			refusedAudit = refusedAudit || entry.Event == "refused" && entry.Outcome == "revoked"
		}
	}
	if !applied || !refusedAudit {
		t.Fatalf("credential audit lacks the OpenCode apply and revoked refusal:\n%s", credentialAudit)
	}

	inferenceAudit, err := runInference(t, "audit", "--endpoint", options.endpoint, "--json", "--timeout", "30s")
	if err != nil {
		t.Fatalf("inference audit: %v\n%s", err, inferenceAudit)
	}
	if strings.Contains(inferenceAudit, testCredentialSecret) {
		t.Fatal("inference audit contains the credential value")
	}
	var inferenceRecords struct {
		Entries []struct{ Program, Outcome string }
	}
	if json.Unmarshal([]byte(inferenceAudit), &inferenceRecords) != nil {
		t.Fatalf("inference audit did not decode: %s", inferenceAudit)
	}
	completedByOpenCode := false
	for _, entry := range inferenceRecords.Entries {
		completedByOpenCode = completedByOpenCode || samePrograms(entry.Program, opencode) && entry.Outcome == "completed"
	}
	if !completedByOpenCode {
		t.Fatalf("inference audit lacks the attributed OpenCode completion:\n%s", inferenceAudit)
	}

	if strings.Contains(transcript.String(), testCredentialSecret) {
		t.Fatal("the Panel HTTP transcript contains the credential value")
	}
	if strings.Contains(panelStdout.String()+panelStderr.String(), testCredentialSecret) {
		t.Fatal("the Panel process's own output contains the credential value")
	}
	if log, err := os.ReadFile(options.out); err == nil && bytes.Contains(log, []byte(testCredentialSecret)) {
		t.Fatal("runtime log contains the credential value")
	}
	if err := filepath.WalkDir(options.stateDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || strings.Contains(path, string(filepath.Separator)+"items"+string(filepath.Separator)) {
			return walkErr
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Contains(data, []byte(testCredentialSecret)) {
			t.Errorf("%s contains the credential value", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// panelCredentialEdit mirrors monitor/credentials_panel.go's credentialEdit
// wire shape exactly (monitor is a separate Go module the serve tests cannot
// import); its JSON field names are the Panel's actual request contract.
type panelCredentialEdit struct {
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

// panelRightsEdit mirrors monitor/rights_panel.go's rightsEdit wire shape.
type panelRightsEdit struct {
	Edit     string `json:"edit"`
	Revision string `json:"revision"`
	Account  string `json:"account"`
	Program  string `json:"program"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Permit   bool   `json:"permit"`
}

func decodePanelReply[T any](t *testing.T, status int, body []byte) T {
	t.Helper()
	var value T
	if status != http.StatusOK {
		t.Fatalf("Panel call failed: %d %s", status, body)
	}
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode Panel reply: %v\n%s", err, body)
	}
	return value
}

// safeBuffer is a bytes.Buffer an exec.Cmd may write to from its own
// goroutine while the test reads it.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// buildAbstractionPanelBeside compiles the monitor module into the exact
// sibling name serve/runtime_credentials.go's operatorSiblings looks for
// beside besideExe, so the runtime recognizes it as an operator program by
// installation (composeRights -> operatorPrograms, read once at startup).
// besideExe must be the path the runtime itself will run as; the caller must
// build this before starting that runtime.
func buildAbstractionPanelBeside(t *testing.T, besideExe string) string {
	t.Helper()
	charterRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	name := "Abstraction Panel"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dest := filepath.Join(filepath.Dir(besideExe), name)
	cmd := exec.Command("go", "build", "-o", dest, ".")
	cmd.Dir = filepath.Join(charterRoot, "monitor")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build Abstraction Panel: %v\n%s", err, out)
	}
	t.Cleanup(func() { os.Remove(dest) })
	return dest
}
