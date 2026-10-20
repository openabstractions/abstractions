package main

import (
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
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
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		program string
		native  bool
		remote  bool
	}{
		{program: self, native: true},
		{program: "relative-program"},
		{program: self + "\n"},
		{program: self + ":other-code", native: runtime.GOOS != "windows"},
		{program: remote.SubjectPrefix + strings.Repeat("a", 64), remote: true},
		{program: remote.SubjectPrefix + strings.Repeat("A", 64)},
	} {
		if gotNative, gotRemote := v.NativeProgram(tc.program), v.RemoteProgram(tc.program); gotNative != tc.native || gotRemote != tc.remote {
			t.Errorf("%q: native=%v remote=%v, want native=%v remote=%v", tc.program, gotNative, gotRemote, tc.native, tc.remote)
		}
	}
}
