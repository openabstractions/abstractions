package main

import (
	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-asks/go/typedpermission"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/remote"
)

func typedPermissionValidators() typedpermission.Validators {
	return typedpermission.Validators{NativeProgram: fileid.ValidSubjectProgram, RemoteProgram: remote.ValidSubjectProgram, PackagedProgramPrefix: identity.PackagedProgramPrefix}
}
func validTypedPermissionDisplay(p asks.TypedPermission) bool {
	return typedpermission.ValidPermission(typedpermission.Permission(p), typedPermissionValidators())
}
