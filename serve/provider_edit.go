package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"

	cas "github.com/openabstractions/abstraction-cas/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	inference "github.com/openabstractions/abstraction-inference/go"
)

type providerAdmissionError string

func (e providerAdmissionError) Error() string { return string(e) }

type providerConflictError string

func (e providerConflictError) Error() string { return string(e) }

type providerMutation func(string, []byte, any) error

// admitPairedName distinguishes a proven reservation from unavailable facts.
// The same classification applies at entry and under the commit's CAS lock.
func (p *runtimeProviders) admitPairedName(name string) error {
	if p.pairedNameAdmission == nil {
		return nil
	}
	err := p.pairedNameAdmission(name)
	if err == nil {
		return nil
	}
	if errors.Is(err, errPairingConflict) {
		return providerConflictError("paired_binding_name")
	}
	return errors.Join(providerAdmissionError("unavailable"), err)
}

// providerEditAdmission retains the entry point's actual action and caller.
// It runs again after waits, including inside the existing native CAS owner.
func (o *inferenceOperator) providerEditAdmission(ctx context.Context, caller inference.Subject, action string) func() error {
	return func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if word := o.gate(ctx, caller, action); word != "" {
			return providerAdmissionError(word)
		}
		return ctx.Err()
	}
}

func providerAdmit(ctx context.Context, admit func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if admit != nil {
		if err := admit(); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// providerMutator binds the observed bytes, including actual absence, under
// the same native CAS lock used by every cooperating configuration writer.
// Its read bound derives from the actual old/new bytes, not a product limit.
func providerMutator(ctx context.Context, admit func() error) providerMutation {
	return func(path string, base []byte, value any) error {
		if value == nil {
			unlock, err := cas.LockContext(ctx, path, false)
			if err != nil {
				return err
			}
			defer unlock()
			cur, err := cas.ReadLimitContext(ctx, path, int64(len(base))+1)
			if err != nil {
				return err
			}
			if !bytes.Equal(cur, base) {
				return cas.ErrMoved
			}
			if err := providerAdmit(ctx, admit); err != nil {
				return err
			}
			return os.Remove(path)
		}
		raw, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
		limit := max(len(base), len(raw), 1)
		return cas.ChangeLimitContext(ctx, path, int64(limit), func(cur []byte) ([]byte, error) {
			if !bytes.Equal(cur, base) {
				return nil, cas.ErrMoved
			}
			if base == nil {
				if _, err := os.Lstat(path); err == nil {
					return nil, cas.ErrMoved
				} else if !errors.Is(err, os.ErrNotExist) {
					return nil, err
				}
			}
			if err := providerAdmit(ctx, admit); err != nil {
				return nil, err
			}
			return raw, nil
		})
	}
}

// commitAdmission is called with p.mu held. Rights reads release their policy
// locks before their post-change callbacks; no child retirement runs here.
func (p *runtimeProviders) commitAdmission(ctx context.Context, expected, name string, paired bool, admit func() error) func() error {
	return func() error {
		if err := providerAdmit(ctx, admit); err != nil {
			return err
		}
		if paired {
			if err := p.admitPairedName(name); err != nil {
				return err
			}
		}
		_, revision, _, err := p.readWithDisabled(false)
		if err != nil {
			return err
		}
		if revision != expected {
			return cas.ErrMoved
		}
		return ctx.Err()
	}
}

func providerKnownPrecommit(err error) bool {
	var admission providerAdmissionError
	var conflict providerConflictError
	return errors.As(err, &admission) || errors.As(err, &conflict) ||
		errors.Is(err, cas.ErrMoved) || errors.Is(err, cas.ErrTooLarge) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// editFailureLocked preserves domain refusals and reports a fresh conflict
// revision. A failed read or uncertain write never becomes an empty registry.
func (p *runtimeProviders) editFailureLocked(err error) wire.DeclarationChange {
	var admission providerAdmissionError
	if errors.As(err, &admission) {
		return wire.DeclarationChange{Outcome: declarationEditRefusal(string(admission))}
	}
	var conflict providerConflictError
	if errors.Is(err, cas.ErrMoved) || errors.Is(err, cas.ErrTooLarge) || errors.As(err, &conflict) {
		_, revision, readErr := p.readCached()
		if readErr == nil {
			reason := "revision"
			if conflict != "" {
				reason = string(conflict)
			}
			return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeConflict, Revision: revision, Reason: reason}
		}
		p.report(readErr)
	}
	return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
}

func (p *runtimeProviders) beginRetirementLocked(name string) *providerSupervisor {
	old := p.supervisors[name]
	delete(p.supervisors, name)
	if old != nil {
		if p.retiring == nil {
			p.retiring = map[string]*providerSupervisor{}
		}
		p.retiring[name] = old
	}
	return old
}

func (p *runtimeProviders) finishRetirement(name string, old, next *providerSupervisor) {
	if old != nil {
		old.halt()
	}
	p.mu.Lock()
	if p.retiring[name] == old {
		delete(p.retiring, name)
	}
	start := next != nil && p.supervisors[name] == next && p.serving && !p.closed
	p.mu.Unlock()
	if start {
		next.start()
	}
}
