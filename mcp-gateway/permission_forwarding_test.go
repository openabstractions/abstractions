package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type permissionForwardingBackend struct {
	gateway.Backend
	calls int
	ctx   context.Context
}

func (b *permissionForwardingBackend) AwaitPermissionQuestions(ctx context.Context, ids []string) (string, error) {
	b.calls++
	b.ctx = ctx
	if !reflect.DeepEqual(ids, []string{"q1"}) {
		return "", errors.New("question changed")
	}
	return "allowed", nil
}
func TestNativePermissionQuestionForwardingPreservesCallerAndAdmission(t *testing.T) {
	base := new(permissionForwardingBackend)
	b := &messagesBackend{Backend: base}
	type callerKey struct{}
	ctx := context.WithValue(t.Context(), callerKey{}, "original")
	if verdict, err := b.AwaitPermissionQuestions(ctx, []string{"q1"}); err != nil || verdict != "allowed" || base.calls != 1 || base.ctx.Value(callerKey{}) != "original" {
		t.Fatal(verdict, err, base.calls)
	}
	a := new(agentToolAdmission)
	a.refuse(errors.New("enrollment revoked"), true)
	b.status = &messagesInboxStatus{admission: a}
	if verdict, err := b.AwaitPermissionQuestions(ctx, []string{"q1"}); err != nil || verdict != "waiting" || base.calls != 1 {
		t.Fatal("retired native binding reached question observer", verdict, err, base.calls)
	}
}
