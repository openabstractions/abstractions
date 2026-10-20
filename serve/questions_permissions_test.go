package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type permissionOperatorStub func(string, int64) (askswire.OperatorPage, error)

func (s permissionOperatorStub) ListPermissionsContext(_ context.Context, cursor string, limit int64) (askswire.OperatorPage, error) {
	return s(cursor, limit)
}

func TestQuestionsPermissionsListsPendingTypedRequests(t *testing.T) {
	pending := askswire.RecordMetadata{ID: "p1", Key: "rights.permission@2", About: "/apps/Playground A", Text: "OA permission request", Options: []string{"allow", "refuse"}}
	answered := askswire.RecordMetadata{ID: "p2", Key: "rights.permission@2", Option: "allow"}
	later := askswire.RecordMetadata{ID: "p3", Key: "rights.permission@2", About: "/apps/Playground B"}
	operator := permissionOperatorStub(func(cursor string, limit int64) (askswire.OperatorPage, error) {
		if limit != 64 {
			t.Fatalf("limit %d", limit)
		}
		if cursor == "" {
			return askswire.OperatorPage{Outcome: askswire.OperatorPageOutcomePage, Records: []askswire.RecordMetadata{pending, answered}, Next: "next"}, nil
		}
		return askswire.OperatorPage{Outcome: askswire.OperatorPageOutcomePage, Records: []askswire.RecordMetadata{later}, Complete: true}, nil
	})
	got, err := listPendingPermissions(context.Background(), operator)
	if err != nil || len(got) != 2 || got[0].ID != "p1" || got[1].ID != "p3" {
		t.Fatalf("permissions %+v, %v", got, err)
	}
	var out bytes.Buffer
	if err := printPendingPermissions(&out, got, false); err != nil || !strings.Contains(out.String(), "/apps/Playground A") || strings.Contains(out.String(), "p2") || !strings.Contains(out.String(), "permission dialog") {
		t.Fatalf("printed %q, %v", out.String(), err)
	}
	refused := permissionOperatorStub(func(string, int64) (askswire.OperatorPage, error) {
		return askswire.OperatorPage{Outcome: askswire.OperatorPageOutcomeForbidden}, nil
	})
	var exit *exitError
	if _, err := listPendingPermissions(context.Background(), refused); !errors.As(err, &exit) || exit.code != exitRefusedCall || !strings.Contains(err.Error(), "questions permissions: forbidden") {
		t.Fatalf("forbidden %v", err)
	}
}

// An empty list is an empty JSON array. The Mac journey read "records":null
// from pending and took it for a failed admission.
func TestQuestionsEmptyListsEncodeEmptyArrays(t *testing.T) {
	empty := permissionOperatorStub(func(string, int64) (askswire.OperatorPage, error) {
		return askswire.OperatorPage{Outcome: askswire.OperatorPageOutcomePage, Complete: true}, nil
	})
	records, err := listPendingPermissions(context.Background(), empty)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printPendingPermissions(&out, records, true); err != nil || strings.TrimSpace(out.String()) != `{"outcome":"page","records":[]}` {
		t.Fatalf("permissions JSON %q, %v", out.String(), err)
	}
	questions, err := listPendingQuestions(context.Background(), questionOperatorStub{list: func(string, int64) (askswire.OperatorPage, error) {
		return askswire.OperatorPage{Outcome: askswire.OperatorPageOutcomePage, Complete: true}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := printPendingQuestions(&out, questions, nil, true); err != nil || strings.TrimSpace(out.String()) != `{"outcome":"page","records":[]}` {
		t.Fatalf("pending JSON %q, %v", out.String(), err)
	}
}

// A refused raw Read admits a typed service request. The ordinary operator
// list omits it by contract; the permissions listing shows it. This is the
// query that found the Mac Ask the earlier journey reported missing.
func TestQuestionsPermissionsShowsServiceAdmittedRequest(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("fixture uses native non-XPC Program transport; no Mac proof claimed")
	}
	a, subject := typedRuntime(t)
	endpoint := listen.Endpoint(fmt.Sprintf("questions-permissions-%d-%d", os.Getpid(), time.Now().UnixNano()))
	h, err := asksservice.Listen(endpoint, a.book)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.EnablePermission(a.preparePermission); err != nil {
		t.Fatal(err)
	}
	if err = h.EnableOperator(func(context.Context, *identity.Peer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Serve(context.Background()) }()
	t.Cleanup(func() {
		h.Close()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(2 * time.Second):
			t.Error("asks host did not drain")
		}
	})
	selection := typedTestSelection(false)
	operation := rwire.ConcreteResource{Permission: selection.Permission, Target: selection.Selector.Target}
	if decision := a.rights.decideTypedAsking(context.Background(), subject, operation); decision.Outcome != rwire.BatchCheckOutcomeChecked || decision.Outcomes[0] != rwire.DecisionOutcomeNotGranted {
		t.Fatalf("decision %+v", decision)
	}
	ctx := context.Background()
	ordinary, err := listPendingQuestions(ctx, askclient.NewOperator(endpoint))
	if err != nil || len(ordinary) != 0 {
		t.Fatalf("ordinary list %+v, %v", ordinary, err)
	}
	typed, err := listPendingPermissions(ctx, askclient.NewPermissionOperator(endpoint))
	if err != nil || len(typed) != 1 || typed[0].Key != asks.TypedPermissionKey || typed[0].About != subject.Program {
		t.Fatalf("permissions list %+v, %v", typed, err)
	}
}
