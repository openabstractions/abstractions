package main

import (
	"context"

	asks "github.com/openabstractions/abstraction-asks/go"
	rights "github.com/openabstractions/abstraction-rights/go"
)

// asksQuestions puts the rights service's registration questions to the asks
// service. asks depends on rights, so the composition joins the two here and
// rights names only the Asker it needs.
type asksQuestions struct{ client *asks.Client }

func asksAt(endpoint string) rights.Asker {
	return asksQuestions{client: &asks.Client{Endpoint: endpoint}}
}

func (q asksQuestions) Await(ctx context.Context, question rights.Question) (rights.Decision, error) {
	a, err := q.client.Await(ctx, asks.Ask{Asker: question.Asker, Key: question.Key,
		Slots: question.Slots, For: question.For})
	return rights.Decision{Yes: a.Yes, Kept: a.Kept}, err
}

// serveRights is `serve rights`: the rights service, asking through asks.
func serveRights(args []string) error {
	return rights.Serve(args, asksAt, asks.DefaultEndpoint())
}
