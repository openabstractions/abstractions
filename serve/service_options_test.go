package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every family that shares serviceOptions must reject malformed independent
// proof as usage before attempting resolution, rather than report an IPC gap.
func TestServiceOptionsProofRefusalsAcrossConsumers(t *testing.T) {
	commands := []struct {
		name string
		args []string
		run  func([]string, io.Writer, io.Writer) error
	}{
		{"credentials", []string{"list"}, func(a []string, o, d io.Writer) error { return credentialsCommand(a, strings.NewReader(""), o, d) }},
		{"applications", []string{"list"}, applicationsCommand},
		{"download", []string{"http://example.invalid/x"}, downloadCommand},
		{"inference", []string{"audit"}, inferenceCommand},
		{"jobs", []string{"list"}, jobsCommand},
		{"models", []string{"lends"}, modelsCommand},
		{"models hosts", []string{"hosts"}, modelsCommand},
		{"provider", []string{"list"}, providerCommand},
		{"questions", []string{"pending"}, questionsCommand},
		{"resources", nil, resourcesCommand},
		{"resources acquire", []string{"acquire", "ram", "1"}, resourcesCommand},
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := unreachableEndpoint(t)
	t.Setenv(runtimeEndpointVar, endpoint)
	for _, command := range commands {
		for _, proof := range []struct {
			name string
			args []string
			want string
		}{
			{"relative", []string{"--endpoint", endpoint, "--runtime-program", "relative/runtime"}, "--runtime-program must be an absolute executable path"},
			{"missing endpoint", []string{"--runtime-program", program}, "--runtime-program verifies an --endpoint; give both"},
			{"NUL", []string{"--endpoint", endpoint, "--runtime-program", program + "\x00suffix"}, "--runtime-program must be an absolute executable path"},
		} {
			t.Run(command.name+"/"+proof.name, func(t *testing.T) {
				var output, diagnostics bytes.Buffer
				args := append(append([]string{}, command.args...), proof.args...)
				err := command.run(args, &output, &diagnostics)
				assertExit(t, err, exitUsage, command.name)
				if output.Len() != 0 || !strings.Contains(diagnostics.String(), proof.want) || strings.Contains(diagnostics.String(), "unverified") {
					t.Fatalf("proof refusal: output=%q diagnostics=%q err=%v", output.String(), diagnostics.String(), err)
				}
			})
		}
	}
}

// Real native transport checks the supplied program independently. A wrong
// expectation cannot read metadata; the correct one still reaches the runtime.
func TestServiceOptionsCredentialsVerifiedRuntime(t *testing.T) {
	options, _ := credentialsRuntime(t)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrong := filepath.Join(t.TempDir(), "different-runtime")
	_, diagnostics, err := runCredentials(t, "", "list", "--endpoint", options.endpoint, "--runtime-program", wrong, "--json", "--timeout", "3s")
	assertExit(t, err, exitNotResolved, "wrong runtime program")
	if !strings.Contains(err.Error(), wrongRuntimeFirstLine) {
		t.Fatalf("wrong-program refusal lost: %v %s", err, diagnostics)
	}
	output, diagnostics, err := runCredentials(t, "", "list", "--endpoint", options.endpoint, "--runtime-program", program, "--json", "--timeout", "3s")
	if err != nil || !strings.Contains(output, `"records"`) || diagnostics != "" {
		t.Fatalf("verified metadata read: %v output=%q diagnostics=%q", err, output, diagnostics)
	}
}

func TestServiceOptionsHelpNamesIndependentRuntimeProof(t *testing.T) {
	for name, usage := range map[string]string{
		"applications": applicationsUsage, "credentials": credentialsUsage, "download": downloadUsage,
		"inference": inferenceUsage, "jobs": jobsServiceUsage, "models": modelsUsage,
		"provider": providerUsage, "questions": questionsUsage, "resources": resourcesUsage, "resources acquire": resourcesAskUsage,
	} {
		if !strings.Contains(usage, "--runtime-program") {
			t.Errorf("%s help omits runtime proof", name)
		}
	}
}
