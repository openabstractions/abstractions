package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

type failedMistakeWriter struct{}

func (failedMistakeWriter) Write([]byte) (int, error) { return 0, errors.New("diagnostics closed") }

type shortMistakeWriter struct{}

func (shortMistakeWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestMistakeWriteFailureRemainsReportable(t *testing.T) {
	for _, mistake := range []struct {
		name string
		call func() error
	}{
		{"command", func() error {
			return commandMistake(failedMistakeWriter{}, "missing command", "openabstractions --help")
		}},
		{"flag", func() error { return flagMistake(failedMistakeWriter{}, "status", "Usage: status", "invalid flag") }},
	} {
		t.Run(mistake.name, func(t *testing.T) {
			err := mistake.call()
			if err == nil || reported(err) || exitStatus(err) != exitUsage || !strings.Contains(err.Error(), "diagnostic write") {
				t.Fatalf("write failure incorrectly marked reported: %v", err)
			}
		})
	}
}

func TestMistakeShortWriteRemainsReportable(t *testing.T) {
	err := commandMistake(shortMistakeWriter{}, "missing command", "openabstractions --help")
	if err == nil || reported(err) || exitStatus(err) != exitUsage || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write incorrectly marked reported: %v", err)
	}
}

// runMain runs the real main() entry point in a separate process (the
// TestSupervisedProcessHelper fixture in supervised_test.go), with os.Args
// set to openabstractions followed by args, and reports its two streams and
// exit code.
func runMain(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=^TestSupervisedProcessHelper$")
	c.Env = helperEnv("runtime", args)
	var out, errOut strings.Builder
	c.Stdout, c.Stderr = &out, &errOut
	err := c.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.String(), errOut.String(), exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String(), 0
}

// TestMainMistakesNameThemselvesAndPointAtHelp is items 2 and 7's own test:
// no command, a misspelled one, "serve" with no capability, and an unknown
// capability each print exactly two lines to stderr, the second pointing at
// --help, and exit 2 — never the ~50-line catalogue, which prints only when
// help is actually requested (on stdout, exit 0, checked by
// TestServeCommandHelpAndEndpointRefusal in runtime_test.go).
func TestMainMistakesNameThemselvesAndPointAtHelp(t *testing.T) {
	cases := []struct {
		args        []string
		problem     string
		helpCommand string
	}{
		{nil, "no command given", "openabstractions --help"},
		{[]string{"statuz"}, `no command called "statuz"`, "openabstractions --help"},
		{[]string{"serve"}, "serve needs a capability", "openabstractions serve --help"},
		{[]string{"serve", "donwload"}, `no capability called "donwload"`, "openabstractions serve --help"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, "_"), func(t *testing.T) {
			stdout, stderr, code := runMain(t, c.args...)
			if code != 2 {
				t.Fatalf("%v: exit %d, want 2\nstdout %s\nstderr %s", c.args, code, stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("%v: stdout %q, want none", c.args, stdout)
			}
			lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
			if len(lines) != 2 {
				t.Fatalf("%v: stderr has %d lines, want 2:\n%s", c.args, len(lines), stderr)
			}
			if lines[0] != "openabstractions: "+c.problem {
				t.Fatalf("%v: first line %q, want %q", c.args, lines[0], "openabstractions: "+c.problem)
			}
			want := `Run "` + c.helpCommand + `" for the commands.`
			if lines[1] != want {
				t.Fatalf("%v: second line %q, want %q", c.args, lines[1], want)
			}
		})
	}
}

// TestServeHelpIsItsOwnScopedPage is item 4's own test: `serve --help` is not
// byte-identical to the top-level `--help`, and names the eight capabilities
// serve hosts rather than every command in the program.
func TestServeHelpIsItsOwnScopedPage(t *testing.T) {
	top, _, code := runMain(t, "--help")
	if code != 0 {
		t.Fatalf("--help: exit %d", code)
	}
	serve, _, code := runMain(t, "serve", "--help")
	if code != 0 {
		t.Fatalf("serve --help: exit %d", code)
	}
	if serve == top {
		t.Fatal("serve --help is byte-identical to the top-level help")
	}
	if !strings.HasPrefix(serve, "Usage: openabstractions serve <capability>") {
		t.Fatalf("serve --help does not start with its own usage line: %q", serve)
	}
	for _, capability := range []string{"runtime", "supervisor", "asks", "rights", "router", "logging", "config", "router-v1"} {
		if !strings.Contains(serve, "\n  "+capability) {
			t.Fatalf("serve --help omits capability %q: %s", capability, serve)
		}
	}
	if !strings.Contains(serve, "host is its old name") {
		t.Fatalf("serve --help does not name host as supervisor's old name: %s", serve)
	}
	if strings.Contains(serve, "download") || strings.Contains(serve, "credentials") {
		t.Fatalf("serve --help names top-level commands, not only its own capabilities: %s", serve)
	}
}

// TestServeSupervisorAcceptsHost is RENAME-PLAN §3, Panel and command line
// step 10: `serve supervisor` is the command and `serve host`, its old name,
// still reaches it for one release. Off Windows both give the same refusal.
func TestServeSupervisorAcceptsHost(t *testing.T) {
	for _, name := range []string{"supervisor", "host"} {
		if capabilities[name] == nil {
			t.Fatalf("serve %s is not a capability", name)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	for _, name := range []string{"supervisor", "host"} {
		err := capabilities[name](nil)
		var exit *exitError
		if !errors.As(err, &exit) || exit.code != exitUsage || !strings.Contains(err.Error(), "serve supervisor is the Windows runtime supervisor") {
			t.Fatalf("serve %s: %v, want the Windows-only refusal naming serve supervisor", name, err)
		}
	}
}

// TestInferenceServerAcceptsOldFlags is RENAME-PLAN §3, Panel and command
// line step 12: --api and --modalities parse, and so do --wire and
// --profiles for one release. A negative --timeout stops each command after
// parsing and before any runtime is resolved, so the refusal proves every
// flag was known.
func TestInferenceServerAcceptsOldFlags(t *testing.T) {
	for _, args := range [][]string{
		{"server", "add", "x", "--base", "https://x.invalid/v1", "--api", "openai-compatible", "--modalities", "chat", "--timeout", "-1s"},
		{"host", "add", "x", "--base", "https://x.invalid/v1", "--wire", "openai-compatible", "--profiles", "chat", "--timeout", "-1s"},
	} {
		var output, diagnostics bytes.Buffer
		err := inferenceCommand(args, &output, &diagnostics)
		var exit *exitError
		if !errors.As(err, &exit) || exit.code != exitUsage || !strings.Contains(diagnostics.String(), "must not be negative") {
			t.Fatalf("%v: %v\n%s", args, err, diagnostics.String())
		}
		if !strings.Contains(diagnostics.String(), "inference server add") {
			t.Fatalf("%v: the refusal does not name inference server add:\n%s", args, diagnostics.String())
		}
	}
}

// TestBareCommandGroupsAreMistakesNotHelp is item 7's own table test: every
// command group with subcommands, given no subcommand, behaves like item 2 —
// one line naming what is missing, one line pointing at --help, on stderr,
// exit 2 — never the whole help printed to stdout with exit 0.
func TestBareCommandGroupsAreMistakesNotHelp(t *testing.T) {
	groups := []struct {
		name    string
		run     func(output, diagnostics *bytes.Buffer) error
		problem string
		help    string
	}{
		{"jobs", func(o, d *bytes.Buffer) error { return jobsCommand(nil, o, d) },
			"jobs: a command is required", "openabstractions jobs --help"},
		{"jobs migrate-legacy", func(o, d *bytes.Buffer) error { return jobsCommand([]string{"migrate-legacy"}, o, d) },
			"jobs migrate-legacy: a command is required", "openabstractions jobs migrate-legacy --help"},
		{"rights", func(o, d *bytes.Buffer) error { return rightsCommand(nil, o, d) },
			"rights: a command is required", "openabstractions rights --help"},
		{"credentials", func(o, d *bytes.Buffer) error {
			return credentialsCommand(nil, strings.NewReader(""), o, d)
		}, "credentials: a command is required", "openabstractions credentials --help"},
		{"applications", func(o, d *bytes.Buffer) error { return applicationsCommand(nil, o, d) },
			"applications: a command is required", "openabstractions applications --help"},
		{"inference", func(o, d *bytes.Buffer) error { return inferenceCommand(nil, o, d) },
			"inference: a command is required", "openabstractions inference --help"},
		{"inference server", func(o, d *bytes.Buffer) error { return inferenceCommand([]string{"server"}, o, d) },
			"inference server: a command is required", "openabstractions inference --help"},
		// host is server's old name for one release; the message names server.
		{"inference host", func(o, d *bytes.Buffer) error { return inferenceCommand([]string{"host"}, o, d) },
			"inference server: a command is required", "openabstractions inference --help"},
		{"models", func(o, d *bytes.Buffer) error { return modelsCommand(nil, o, d) },
			"models: a command is required", "openabstractions models --help"},
		{"opencode", func(o, d *bytes.Buffer) error { return opencodeCommand(nil, o, d) },
			"opencode: a command is required", "openabstractions opencode --help"},
		{"provider", func(o, d *bytes.Buffer) error { return providerCommand(nil, o, d) },
			"provider: a command is required", "openabstractions provider --help"},
		{"host", func(o, d *bytes.Buffer) error { return hostCommand(nil, o, d) },
			"host: a command is required", "openabstractions host --help"},
		{"service", func(o, d *bytes.Buffer) error { return serviceCommand(nil, o, d) },
			"service: a command is required", "openabstractions service --help"},
		{"storage", func(o, d *bytes.Buffer) error { return storageCommand(nil, o, d) },
			"storage: a command is required", "openabstractions storage --help"},
		{"status describe", func(o, d *bytes.Buffer) error { return statusDescribe(nil, o, d) },
			"status describe: an endpoint is required", "openabstractions status describe --help"},
	}
	for _, g := range groups {
		t.Run(g.name, func(t *testing.T) {
			var output, diagnostics bytes.Buffer
			err := g.run(&output, &diagnostics)
			var exit *exitError
			if !errors.As(err, &exit) || exit.code != exitUsage {
				t.Fatalf("%s: err = %v, want *exitError{exitUsage}", g.name, err)
			}
			if !reported(err) {
				t.Fatalf("%s: err not marked already reported", g.name)
			}
			if output.Len() != 0 {
				t.Fatalf("%s: stdout %q, want none", g.name, output.String())
			}
			text := strings.TrimRight(diagnostics.String(), "\n")
			lines := strings.Split(text, "\n")
			if len(lines) != 2 {
				t.Fatalf("%s: diagnostics has %d lines, want 2 (not the whole catalogue):\n%s", g.name, len(lines), text)
			}
			if lines[0] != "openabstractions: "+g.problem {
				t.Fatalf("%s: first line %q, want %q", g.name, lines[0], "openabstractions: "+g.problem)
			}
			want := `Run "` + g.help + `" for the commands.`
			if lines[1] != want {
				t.Fatalf("%s: second line %q, want %q", g.name, lines[1], want)
			}
		})
	}
}

// TestCapabilityUsageErrorsExitTwo checks that each of the six capability
// modules registered in capabilities turns its own usage error into the
// same *exitError, code exitUsage (2), that every other command's usage
// error carries. It calls the wrapped Serve directly; nothing here spawns a
// process or reads a real process exit status.
func TestCapabilityUsageErrorsExitTwo(t *testing.T) {
	for _, name := range []string{"asks", "rights", "router", "logging", "config", "router-v1"} {
		run, ok := capabilities[name]
		if !ok {
			t.Fatalf("no capability called %q", name)
		}
		err := run([]string{"--not-a-real-flag"})
		var exit *exitError
		if !errors.As(err, &exit) {
			t.Fatalf("%s: err = %v, want *exitError", name, err)
		}
		if exit.code != exitUsage {
			t.Fatalf("%s: code = %d, want %d", name, exit.code, exitUsage)
		}
	}
}

// singleDashFlag matches a line of Go's flag.PrintDefaults own dump, such as
// "  -account string" or "  -timeout duration": the listing item 4 (round 1)
// and item 6 (round 3) both refuse, since every command's own prose usage
// already documents its flags.
var singleDashFlag = regexp.MustCompile(`^\s*-[a-zA-Z][\w-]*(\s|$)`)

// TestBadFlagShapeAcrossCommands is item 6's own table test: a bad flag
// prints exactly three lines to diagnostics ("openabstractions <command>:
// unknown flag <token as typed>", the command's usage line, and "Run
// ... --help for the flags."), writes nothing else anywhere, marks its
// error already reported (so main's own dispatch does not print a fourth,
// redundant copy), and exits usage (2). Checked across a representative
// command from each family: top-level, a "group sub" command, and a
// service-backed leaf.
func TestBadFlagShapeAcrossCommands(t *testing.T) {
	commands := []struct {
		name string
		run  func(args []string, output, diagnostics *bytes.Buffer) error
	}{
		{"status", func(args []string, output, diagnostics *bytes.Buffer) error {
			return runtimeStatus(args, output, diagnostics)
		}},
		{"status describe", func(args []string, output, diagnostics *bytes.Buffer) error {
			return statusDescribe(append([]string{"placeholder-endpoint"}, args...), output, diagnostics)
		}},
		{"rights list", func(args []string, output, diagnostics *bytes.Buffer) error {
			return rightsCommand(append([]string{"list"}, args...), output, diagnostics)
		}},
		{"probe", func(args []string, output, diagnostics *bytes.Buffer) error {
			return probeCommand(append([]string{"config"}, args...), output, diagnostics)
		}},
		{"jobs list", func(args []string, output, diagnostics *bytes.Buffer) error {
			return jobsCommand(append([]string{"list"}, args...), output, diagnostics)
		}},
		{"jobs migrate-legacy inspect", func(args []string, output, diagnostics *bytes.Buffer) error {
			return jobsCommand(append([]string{"migrate-legacy", "inspect"}, args...), output, diagnostics)
		}},
		{"download", func(args []string, output, diagnostics *bytes.Buffer) error {
			return downloadCommand(append([]string{"http://example.invalid/x"}, args...), output, diagnostics)
		}},
		{"credentials list", func(args []string, output, diagnostics *bytes.Buffer) error {
			return credentialsCommand(append([]string{"list"}, args...), strings.NewReader(""), output, diagnostics)
		}},
		{"applications list", func(args []string, output, diagnostics *bytes.Buffer) error {
			return applicationsCommand(append([]string{"list"}, args...), output, diagnostics)
		}},
		{"inference audit", func(args []string, output, diagnostics *bytes.Buffer) error {
			return inferenceCommand(append([]string{"audit"}, args...), output, diagnostics)
		}},
		{"models lends", func(args []string, output, diagnostics *bytes.Buffer) error {
			return modelsCommand(append([]string{"lends"}, args...), output, diagnostics)
		}},
		{"provider list", func(args []string, output, diagnostics *bytes.Buffer) error {
			return providerCommand(append([]string{"list"}, args...), output, diagnostics)
		}},
		{"opencode configure", func(args []string, output, diagnostics *bytes.Buffer) error {
			return opencodeCommand(append([]string{"configure"}, args...), output, diagnostics)
		}},
		{"resources", func(args []string, output, diagnostics *bytes.Buffer) error {
			return resourcesCommand(args, output, diagnostics)
		}},
		{"resources audit", func(args []string, output, diagnostics *bytes.Buffer) error {
			return resourcesCommand(append([]string{"audit"}, args...), output, diagnostics)
		}},
		{"start", func(args []string, output, diagnostics *bytes.Buffer) error {
			return runtimeStart(args, output, diagnostics)
		}},
		{"storage check", func(args []string, output, diagnostics *bytes.Buffer) error {
			return storageCommand(append([]string{"check"}, args...), output, diagnostics)
		}},
		{"serve runtime", func(args []string, output, diagnostics *bytes.Buffer) error {
			_, err := parseRuntime(args, diagnostics)
			return err
		}},
	}
	for _, c := range commands {
		t.Run(c.name, func(t *testing.T) {
			var output, diagnostics bytes.Buffer
			err := c.run([]string{"--bogus-flag"}, &output, &diagnostics)
			var exit *exitError
			if !errors.As(err, &exit) || exit.code != exitUsage {
				t.Fatalf("%s --bogus-flag: err = %v, want *exitError{exitUsage}", c.name, err)
			}
			if !reported(err) {
				t.Fatalf("%s: err not marked already reported; main's own dispatch would print it a second time", c.name)
			}
			if output.Len() != 0 {
				t.Fatalf("%s: output %q, want none", c.name, output.String())
			}
			text := strings.TrimRight(diagnostics.String(), "\n")
			lines := strings.Split(text, "\n")
			if len(lines) != 3 {
				t.Fatalf("%s: diagnostics has %d lines, want 3:\n%s", c.name, len(lines), text)
			}
			if !strings.HasPrefix(lines[0], "openabstractions "+c.name+": ") || !strings.Contains(lines[0], "--bogus-flag") {
				t.Fatalf("%s: first line %q does not name the flag as typed", c.name, lines[0])
			}
			if !strings.HasPrefix(lines[1], "Usage: openabstractions") {
				t.Fatalf("%s: second line %q is not the usage line", c.name, lines[1])
			}
			if !strings.HasPrefix(lines[2], "Run \"openabstractions "+c.name) || !strings.HasSuffix(lines[2], "--help\" for the flags.") {
				t.Fatalf("%s: third line %q does not point at --help", c.name, lines[2])
			}
			for _, line := range lines {
				if singleDashFlag.MatchString(line) {
					t.Fatalf("%s: line %q looks like a flag.PrintDefaults dump", c.name, line)
				}
			}
		})
	}
}

// TestTakesNoArgumentsIsAThreeLineMistake is item 1, round 6's own table
// test: a sixth naive user found `applications list extraarg`, `inference
// host list extraarg` and `credentials list extraarg` printing
// "openabstractions <command>: takes no arguments" alone, with no usage line
// and no --help pointer, unlike `status extraarg`'s three-line shape. Every
// "takes no arguments" (and every other bare usage refusal that used to
// print one line) now goes through flagMistake, so an unexpected argument on
// any zero-argument command ends the same way: the problem, the command's
// usage line, and how to see its flags, on stderr only, exit 2.
func TestTakesNoArgumentsIsAThreeLineMistake(t *testing.T) {
	cases := []struct {
		name string
		run  func(output, diagnostics *bytes.Buffer) error
	}{
		{"applications list", func(o, d *bytes.Buffer) error {
			return applicationsCommand([]string{"list", "extraarg"}, o, d)
		}},
		{"inference server list", func(o, d *bytes.Buffer) error {
			return inferenceCommand([]string{"server", "list", "extraarg"}, o, d)
		}},
		{"inference host list", func(o, d *bytes.Buffer) error {
			return inferenceCommand([]string{"host", "list", "extraarg"}, o, d)
		}},
		{"inference audit", func(o, d *bytes.Buffer) error {
			return inferenceCommand([]string{"audit", "extraarg"}, o, d)
		}},
		{"credentials list", func(o, d *bytes.Buffer) error {
			return credentialsCommand([]string{"list", "extraarg"}, strings.NewReader(""), o, d)
		}},
		{"credentials audit", func(o, d *bytes.Buffer) error {
			return credentialsCommand([]string{"audit", "extraarg"}, strings.NewReader(""), o, d)
		}},
		{"opencode configure", func(o, d *bytes.Buffer) error {
			return opencodeCommand([]string{"configure", "extraarg"}, o, d)
		}},
		{"models hosts", func(o, d *bytes.Buffer) error {
			return modelsCommand([]string{"hosts", "extraarg"}, o, d)
		}},
		{"models lends", func(o, d *bytes.Buffer) error {
			return modelsCommand([]string{"lends", "extraarg"}, o, d)
		}},
		{"provider list", func(o, d *bytes.Buffer) error {
			return providerCommand([]string{"list", "extraarg"}, o, d)
		}},
		{"resources audit", func(o, d *bytes.Buffer) error {
			return resourcesCommand([]string{"audit", "extraarg"}, o, d)
		}},
		{"resources acquire", func(o, d *bytes.Buffer) error {
			return resourcesCommand([]string{"acquire", "card:0"}, o, d)
		}},
		{"resources ask", func(o, d *bytes.Buffer) error {
			return resourcesCommand([]string{"ask", "card:0"}, o, d)
		}},
		{"jobs list", func(o, d *bytes.Buffer) error {
			return jobsCommand([]string{"list", "extraarg"}, o, d)
		}},
		{"storage check", func(o, d *bytes.Buffer) error {
			return storageCommand([]string{"check", "extraarg"}, o, d)
		}},
		{"rights list", func(o, d *bytes.Buffer) error {
			return rightsCommand([]string{"list", "extraarg"}, o, d)
		}},
		{"probe all", func(o, d *bytes.Buffer) error {
			return probeCommand([]string{"all", "extraoperation", "--timeout", "2s"}, o, d)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var output, diagnostics bytes.Buffer
			err := c.run(&output, &diagnostics)
			var exit *exitError
			if !errors.As(err, &exit) || exit.code != exitUsage {
				t.Fatalf("%s: err = %v, want *exitError{exitUsage}", c.name, err)
			}
			if output.Len() != 0 {
				t.Fatalf("%s: output %q, want none", c.name, output.String())
			}
			lines := strings.Split(strings.TrimRight(diagnostics.String(), "\n"), "\n")
			if len(lines) != 3 {
				t.Fatalf("%s: diagnostics has %d lines, want 3:\n%s", c.name, len(lines), diagnostics.String())
			}
			if !strings.HasPrefix(lines[0], "openabstractions ") {
				t.Fatalf("%s: first line %q does not name the program", c.name, lines[0])
			}
			if !strings.HasPrefix(lines[1], "Usage: openabstractions") {
				t.Fatalf("%s: second line %q is not a usage line", c.name, lines[1])
			}
			if !strings.Contains(lines[2], "--help") {
				t.Fatalf("%s: third line %q does not point at --help", c.name, lines[2])
			}
		})
	}
}

// rawErrorPhrase is a fragment of a raw OS or Go standard-library error this
// program must never show whole: the sweep's own forbidden list.
var rawErrorPhrases = []string{
	"cannot find the file", "The system cannot", "no such file",
	"strconv.Parse", "invalid URI", "parse \"",
}

// TestMalformedValuesAreSentencesNotRawErrors is the sweep's own table test:
// one malformed value for a URL, a path, an endpoint, a duration and a
// number, across different commands, each naming the flag or argument, the
// value as typed, and the accepted form — never the raw OS or Go error a
// naive user reported seeing instead. mustParse cases (the value's syntax is
// wrong) exit usage (2); runsAndFails cases (the value is well-formed but
// wrong at run time — a file that is not there) exit 1.
func TestMalformedValuesAreSentencesNotRawErrors(t *testing.T) {
	cases := []struct {
		name string
		run  func(output, diagnostics *bytes.Buffer) error
		want int
		says []string
	}{
		{"status --timeout (duration)", func(o, d *bytes.Buffer) error {
			return runtimeStatus([]string{"--timeout", "banana"}, o, d)
		}, exitUsage, []string{"--timeout", `"banana"`, "duration"}},
		{"resources audit --limit (number)", func(o, d *bytes.Buffer) error {
			return resourcesCommand([]string{"audit", "--limit", "banana"}, o, d)
		}, exitUsage, []string{"--limit", `"banana"`, "whole number"}},
		{"download (URL)", func(o, d *bytes.Buffer) error {
			return downloadCommand([]string{"not-a-url", "--timeout", "2s"}, o, d)
		}, exitUsage, []string{`"not-a-url"`, "URL"}},
		{"jobs migrate-legacy apply --mapping (path)", func(o, d *bytes.Buffer) error {
			return jobsCommand([]string{"migrate-legacy", "apply", "--state-dir", t.TempDir(), "--mapping", "/oa-sweep-test/absent-mapping.json"}, o, d)
		}, exitNotResolved, []string{"--mapping", "/oa-sweep-test/absent-mapping.json", "does not exist"}},
		{"status describe, malformed (endpoint)", func(o, d *bytes.Buffer) error {
			return statusDescribe([]string{"abstraction.config/reader@1", "--timeout", "2s"}, o, d)
		}, exitUsage, []string{"takes an endpoint", "for example"}},
		{"status describe, well-formed but absent (endpoint)", func(o, d *bytes.Buffer) error {
			return statusDescribe([]string{"nonsense-endpoint-word", "--timeout", "2s"}, o, d)
		}, exitNotResolved, []string{"no runtime listens at", `run "openabstractions status"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var output, diagnostics bytes.Buffer
			err := c.run(&output, &diagnostics)
			var exit *exitError
			if !errors.As(err, &exit) || exit.code != c.want {
				t.Fatalf("%s: err = %v, want *exitError{%d}", c.name, err, c.want)
			}
			combined := output.String() + diagnostics.String() + err.Error()
			for _, want := range c.says {
				if !strings.Contains(combined, want) {
					t.Fatalf("%s: output does not mention %q:\n%s", c.name, want, combined)
				}
			}
			for _, forbidden := range rawErrorPhrases {
				if strings.Contains(combined, forbidden) {
					t.Fatalf("%s: output still carries the raw error phrase %q:\n%s", c.name, forbidden, combined)
				}
			}
		})
	}
}
