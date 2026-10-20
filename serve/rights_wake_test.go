package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-identity/go/privatedir/privatetest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rights "github.com/openabstractions/abstraction-rights/go"
)

type wakeApproval struct{}

func (wakeApproval) Await(context.Context, rights.Question) (rights.Decision, error) {
	return rights.Decision{Yes: true}, nil
}

func TestLegacyRightsBridgeUsesOriginalSubjectAndResourceLifetime(t *testing.T) {
	var taken, released atomic.Int32
	book, err := resourceservice.OpenBook(resourceservice.BookOptions{Table: resourceservice.NewTable(nil, nil, 0),
		KeepSystemAwake: func(string) (func(), error) { taken.Add(1); return func() { released.Add(1) }, nil }})
	if err != nil {
		t.Fatal(err)
	}
	dir := privatetest.Dir(t)
	at := rightsAsksEndpoint(t, dir, "rights-wake")
	s, err := rights.StartWithWake(at, dir, wakeApproval{}, func() (rights.WakeKeeper, error) { return legacyWakeBook{book}, nil })
	if runtime.GOOS == "darwin" && errors.Is(err, identity.ErrNotProven) {
		if taken.Load() != 0 || released.Load() != 0 {
			t.Fatal("proof refusal touched the resource lease")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	admin, err := os.ReadFile(filepath.Join(dir, "admin.secret"))
	if err != nil {
		t.Fatal(err)
	}
	app := &rights.Client{Endpoint: at}
	secret, err := app.Register("downloader", rights.RightAwake)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := app.Ask(secret, rights.RightAwake)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := app.Hold(token, "download")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	tool := &rights.Client{Endpoint: at, Admin: string(admin)}
	holds, err := tool.Holds()
	if err != nil || len(holds) != 1 {
		t.Fatal(holds, err)
	}
	rows := book.AwakeHolds()
	if len(rows) != 1 || rows[0].Program != holds[0].Seen.Path || rows[0].Account != holds[0].Seen.User || rows[0].Why != "download" || rows[0].Lease != holds[0].Lease {
		t.Fatal("original subject changed", rows, holds)
	}
	if err := tool.Revoke("downloader", rights.RightAwake); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lease.Done():
	case <-time.After(time.Second):
		t.Fatal("revoke retained connection")
	}
	for until := time.Now().Add(time.Second); released.Load() == 0 && time.Now().Before(until); {
		time.Sleep(time.Millisecond)
	}
	if taken.Load() != 1 || released.Load() != 1 || len(book.AwakeHolds()) != 0 {
		t.Fatal(taken.Load(), released.Load(), book.AwakeHolds())
	}
}
