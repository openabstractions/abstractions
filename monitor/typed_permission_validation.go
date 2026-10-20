package main

import (
	"context"
	wire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	"github.com/openabstractions/abstraction-asks/go/typedpermission"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/remote"
)

// Authority display uses the pure owner with the native Identity grammar.
func readTypedPermissionDetails(ctx context.Context, reader typedpermission.PermissionDetailsReader, account, id string) (wire.TypedPermissionDetails, error) {
	return typedpermission.ReadPermissionDetailsContext(ctx, reader, account, id, typedPermissionValidators())
}

func typedPermissionValidators() typedpermission.Validators {
	return typedpermission.Validators{NativeProgram: fileid.ValidSubjectProgram, RemoteProgram: remote.ValidSubjectProgram, PackagedProgramPrefix: identity.PackagedProgramPrefix}
}

func readQuestionDetails(ctx context.Context, reader typedpermission.DetailsReader, account, id string) (wire.QuestionDetails, error) {
	return typedpermission.ReadDetailsContext(ctx, reader, account, id, typedPermissionValidators())
}
func readQuestionContent(ctx context.Context, reader typedpermission.DetailsReader, account, id string) (wire.QuestionContent, error) {
	return typedpermission.ReadContentContext(ctx, reader, account, id, typedPermissionValidators())
}
