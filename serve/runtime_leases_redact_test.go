package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go-core/redact/redacttest"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

// An engine that refuses to let go can say why in its reply, and the lease
// service answers the program asking for the card "refused: <reason>". The
// reason passes through redact.Line in attachedHost.Yield: for every row of
// the bypass table as the engine's reply, the refused answer carries none of
// the table's secrets.
func TestAnEnginesUnloadRefusalReachesTheAskerRedacted(t *testing.T) {
	comfyPython := `C:\AI_Bundle\ComfyUI\venv\Scripts\python.exe`
	for _, c := range redacttest.Cases {
		in := newAskerFixture(200 * askersGiB)
		table := comfyAttachedTable(in, comfyPython, 40*askersGiB)
		comfy := &comfyAttachedFixture{in: in, program: comfyPython, refuse: c.In}
		comfy.setHeld(40 * askersGiB)
		server := comfy.server(t)
		r := router.New(router.ComfyUI(server.URL))
		r.SetResidency(tableResidency{table: table})
		book, err := resourceservice.OpenBook(resourceservice.BookOptions{
			Table: table, Attached: attachedHosts(r), Settle: time.Second,
			Yield: func(context.Context, string) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		result := book.Acquire(context.Background(), askerSubject("someone-else"), nil, instrument.Card0, 180*askersGiB, 2000)
		if !comfy.called() || len(result.Asked) != 1 {
			t.Fatalf("the engine was not asked to yield: %+v", result.Asked)
		}
		answer := result.Asked[0].Answer
		if !strings.HasPrefix(answer, resourceservice.AnswerRefused) {
			t.Fatalf("the engine's refusal answered %q", answer)
		}
		for _, secret := range redacttest.Secrets {
			if strings.Contains(answer, secret) {
				t.Errorf("the refusal of %q reached the asker as %q, keeping %q", c.In, answer, secret)
			}
		}
	}
}
