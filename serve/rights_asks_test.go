package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
)

// The rights service's registration question reaches the real asks service
// through the composition's Asker, and the person's answer comes back: "allow"
// registers, "never" refuses and stays refused without asking again. The
// rights package tests its own half against a stand-in; this is the joint.

func rightsAsksEndpoint(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "darwin" {
		short, err := os.MkdirTemp("/tmp", "rights-asks-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(short) })
		dir = short
	}
	if runtime.GOOS != "windows" {
		return filepath.Join(dir, name)
	}
	return fmt.Sprintf(`\\.\pipe\%s-serve-test-%d-%s`, name, os.Getpid(), t.Name())
}

func rightsWithAsks(t *testing.T) (app *rights.Client, person *asks.Client) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		if err := identity.CanEver(listen.Program); err != nil {
			t.Skipf("UNPROVEN: successful Program-bound service case on current macOS socket transport; requires native transport proof: %v", err)
		}
	}
	dir := t.TempDir()
	asksDir := filepath.Join(dir, "asks")
	asksEP := rightsAsksEndpoint(t, asksDir, "asks")
	a, err := asks.Start(asksEP, asksDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	rightsDir := filepath.Join(dir, "rights")
	at := rightsAsksEndpoint(t, rightsDir, "rights")
	s, err := rights.Start(at, rightsDir, asksAt(asksEP))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	personAdmin, _ := os.ReadFile(filepath.Join(asksDir, "admin.secret"))
	return &rights.Client{Endpoint: at}, &asks.Client{Endpoint: asksEP, Admin: string(personAdmin)}
}

func firstAsksQuestion(t *testing.T, person *asks.Client) asks.Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		rs, err := person.Pending()
		if err != nil {
			t.Fatal(err)
		}
		if len(rs) > 0 {
			return rs[0]
		}
		select {
		case <-ctx.Done():
			t.Fatal("no question reached asks")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRightsRegistrationIsAQuestionInAsks(t *testing.T) {
	app, person := rightsWithAsks(t)

	secret := make(chan string, 1)
	fail := make(chan error, 1)
	go func() {
		s, err := app.Register("demo", rights.RightAwake)
		if err != nil {
			fail <- err
			return
		}
		secret <- s
	}()
	q := firstAsksQuestion(t, person)
	if q.Asker != "demo" || q.Key != "rights.register" {
		t.Fatalf("the question asked was %+v", q)
	}
	if identity.Ceiling().Bindable && !q.Via.Bound {
		t.Fatalf("this machine can bind but the asker is not bound: %s", q.Via.Why)
	}
	if _, err := person.Answer(q.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-secret:
		if s == "" {
			t.Fatal("an allowed registration returned no secret")
		}
	case err := <-fail:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("register never returned")
	}

	refused := make(chan error, 1)
	go func() { _, err := app.Register("denied", rights.RightAwake); refused <- err }()
	q = firstAsksQuestion(t, person)
	if _, err := person.Answer(q.ID, "never"); err != nil {
		t.Fatal(err)
	}
	if err := <-refused; err == nil {
		t.Fatal("a refused application got a secret")
	}
	if _, err := app.Register("denied", rights.RightAwake); err == nil {
		t.Fatal("'never' did not hold on the second registration")
	}
	if ps, _ := person.Pending(); len(ps) != 0 {
		t.Fatalf("a question answered 'never' was asked again: %+v", ps)
	}
}
