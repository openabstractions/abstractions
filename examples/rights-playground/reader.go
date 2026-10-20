package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"

	wire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	facade "github.com/openabstractions/abstraction-facade/go"
)

// The separately resolved SDK profile binds the recipient from the native
// transport. This app never supplies a recipient, subject, consumer or target.
func readSecret(ctx context.Context, c connection, label string, out io.Writer) error {
	f, err := fixtureFor(label)
	if err != nil {
		return err
	}
	if _, err := actualAppProgram(c); err != nil {
		return err
	}
	m, err := selectedMachine(c)
	if err != nil {
		return err
	}
	reader, err := m.ResolveCredentialReader(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return errors.New("Read unavailable: required reader@1 route did not resolve")
	}
	return readWithClient(ctx, reader, f, out)
}

type secretReader interface {
	Read(context.Context, string) (wire.SecretReadResult, error)
}

// Raw Read is disclosed to this app, unlike Use. Compare the dummy value
// privately and wipe this app's returned copy on every outcome. Evidence
// contains only a label/outcome; no value or derivative enters output.
func readWithClient(ctx context.Context, reader secretReader, f fixture, out io.Writer) error {
	result, err := reader.Read(ctx, f.name)
	if result.Value != nil {
		defer func() {
			for i := range result.Value.Secret {
				result.Value.Secret[i] = 0
			}
		}()
	}
	if err != nil {
		return errors.New("Read did not complete; no successful raw disclosure claimed")
	}
	if result.Outcome != wire.SecretReadOutcomeRead {
		if result.Value != nil {
			return errors.New("Read reply was inconsistent")
		}
		return &readRefusalError{Label: f.label, Outcome: result.Outcome}
	}
	if result.Value == nil || result.Value.Name != f.name || result.Value.Revision == "" || subtle.ConstantTimeCompare(result.Value.Secret, f.secret) != 1 {
		return errors.New("Read result did not match the current dummy fixture value")
	}
	_, err = fmt.Fprintf(out, "Read %s succeeded: verified dummy value in this app, then cleared it.\n", f.label)
	return err
}
