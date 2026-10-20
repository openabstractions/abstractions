package main

import (
	"context"
	"errors"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
)

type setupPackageFixture struct {
	fileid.Handle
	checks  int
	changed bool
}

func (f *setupPackageFixture) Check(context.Context) error {
	f.checks++
	if f.changed {
		return errors.New("registered root changed")
	}
	return nil
}

func TestSetupRegisteredPackageRechecksOwningRegistration(t *testing.T) {
	options, _, _ := claudeSetupFixture(t)
	held, _, err := setupIdentity(context.Background(), options.cli, false)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	image := &setupPackageFixture{Handle: held}
	if !setupIdentitiesCurrent(context.Background(), []setupIdentityRef{image}) || image.checks != 1 {
		t.Fatal("shared owner was not rechecked", image.checks)
	}
	image.changed = true
	if setupIdentitiesCurrent(context.Background(), []setupIdentityRef{image}) || image.checks != 2 {
		t.Fatal("changed native registration admitted", image.checks)
	}
}
