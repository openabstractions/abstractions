// rights-playground is an application-owned chooser for real isolated native
// permission calls. Each A/B executable makes its own public service calls.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

const typedCredentialGuarantee = "abstraction.download/credentials@2"

const usage = `Usage: rights-playground <command> [options]

  window --connection FILE     native Windows chooser for alpha/beta and Use/Read
  menu --connection FILE       terminal chooser for development checks
  use alpha|beta --connection FILE
                              real typed Use; requires credentials@2
  read alpha|beta --connection FILE
                              raw Read, when the reader API is served
  identity                    this executable's native account and program
  source --ready-file FILE    start the bounded dummy loopback receiver
  stats --connection FILE     dummy receiver counts, no headers or values
  setup --connection FILE     operator-only two-name setup and job prerequisite
  reset-secrets --connection FILE
                              operator-only conditional deletion of dummy values
  lifecycle Start|Stop|Reset  macOS pack lifecycle, with isolated native XPC
  legacy-use alpha|beta --connection FILE
                              explicit legacy exact-Use development check

Only oa-playground-alpha and oa-playground-beta are accepted. A completed or
definitely refused Use starts a fresh job next time. Uncertain admission keeps
its original identity for reconciliation. A/B are distinct physical
executables and call the runtime themselves. The existing native OA presenter
handles permission consent; this app owns ordinary operation selection.
This example uses dummy data in an explicitly selected isolated runtime.
No command resolves an installed runtime implicitly. Secret values never enter
command arguments, progress output, receiver statistics or evidence.
`

// connection is a local packaging record, never an assertion of caller identity.
// Services derive the caller from the native transport.
type connection struct {
	Version        int    `json:"version"`
	Isolated       string `json:"isolated"`
	Endpoint       string `json:"endpoint"`
	RuntimeProgram string `json:"runtime_program"`
	StateDir       string `json:"state_dir"`
	AppA           string `json:"app_a"`
	AppB           string `json:"app_b"`
	Origin         string `json:"origin"`
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if len(os.Args) > 1 && os.Args[1] == "window" {
			reportWindowError()
		}
		os.Exit(1)
	}
}

func run(args []string, input io.Reader, out io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		_, err := io.WriteString(out, usage)
		return err
	}
	if args[0] == "identity" {
		if len(args) != 1 {
			return errors.New("identity takes no arguments")
		}
		return showIdentity(out)
	}
	if args[0] == "lifecycle" {
		return runLifecycle(args[1:], out)
	}
	if args[0] == "source" {
		flags := flag.NewFlagSet("source", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		ready := flags.String("ready-file", "", "absolute local ready file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || !filepath.IsAbs(*ready) {
			return errors.New("source requires --ready-file with an absolute local path")
		}
		return serveSource(*ready, out)
	}
	command := args[0]
	if command != "window" && command != "menu" && command != "use" && command != "legacy-use" && command != "read" && command != "setup" && command != "reset-secrets" && command != "stats" {
		return fmt.Errorf("unknown command %q; use --help", command)
	}
	// Accept a fixture label before flags, matching ordinary CLI invocation.
	label := ""
	remaining := args[1:]
	if command == "use" || command == "legacy-use" || command == "read" {
		if len(remaining) == 0 {
			return errors.New("select alpha or beta")
		}
		label, remaining = remaining[0], remaining[1:]
		if _, err := fixtureFor(label); err != nil {
			return err
		}
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("connection", "", "isolated local connection record")
	if err := flags.Parse(remaining); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" {
		return errors.New("pass --connection FILE; use --help")
	}
	c, err := loadConnection(*path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	switch command {
	case "window":
		return appWindow(c)
	case "menu":
		return menu(c, input, out)
	case "use", "legacy-use":
		return use(ctx, c, label, command == "legacy-use", out)
	case "read":
		return readSecret(ctx, c, label, out)
	case "setup":
		return setup(ctx, c, out)
	case "reset-secrets":
		return resetSecrets(ctx, c, out)
	case "stats":
		return sourceStats(ctx, c.Origin, out)
	}
	return nil
}

func loadConnection(path string) (connection, error) {
	var c connection
	file, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid connection record: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return c, errors.New("connection record has trailing data")
	}
	if c.Version != 1 || !validIsolatedName(c.Isolated) || c.Endpoint == "" || !filepath.IsAbs(c.RuntimeProgram) || !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.AppA) || !filepath.IsAbs(c.AppB) {
		return c, errors.New("connection must name an isolated runtime and absolute local paths")
	}
	expectedEndpoint, err := playgroundEndpoint(c.Isolated, runtime.GOOS)
	if err != nil || c.Endpoint != expectedEndpoint {
		return c, errors.New("connection endpoint must match its isolated runtime name")
	}
	absoluteRecord, err := filepath.Abs(path)
	if err != nil || !identity.SamePathName(filepath.Clean(c.StateDir), filepath.Join(filepath.Dir(absoluteRecord), "state")) {
		return c, errors.New("connection state must be this run's state folder")
	}
	for _, p := range []string{c.RuntimeProgram, c.StateDir, c.AppA, c.AppB} {
		if err := identity.LocalPath(p); err != nil {
			return c, fmt.Errorf("connection path must be local: %w", err)
		}
	}
	if identity.SamePathName(identity.CanonicalProgramPath(c.AppA), identity.CanonicalProgramPath(c.AppB)) {
		return c, errors.New("A and B must name distinct executables")
	}
	if same, err := identity.SameFile(c.AppA, c.AppB); err != nil || same {
		return c, errors.New("A and B must be two existing physical files")
	}
	if _, err := loopbackOrigin(c.Origin); err != nil {
		return c, err
	}
	return c, nil
}

func playgroundEndpoint(name, platform string) (string, error) {
	if platform == "darwin" {
		return bootstrap.IsolatedXPCEndpoint(name, "runtime-v1")
	}
	return bootstrap.Endpoint(name + "-runtime")
}

func validIsolatedName(s string) bool {
	if !strings.HasPrefix(s, "rights-playground-") || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func loopbackOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("fixture origin must be an explicit http://127.0.0.1:PORT")
	}
	return u, nil
}

func selectedMachine(c connection) (*facade.Machine, error) {
	who, err := identity.Self()
	if err != nil {
		return nil, err
	}
	return client.NewVerified(c.Endpoint, listen.ServerExpectation{Principal: who, Program: identity.CanonicalProgramPath(c.RuntimeProgram)}), nil
}

func showIdentity(out io.Writer) error {
	who, err := identity.Self()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	account := who.SID
	if who.Kind == "posix" {
		account = strconv.Itoa(who.UID)
	}
	return json.NewEncoder(out).Encode(map[string]any{"account": account, "program": identity.CanonicalProgramPath(exe), "pid": os.Getpid(), "source": "this process; service derives peer independently"})
}

func menu(c connection, input io.Reader, out io.Writer) error {
	if err := registerMenu(c); err != nil {
		return err
	}
	if err := showIdentity(out); err != nil {
		return err
	}
	fmt.Fprintln(out, "Dummy secret playground. The runtime continues when this app closes.")
	fmt.Fprintln(out, "Type: use alpha | use beta | read alpha | read beta | stats | quit")
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 128), 512)
	for {
		fmt.Fprint(out, "> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		words := strings.Fields(strings.ToLower(scanner.Text()))
		if len(words) == 1 && (words[0] == "quit" || words[0] == "q") {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		var err error
		switch {
		case len(words) == 1 && words[0] == "stats":
			err = sourceStats(ctx, c.Origin, out)
		case len(words) == 2 && words[0] == "use":
			err = use(ctx, c, words[1], false, out)
		case len(words) == 2 && words[0] == "read":
			err = readSecret(ctx, c, words[1], out)
		default:
			err = errors.New("type use/read alpha/beta, stats or quit")
		}
		cancel()
		if err != nil {
			fmt.Fprintln(out, err)
			if errors.Is(err, errAcceptanceUncertain) {
				fmt.Fprintln(out, "This operation may still run. Repeat the same Use to reconcile its retained identity.")
			}
		}
	}
}

type jobClient interface {
	GetHistoryWindow(context.Context) (api.HistoryWindow, error)
	Submit(context.Context, api.Submission) (api.AcceptanceResult, error)
	Reconcile(context.Context, api.RequestIdentity) (api.AcceptanceResult, error)
	ObserveWork(context.Context, api.RequestIdentity) (api.ObservationResult, error)
	CopyResult(context.Context, api.RequestIdentity, io.Writer) (int64, error)
}

func use(ctx context.Context, c connection, label string, legacy bool, out io.Writer) error {
	f, err := fixtureFor(label)
	if err != nil {
		return err
	}
	program, err := actualAppProgram(c)
	if err != nil {
		return err
	}
	m, err := selectedMachine(c)
	if err != nil {
		return err
	}
	required := useGuarantees(legacy)
	jobs, err := m.ResolveJobs(ctx, facade.Requirements{Scope: facade.ScopeLocal, Guarantees: required})
	if err != nil {
		return fmt.Errorf("Use unavailable: required credential route is not resolved: %w", err)
	}
	subjectKey := sha256.Sum256([]byte(program))
	mode := "typed"
	if legacy {
		mode = "legacy"
	}
	intentPath := filepath.Join(filepath.Dir(c.StateDir), "intents", hex.EncodeToString(subjectKey[:16])+"-"+mode+"-"+f.label+".json")
	return submitUse(ctx, jobs, c.Origin, f, required, intentPath, out)
}

func useGuarantees(legacy bool) []string {
	required := append([]string(nil), api.AdmissionGuarantees...)
	if legacy {
		return append(required, request.CredentialGuarantees...)
	}
	return append(required, request.ScopedCredentialGuarantees...)
}

func actualAppProgram(c connection) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	program := identity.CanonicalProgramPath(exe)
	if !identity.SamePathName(program, identity.CanonicalProgramPath(c.AppA)) && !identity.SamePathName(program, identity.CanonicalProgramPath(c.AppB)) {
		return "", errors.New("Use/Read must run in this connection's actual App A or App B executable")
	}
	return program, nil
}

func submitUse(ctx context.Context, jobs jobClient, origin string, f fixture, guarantees []string, intentPath string, out io.Writer) error {
	intent, err := prepareIntent(ctx, jobs, origin, f, guarantees, intentPath)
	if err != nil {
		return err
	}
	id := intent.Submission.Identity
	var accepted api.AcceptanceResult
	directSubmitReply := false
	if intent.Existing {
		accepted, err = jobs.Reconcile(ctx, id)
	} else {
		accepted, err = jobs.Submit(ctx, intent.Submission)
		directSubmitReply = err == nil
		if err != nil {
			accepted, err = jobs.Reconcile(ctx, id)
		}
	}
	if err != nil || accepted.Outcome == api.AcceptanceOutcomeUnknown || accepted.Outcome == api.AcceptanceOutcomeUnavailable {
		return uncertain(id)
	}
	if accepted.Outcome != api.AcceptanceOutcomeAccepted || accepted.Receipt == nil {
		if accepted.Outcome == api.AcceptanceOutcomeDefinitelyNotAccepted || (directSubmitReply && (accepted.Outcome == api.AcceptanceOutcomeForbidden || accepted.Outcome == api.AcceptanceOutcomeInvalid)) {
			if err := clearIntent(intentPath); err != nil {
				return err
			}
			return &useRefusalError{Outcome: accepted.Outcome, Reason: accepted.Reason}
		}
		return uncertain(id)
	}
	fmt.Fprintf(out, "Use %s: operation %s\n", f.label, accepted.Receipt.OperationID)
	for {
		observed, err := jobs.ObserveWork(ctx, id)
		if err != nil {
			return uncertain(id)
		}
		if observed.Snapshot == nil {
			return uncertain(id)
		}
		switch observed.Snapshot.State {
		case api.WorkStateComplete:
			var result bytes.Buffer
			written, err := jobs.CopyResult(ctx, id, &boundedArtifact{Buffer: &result, remaining: int64(len(f.artifact))})
			if err != nil {
				return err
			}
			if written != int64(len(f.artifact)) || !bytes.Equal(result.Bytes(), f.artifact) {
				return errors.New("Use result did not match the dummy artifact")
			}
			fmt.Fprintf(out, "Use %s succeeded: verified dummy artifact; credential stayed in runtime/receiver.\n", f.label)
			return clearIntent(intentPath)
		case api.WorkStateFailed, api.WorkStateCancelled:
			if err := clearIntent(intentPath); err != nil {
				return err
			}
			failure := &useFailureError{Label: f.label, State: observed.Snapshot.State}
			if observed.Snapshot.Failure != nil {
				failure.Cause = observed.Snapshot.Failure.Cause
			}
			return failure
		}
		select {
		case <-ctx.Done():
			return uncertain(id)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type boundedArtifact struct {
	*bytes.Buffer
	remaining int64
}

func (w *boundedArtifact) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("dummy artifact exceeded its expected bound")
	}
	w.remaining -= int64(len(p))
	return w.Buffer.Write(p)
}

func freshKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "rights-playground-" + hex.EncodeToString(b[:]), nil
}
