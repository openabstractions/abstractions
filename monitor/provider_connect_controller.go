package main

import (
	"context"

	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

type providerConnectError string

func (e providerConnectError) Error() string                   { return string(e) }
func (e providerConnectError) NativeCredentialMessage() string { return string(e) }

const (
	errProviderBudgetPending     providerConnectError = "Daily limits for this app are not available yet. Nothing was allowed."
	errProviderKeyRejected       providerConnectError = "That key didn't work."
	errProviderConnectionChanged providerConnectError = "This request changed. Nothing was allowed; check it again."
	errProviderStore             providerConnectError = "The key could not be stored. Nothing was allowed."
	errProviderHost              providerConnectError = "The model server could not be configured. Earlier setup steps were kept."
	errProviderBudget            providerConnectError = "The daily limit could not be confirmed. Earlier setup steps were kept."
	errProviderCostUnreported    providerConnectError = "Cost was not reported for this app today. The spending limit is unavailable."
	errProviderRights            providerConnectError = "The app's use permission could not be confirmed. Earlier setup steps were kept."
	errProviderReadback          providerConnectError = "The connection could not be confirmed. Earlier setup steps were kept."
	errProviderPermissionRemoved providerConnectError = "The permission was removed. Finish setup did not restore it."
)

// providerKeyForm is a process-private native field controller. It is never
// serialized to Asks, the Mac bridge, a tool, a URL or a log. Only the native
// person-operated surface may call Allow. Candidate bytes are consumed once.
type providerKeyForm struct {
	Label, KeyPage, BudgetUnit string
	NeedsKey                   bool
	NeedsKeyNow                func() bool
	RetryOnly                  func() bool
	DefaultBudget              int64
	PendingReason              string
	Warning                    func() string
	Allow                      func(context.Context, []byte, int64) error
}

// providerConnectController receives the requester and exact question from
// the native readback owner. None is a tool argument or a subject override.
// A missing budget service fails before Store or a permission answer.
type providerConnectController struct {
	provider      string
	missing       bool
	warning       string
	granted       bool
	recheck       func(context.Context) error
	validate      func(context.Context, string, []byte) router.CandidateValidation
	preflight     func(context.Context) error
	store         func(context.Context, []byte) error
	configure     func(context.Context) error
	budget        func(context.Context, int64) error
	grant         func(context.Context) error
	readback      func(context.Context) error
	initialBudget *int64
}

func (c *providerConnectController) form() (*providerKeyForm, error) {
	p, ok := hostedcatalogue.Lookup(c.provider)
	if !ok {
		return nil, errProviderConnectionChanged
	}
	if _, err := hostedcatalogue.Select(c.provider); err != nil {
		return nil, errProviderConnectionChanged
	}
	form := &providerKeyForm{Label: p.Label, KeyPage: p.KeyPage, BudgetUnit: p.Budget, NeedsKey: c.missing, DefaultBudget: p.DefaultBudget, Allow: c.allow}
	if c.initialBudget != nil {
		form.DefaultBudget = *c.initialBudget
	}
	form.Warning = func() string { return c.warning }
	form.NeedsKeyNow = func() bool { return c.missing }
	form.RetryOnly = func() bool { return c.granted }
	if c.budget == nil || c.configure == nil || c.preflight == nil || c.readback == nil {
		form.PendingReason = errProviderBudgetPending.Error()
	}
	return form, nil
}

func (c *providerConnectController) allow(ctx context.Context, candidate []byte, amount int64) error {
	defer clear(candidate)
	p, ok := hostedcatalogue.Lookup(c.provider)
	if !ok || amount < 0 || amount > maxPresetBudget || c.recheck == nil || c.grant == nil {
		return errProviderConnectionChanged
	}
	if c.budget == nil || c.configure == nil || c.preflight == nil || c.readback == nil {
		return errProviderBudgetPending
	}
	if c.granted {
		if err := c.readback(ctx); err != nil || ctx.Err() != nil {
			if err == errProviderPermissionRemoved {
				return err
			}
			return errProviderReadback
		}
		return nil
	}
	if err := c.recheck(ctx); err != nil || ctx.Err() != nil {
		return errProviderConnectionChanged
	}
	if err := c.preflight(ctx); err != nil {
		return errProviderBudgetPending
	}
	if c.missing {
		if !p.AcceptsKey(candidate) || c.validate == nil || c.store == nil {
			return errProviderKeyRejected
		}
		// The validation owner consumes its own transient copy. The native
		// controller retains the candidate only until this explicit Allow ends.
		transient := append([]byte(nil), candidate...)
		defer clear(transient)
		c.warning = ""
		switch c.validate(ctx, c.provider, transient) {
		case router.CandidateValid:
		case router.CandidateUnreachable:
			c.warning = "Couldn't check the key right now."
		default:
			return errProviderKeyRejected
		}
	} else if len(candidate) != 0 {
		return errProviderConnectionChanged
	}
	if err := c.recheck(ctx); err != nil || ctx.Err() != nil {
		return errProviderConnectionChanged
	}
	if c.missing {
		if err := c.store(ctx, candidate); err != nil {
			return errProviderStore
		}
		c.missing = false
	}
	if err := c.configure(ctx); err != nil {
		if _, ok := err.(*providerHostConflict); ok {
			return err
		}
		return errProviderHost
	}
	if err := c.budget(ctx, amount); err != nil {
		if err == errProviderCostUnreported {
			return err
		}
		return errProviderBudget
	}
	if err := c.recheck(ctx); err != nil || ctx.Err() != nil {
		return errProviderConnectionChanged
	}
	if err := c.grant(ctx); err != nil {
		return errProviderRights
	}
	c.granted = true
	if err := c.readback(ctx); err != nil || ctx.Err() != nil {
		if err == errProviderPermissionRemoved {
			return err
		}
		return errProviderReadback
	}
	return nil
}
