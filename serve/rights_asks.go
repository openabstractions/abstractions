package main

import (
	"context"

	asks "github.com/openabstractions/abstraction-asks/go"
	job "github.com/openabstractions/abstraction-job/go"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
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
	return rights.ServeWithWake(args, asksAt, asks.DefaultEndpoint(), legacyWakeFactory)
}

type legacyWakeBook struct{ book *resourceservice.Book }

func (b legacyWakeBook) KeepAwake(program, account, why string) (string, func(), error) {
	return b.book.HoldAwakeChecked(resourceservice.Subject{Program: program, Account: account}, why)
}

func legacyWakeFactory() (rights.WakeKeeper, error) {
	book, err := resourceservice.OpenBook(resourceservice.BookOptions{
		Table: resourceservice.NewTable(nil, nil, 0), KeepSystemAwake: keepAwake(job.Platform),
	})
	if err != nil {
		return nil, err
	}
	return legacyWakeBook{book}, nil
}
