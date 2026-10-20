package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

// Operator commands run the explicitly selected runtime's own legitimate CLI.
// Apps A/B still make effect calls themselves and receive no operator authority.
func operatorCall(ctx context.Context, c connection, stdin []byte, args ...string) ([]byte, error) {
	base := strings.ToLower(filepath.Base(c.RuntimeProgram))
	if base != "openabstractions" && base != "openabstractions.exe" {
		return nil, errors.New("setup/reset requires this pack's openabstractions operator executable")
	}
	cmd := exec.CommandContext(ctx, c.RuntimeProgram, append(args, "--endpoint", c.Endpoint, "--runtime-program", c.RuntimeProgram, "--json", "--timeout", "30s")...)
	cmd.Stdin = bytes.NewReader(stdin)
	var output limitedOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	err := cmd.Run()
	if err != nil {
		// Underlying storage/transport output is not forwarded. It can be
		// uncertain, and reset must preserve the connection/state for retry.
		return nil, errors.New("operator call did not complete; keep this run for reconciliation")
	}
	if output.overflow {
		return nil, errors.New("operator reply exceeded its bound")
	}
	return output.data.Bytes(), nil
}

type limitedOutput struct {
	data     bytes.Buffer
	overflow bool
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	if len(p)+w.data.Len() > 1<<20 {
		w.overflow = true
		return len(p), nil
	}
	return w.data.Write(p)
}

func setup(ctx context.Context, c connection, out io.Writer) error {
	data, err := operatorCall(ctx, c, nil, "credentials", "list")
	if err != nil {
		return err
	}
	var listed struct {
		Records []struct {
			Name  string `json:"name"`
			State string `json:"state"`
			Kind  string `json:"kind"`
			Scope struct {
				Targets   []string `json:"targets"`
				Consumers []string `json:"consumers"`
			} `json:"scope"`
		} `json:"records"`
	}
	if err := json.Unmarshal(data, &listed); err != nil {
		return errors.New("setup metadata was not definite")
	}
	for _, label := range []string{"alpha", "beta"} {
		f, _ := fixtureFor(label)
		present := false
		for _, record := range listed.Records {
			if record.Name == f.name {
				if record.State != "active" || record.Kind != "bearer" || len(record.Scope.Targets) != 1 || record.Scope.Targets[0] != "127.0.0.1" || len(record.Scope.Consumers) != 1 || record.Scope.Consumers[0] != "abstraction.download/http-execution@1" {
					return errors.New("existing dummy registration differs; Reset this run before setup")
				}
				present = true
			}
		}
		if !present {
			if _, err := operatorCall(ctx, c, f.secret, "credentials", "add", f.name, "--from-stdin", "--target", "127.0.0.1", "--for", "abstraction.download/http-execution@1"); err != nil {
				return fmt.Errorf("setup %s: %w", label, err)
			}
		}
		for i := range f.secret {
			f.secret[i] = 0
		}
		fmt.Fprintln(out, "dummy name ready:", f.name)
	}
	for _, program := range []string{c.AppA, c.AppB} {
		if _, err := operatorCall(ctx, c, nil, "rights", "grant", "--program", program, "--action", "abstraction.job/acceptance.submit", "--resource", "job:download"); err != nil {
			return fmt.Errorf("download prerequisite: %w", err)
		}
	}
	fmt.Fprintln(out, "setup complete: only job:download prerequisite granted to A/B; credential Use/Read remain ungranted")
	return nil
}

func resetSecrets(ctx context.Context, c connection, out io.Writer) error {
	data, err := operatorCall(ctx, c, nil, "credentials", "list")
	if err != nil {
		return err
	}
	var reply struct {
		Records []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"records"`
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &reply); err != nil || json.Unmarshal(data, &fields) != nil || fields["records"] == nil {
		return errors.New("operator metadata reply was not definite; reset kept state")
	}
	for _, label := range []string{"alpha", "beta"} {
		f, _ := fixtureFor(label)
		found := false
		for _, record := range reply.Records {
			if record.Name == f.name && record.State != "revoked" {
				found = true
			}
		}
		if found {
			data, err := operatorCall(ctx, c, nil, "credentials", "revoke", f.name)
			if err != nil {
				return fmt.Errorf("reset %s: %w", label, err)
			}
			var result struct {
				Outcome string `json:"outcome"`
			}
			if err := json.Unmarshal(data, &result); err != nil || result.Outcome != "revoked" {
				return errors.New("dummy deletion was not definite; reset kept state")
			}
		}
		fmt.Fprintln(out, "dummy value removed or already absent:", f.name)
	}
	fmt.Fprintln(out, "owned dummy deletion complete; lifecycle helper may now stop owned processes and remove run state")
	return nil
}
