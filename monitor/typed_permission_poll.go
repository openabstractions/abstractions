package main

import (
	"context"
	"errors"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
)

// Typed permissions have their own closed operator route. An older runtime
// may explicitly omit this route while continuing to serve its exact asks.
func pollPermissionRecords(ctx context.Context, machine *facade.Machine) ([]askclient.RecordMetadata, bool) {
	operator, err := machine.ResolveAsksPermissionOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		var refusal *facade.ResolutionError
		if errors.As(err, &refusal) && refusal.Status == "incompatible" {
			return nil, false
		}
		return nil, true
	}
	records, err := listPermissionQuestions(ctx, operator)
	if ordinaryUnsupported(err) {
		return nil, false
	}
	return records, err != nil
}

func listPermissionQuestions(ctx context.Context, operator presenterPermissionOperator) ([]askclient.RecordMetadata, error) {
	records, err := presenterList(operator.ListAllPermissionsContext(ctx))
	for _, record := range records {
		if record.Key != asksTypedPermissionKey {
			return nil, &presenterRefusal{outcome: "foreign permission record"}
		}
	}
	return records, err
}
