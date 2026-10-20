package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The Asks presenter is its own program beside the Panel. The runtime's
// sign-in entry and its supervisor start it; a Panel operation that needs the
// person's own consent hands it one Book occurrence with -present and waits
// for the outcome.

// presenterFileStem and panelFileStem are the two operator programs' file
// names without the platform suffix.
const (
	presenterFileStem = "OpenAbstractions Presenter"
	panelFileStem     = oaPanelFileName
)

// siblingProgram is the OA program stem installed beside this executable.
func siblingProgram(stem string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		stem += ".exe"
	}
	return filepath.Join(filepath.Dir(self), stem), nil
}

// panelProgramPath is the Panel's executable: this program in the Panel, and the
// Panel beside it in the presenter.
func panelProgramPath() (string, error) {
	if presenterBuild {
		return siblingProgram(panelFileStem)
	}
	return os.Executable()
}

// presenterExecutable is the presenter's executable beside this program.
func presenterExecutable() (string, error) {
	if presenterBuild {
		return os.Executable()
	}
	return siblingProgram(presenterFileStem)
}

// explicitRuntimeArgs passes an explicitly selected runtime on to another OA
// program, so both act on the same one.
func explicitRuntimeArgs(binding *explicitRuntime) []string {
	if binding == nil {
		return nil
	}
	args := []string{"-runtime-endpoint", binding.Endpoint, "-runtime-program", binding.Program}
	if binding.StateDir != "" {
		args = append(args, "-runtime-state-dir", binding.StateDir)
	}
	return args
}

// launchPresenter starts the presenter, detached. A running presenter keeps
// its session claim and the new one exits.
func launchPresenter() error {
	program, err := presenterExecutable()
	if err != nil {
		return err
	}
	cmd := childproc.Hidden(exec.Command(program, append([]string{"--background"}, explicitRuntimeArgs(panelExplicit)...)...))
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// handedKind is which Book occurrence a Panel operation may hand over.
type handedKind int

const (
	handedNone handedKind = iota
	// handedSetup is Setup's own pending question.
	handedSetup
	// handedProvider is a provider connection the person allowed whose
	// credential form did not finish (Finish setup).
	handedProvider
)

// presentOccurrence shows the Book occurrence id that a Panel operation
// handed over: Setup's pending question, or the credential form of a
// provider connection the person allowed whose key is still missing. It
// shows nothing else.
func (p *questionPresenter) presentOccurrence(ctx context.Context, id string) presentResult {
	operator, err := p.operator(ctx)
	if err != nil {
		return presentResult{Outcome: presentUnavailable, Err: err}
	}
	records, err := listPresenterQuestions(ctx, operator)
	if err != nil {
		return presentResult{Outcome: presentUnavailable, Err: err}
	}
	for _, record := range records {
		if record.ID != id {
			continue
		}
		kind := handedOccurrence(record)
		if kind == handedNone && record.Key != asks.SetupModelsKey {
			break
		}
		bound := *p
		bound.operator = func(context.Context) (presenterOperator, error) { return operator, nil }
		show := func(ctx context.Context) presentResult {
			if record.Key == asks.SetupModelsKey {
				return bound.present(ctx, record)
			}
			return bound.reopenProvider(ctx, id)
		}
		if native, ok := operator.(*askclient.Operator); ok {
			return awaitHandedPopup(ctx, native, record, show)
		}
		return show(ctx)
	}
	return presentResult{Outcome: presentUnavailable, Err: errHandedOccurrence}
}

var errHandedOccurrence = errors.New("the presenter shows only Setup's question or Finish setup's form from the Book")

func handedOccurrence(record askclient.RecordMetadata) handedKind {
	switch {
	case record.Key == asks.SetupModelsKey && record.Option == "":
		return handedSetup
	case record.Key == asks.FirstUseKey && record.Option == "allow":
		return handedProvider
	}
	return handedNone
}

// The outcomes of one handed occurrence, as the presenter's exit code
// carries them back to the Panel.
const (
	occurrenceAnswered    = "answered"
	occurrenceCompleted   = "completed"
	occurrenceApplied     = "applied"
	occurrenceClosed      = "closed"
	occurrenceRemoved     = "removed"
	occurrenceUnavailable = "unavailable"
)

var occurrenceExitCodes = map[string]int{occurrenceAnswered: 0, occurrenceCompleted: 10, occurrenceApplied: 11,
	occurrenceClosed: 12, occurrenceRemoved: 13, occurrenceUnavailable: 14}

// occurrenceOutcome names what became of one handed occurrence.
func occurrenceOutcome(r presentResult) string {
	switch {
	case errors.Is(r.Err, errProviderPermissionRemoved):
		return occurrenceRemoved
	case r.Outcome == presentClosed:
		return occurrenceClosed
	case r.Outcome != presentAnswered || r.Err != nil:
		return occurrenceUnavailable
	case r.Answered.RuleOutcome == grants.RuleCompleted:
		return occurrenceCompleted
	case r.Answered.Edit != nil && r.Answered.Edit.Outcome == rightsclient.PolicyEditOutcomeApplied:
		return occurrenceApplied
	case r.Answered.Decision.Outcome == askclient.OperatorDecisionOutcomeAnswered && r.Answered.Decision.Record != nil:
		return occurrenceAnswered
	}
	return occurrenceUnavailable
}

func occurrenceExitCode(outcome string) int {
	if code, ok := occurrenceExitCodes[outcome]; ok {
		return code
	}
	return occurrenceExitCodes[occurrenceUnavailable]
}

func occurrenceFromExitCode(code int) string {
	for outcome, c := range occurrenceExitCodes {
		if c == code {
			return outcome
		}
	}
	return occurrenceUnavailable
}

// presentThroughPresenter hands the Book occurrence id to the presenter
// program and waits for its outcome. The Panel draws nothing.
var presentThroughPresenter = func(ctx context.Context, id string) (string, error) {
	program, err := presenterExecutable()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(program); err != nil {
		return "", errors.New("the OpenAbstractions presenter is not installed beside this Panel")
	}
	cmd := childproc.Hidden(exec.CommandContext(ctx, program, append([]string{"-present", id}, explicitRuntimeArgs(panelExplicit)...)...))
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		return occurrenceFromExitCode(exit.ExitCode()), nil
	}
	if err != nil {
		return "", err
	}
	return occurrenceAnswered, nil
}
