package main

import (
	"context"
	"fmt"
	"io"
	"os"

	facade "github.com/openabstractions/abstraction-facade/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
)

// --register adds this program to the runtime's Apps under the account-wide
// registration default (APPLICATIONS.md, rights RIGHTS-R22). The runtime then
// asks the person, in its own question, whether to keep it; until Keep the
// Panel shows "Pretend Lemonade (added itself)".
//
// --read-secret NAME reads one saved secret's raw value, a permission no
// account-wide default covers. The first attempt is refused and the runtime
// asks the person; after Allow the same command reads it. The value is
// compared with nothing and printed nowhere: only its length is reported, and
// this process clears its copy at once.

const (
	applicationName  = "pretend-lemonade"
	applicationTitle = "Pretend Lemonade"
)

func registerSelf(ctx context.Context, selected *facade.Machine, out io.Writer) error {
	apps, err := selected.ResolveApplications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return fmt.Errorf("application directory: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	change, err := apps.Register(ctx, wire.ApplicationDescriptor{Name: applicationName, Program: identity.CanonicalProgramPath(exe), Title: applicationTitle,
		StartGuidance: "Run Download.cmd in the example folder."})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, registerSentence(change))
	if err == nil && change.Outcome != wire.ApplicationOutcomeApplied && change.Outcome != wire.ApplicationOutcomeConflict {
		err = fmt.Errorf("registration %s", change.Outcome)
	}
	return err
}

// registerSentence says what one Register outcome means for the person.
func registerSentence(change wire.ApplicationChange) string {
	switch {
	case change.Outcome == wire.ApplicationOutcomeApplied:
		return "registered itself as " + applicationTitle + "; OpenAbstractions asks whether to keep it"
	case change.Outcome == wire.ApplicationOutcomeConflict && change.Reason == "name":
		return applicationTitle + " is already in Apps"
	case change.Outcome == wire.ApplicationOutcomeConflict && change.Reason == "unconfirmed":
		return applicationTitle + " already added itself and waits for your answer"
	case change.Outcome == wire.ApplicationOutcomeForbidden:
		return "registration refused: forbidden (the Add itself to Apps default is off, or this app is blocked)"
	}
	return fmt.Sprintf("registration %s %s", change.Outcome, change.Reason)
}

func readSecret(ctx context.Context, selected *facade.Machine, name string, out io.Writer) error {
	reader, err := selected.ResolveCredentialReader(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return fmt.Errorf("secret reader: %w", err)
	}
	result, err := reader.Read(ctx, name)
	if result.Value != nil {
		defer clear(result.Value.Secret)
	}
	if err != nil {
		return err
	}
	if result.Value == nil {
		return fmt.Errorf("read %s: %s", name, result.Outcome)
	}
	_, err = fmt.Fprintf(out, "read %s: %d bytes; this app cleared its copy\n", name, len(result.Value.Secret))
	return err
}
