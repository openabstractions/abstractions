package main

import (
	"path/filepath"
	"reflect"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/placetest"
	"github.com/openabstractions/abstraction-identity/remote"
)

func TestPermissionIdentityValidatorComposition(t *testing.T) {
	v := typedPermissionValidators()
	if v.NativeProgram == nil || v.RemoteProgram == nil {
		t.Fatal("missing Identity owner")
	}
	if reflect.ValueOf(v.NativeProgram).Pointer() != reflect.ValueOf(fileid.ValidSubjectProgram).Pointer() || reflect.ValueOf(v.RemoteProgram).Pointer() != reflect.ValueOf(remote.ValidSubjectProgram).Pointer() {
		t.Fatal("native composition substituted its own grammar")
	}
	if v.PackagedProgramPrefix != identity.PackagedProgramPrefix {
		t.Fatal("native spelling owner drift")
	}
	table, _, err := placetest.Load()
	if err != nil {
		t.Fatal(err)
	}
	// The shared corpus currently has a Windows stream case and no native
	// grammar row for Unix. Exercise both owners on all hosts.
	program := filepath.Join(t.TempDir(), "fixture")
	if !v.NativeProgram(program) || v.RemoteProgram(program) || v.NativeProgram("relative/program") {
		t.Fatal("native and remote program validators did not enforce their own forms")
	}
	for _, row := range table.Rows {
		if row.Op != "ValidSubjectProgram" || !row.ForThisPlatform() {
			continue
		}
		if v.NativeProgram(row.A) != fileid.ValidSubjectProgram(row.A) || v.RemoteProgram(row.A) != remote.ValidSubjectProgram(row.A) {
			t.Fatal(row.ID, "Identity corpus drift")
		}
	}
}
