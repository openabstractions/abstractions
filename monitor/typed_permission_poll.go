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
	for attempt := 0; attempt < 2; attempt++ {
		var records []askclient.RecordMetadata
		cursor, gap := "", false
		for page := 0; page < 64; page++ {
			result, err := operator.ListPermissionsContext(ctx, cursor, 64)
			if err != nil {
				return nil, err
			}
			switch result.Outcome {
			case askclient.OperatorPageOutcomePage:
			case askclient.OperatorPageOutcomeGap:
				gap = true
			default:
				return nil, &presenterRefusal{outcome: result.Outcome.String()}
			}
			if gap {
				break
			}
			for _, record := range result.Records {
				if record.Key != asksTypedPermissionKey {
					return nil, &presenterRefusal{outcome: "foreign permission record"}
				}
			}
			records = append(records, result.Records...)
			if result.Complete {
				return records, nil
			}
			if result.Next == "" || result.Next == cursor {
				return nil, &presenterRefusal{outcome: "incomplete permission page"}
			}
			cursor = result.Next
		}
		if !gap {
			return nil, &presenterRefusal{outcome: "incomplete"}
		}
	}
	return nil, &presenterRefusal{outcome: "gap"}
}
