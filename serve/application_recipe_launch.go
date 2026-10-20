package main

import (
	"context"
	"errors"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// launchRegisteredRecipe is the shared mechanism below caller authorization.
// Callers retain their own permission, generation and readiness adapters.
func (d *applicationDirectory) launchRegisteredRecipe(ctx context.Context, descriptor wire.ApplicationDescriptor, recheck func(context.Context) error) error {
	if descriptor.Activation == nil || d.launch == nil || recheck == nil {
		return errors.New("applications: registered recipe unavailable")
	}
	if err := recheck(ctx); err != nil {
		return err
	}
	return d.launch(descriptor.Program, append([]string{}, descriptor.Activation.Arguments...))
}
