package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	rwire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
)

// Two askers queueing on one lease (TODO.md resource §6 remainder;
// research/resources/COMFY-CARD-2026-09-22.md "What this does not prove":
// "A second render waiting on the first. Only one ComfyUI asked. Two askers
// queueing behind one lease is untested."). This is a fixture-pool test
// against the same resourceservice.Book the isolated runtime composes
// (serve/runtime_resources.go composeResources), with a fixture Instrument in
// place of the real platform counters, so the pool, what is held and what a
// yield frees are all this test's own arithmetic rather than this machine's
// GPU. It needs no isolated runtime process: Book.Acquire, Observe and Answer
// are the same calls composeResources wires to the leases@1 RPC endpoint
// (leases_host.go), and a Subject is just the struct the wire binds a caller
// to (SubjectFromPeer) — a fixture can supply two of them directly without
// two real OS processes.

const askersGiB = int64(1) << 30

// askerFixture is a fixture instrument: a fixed capacity and whatever samples
// the test sets, standing in for instrument.Detect() so a serve-level test can
// drive two askers deterministically.
type askerFixture struct {
	mu       sync.Mutex
	capacity int64
	samples  map[string]int64
	at       time.Time
}

func newAskerFixture(capacity int64) *askerFixture {
	return &askerFixture{capacity: capacity, samples: map[string]int64{}, at: time.Now()}
}

func (f *askerFixture) Name() string          { return instrument.NameNone }
func (f *askerFixture) Resources() []string   { return []string{instrument.Card0} }
func (f *askerFixture) Read(resource string) (instrument.Reading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reading := instrument.Reading{Resource: resource, At: f.at}
	if resource != instrument.Card0 {
		return reading, nil
	}
	reading.Capacity = f.capacity
	i := 0
	for program, amount := range f.samples {
		if amount <= 0 {
			continue
		}
		i++
		reading.Samples = append(reading.Samples, instrument.Sample{
			PID: i, Program: program, Account: "S-1-5-fixture", Amount: amount})
	}
	return reading, nil
}

// occupy sets what the fixture reports a program's process holds now, the way
// a holder that has loaded its granted bytes shows up as a verified row
// (CONTRACT.md RES-T5). Zero drops the row, the way a yield does.
func (f *askerFixture) occupy(program string, amount int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.samples[program] = amount
	f.at = time.Now()
}

// askerBook composes a Book the way composeResources does, over the fixture
// instrument instead of instrument.Detect(). Hold and Yield are left nil:
// BookOptions documents that as every bound caller may hold, which is what a
// fixture with no rights policy of its own needs (matching cardBook in
// leases_test.go and TestTheLeaseBookLivesInTheRuntimeState's unpolicied
// book).
func askerBook(t *testing.T, in *askerFixture, attached ...resourceservice.Attached) *resourceservice.Book {
	t.Helper()
	table := resourceservice.NewTable(in, nil, time.Nanosecond)
	book, err := resourceservice.OpenBook(resourceservice.BookOptions{
		Table: table, Attached: attached, Settle: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return book
}

func askerSubject(name string) resourceservice.Subject {
	return resourceservice.Subject{Program: `C:\fixture\` + name + `.exe`, Account: "S-1-5-fixture-" + name}
}

// yieldOnObserve runs one holder's side of leases@1: it waits for a yield
// request addressed to it, waits delay to simulate a holder that takes a
// moment to let go, then drops its occupied bytes and answers. A holder that
// refuses answers refused instead and keeps its bytes.
func yieldOnObserve(t *testing.T, book *resourceservice.Book, in *askerFixture, who resourceservice.Subject, delay time.Duration, refuseWith string) <-chan rwire.YieldRequest {
	t.Helper()
	seen := make(chan rwire.YieldRequest, 1)
	go func() {
		page, err := book.Observe(context.Background(), who, "", 10000)
		if err != nil || len(page.Requests) == 0 {
			close(seen)
			return
		}
		request := page.Requests[0]
		time.Sleep(delay)
		if refuseWith != "" {
			book.Answer(who, request.ID, rwire.YieldAnswerRefused, refuseWith)
		} else {
			in.occupy(who.Program, 0)
			book.Answer(who, request.ID, rwire.YieldAnswerYielded, "")
		}
		seen <- request
		close(seen)
	}()
	return seen
}

// Asker A holds a lease for most of the pool and occupies it, so it is a
// verified row as well as a claimed one (RES-T5). Asker B asks for more than
// is free; the service asks A, which implements leases@1 and, after a delay,
// answers yielded; B is granted once the instrument confirms the bytes left
// (RES-L3).
func TestTwoAskersOneYieldsAfterADelay(t *testing.T) {
	in := newAskerFixture(120 * askersGiB)
	book := askerBook(t, in)
	a, b := askerSubject("a"), askerSubject("b")

	granted := book.Acquire(context.Background(), a, nil, instrument.Card0, 100*askersGiB, 0)
	if granted.Outcome != rwire.AcquireOutcomeAcquired {
		t.Fatalf("A's own acquire read %s", granted.Outcome)
	}
	in.occupy(a.Program, 100*askersGiB)

	seen := yieldOnObserve(t, book, in, a, 200*time.Millisecond, "")

	began := time.Now()
	result := book.Acquire(context.Background(), b, nil, instrument.Card0, 60*askersGiB, 5000)
	took := time.Since(began)
	<-seen

	if result.Outcome != rwire.AcquireOutcomeAcquired {
		t.Fatalf("B's ask after A yields read %s: %+v", result.Outcome, result.Asked)
	}
	if took < 200*time.Millisecond {
		t.Fatalf("B was granted in %s, before A's delayed answer could have landed", took)
	}
	if len(result.Asked) != 1 || result.Asked[0].Holder != a.Program || result.Asked[0].Answer != resourceservice.AnswerYielded {
		t.Fatalf("asked %+v; want one yielded ask naming A", result.Asked)
	}
	if result.Asked[0].Amount != 100*askersGiB {
		t.Fatalf("the ask recorded %d bytes freed, not the %d the instrument showed leaving", result.Asked[0].Amount, 100*askersGiB)
	}
	for _, live := range book.Leases() {
		if live.ID == granted.Lease.ID {
			t.Fatal("A yielded and still holds a lease")
		}
	}
}

// The same setup, except A's own leases@1 answers refused: B reads
// holders_refused, and the record of the ask names A (CONTRACT.md RES-L2's
// holders_refused, RES-L3's "the refusal reaches asked naming that host" —
// the same naming for a lease holder as for an attached one).
func TestTwoAskersOneRefuses(t *testing.T) {
	in := newAskerFixture(120 * askersGiB)
	book := askerBook(t, in)
	a, b := askerSubject("a"), askerSubject("b")

	granted := book.Acquire(context.Background(), a, nil, instrument.Card0, 100*askersGiB, 0)
	if granted.Outcome != rwire.AcquireOutcomeAcquired {
		t.Fatalf("A's own acquire read %s", granted.Outcome)
	}
	in.occupy(a.Program, 100*askersGiB)

	seen := yieldOnObserve(t, book, in, a, 0, "the render is still using this")

	result := book.Acquire(context.Background(), b, nil, instrument.Card0, 60*askersGiB, 5000)
	<-seen

	if result.Outcome != rwire.AcquireOutcomeHoldersRefused {
		t.Fatalf("B's ask after A refuses read %s, want holders_refused: %+v", result.Outcome, result.Asked)
	}
	if result.Lease != nil {
		t.Fatal("a refused asker was given a lease")
	}
	if len(result.Asked) != 1 {
		t.Fatalf("asked %+v", result.Asked)
	}
	if result.Asked[0].Holder != a.Program {
		t.Fatalf("the refusal does not name A: %+v", result.Asked[0])
	}
	if !strings.HasPrefix(result.Asked[0].Answer, resourceservice.AnswerRefused) || !strings.Contains(result.Asked[0].Answer, "the render is still using this") {
		t.Fatalf("the refusal lost its reason: %q", result.Asked[0].Answer)
	}
	for _, live := range book.Leases() {
		if live.ID == granted.Lease.ID && live.Amount != 100*askersGiB {
			t.Fatalf("A's refused lease changed: %+v", live)
		}
	}
}

// A and B ask at the same moment, both needing the same one lease to fit: the
// pool cannot satisfy both requests even after the holder yields everything,
// so the contract's order — one asker's own ask loop, run to the holder it
// found waiting (RES-L2) — has to decide exactly one winner despite the two
// Acquire calls running concurrently against the same book. Nothing may be
// granted twice from the same freed bytes.
//
// What RES-L2 does not say: two asks that arrive together and both exhaust
// what one yield frees are not both satisfied from it. A proposed sentence:
// "A yield frees bytes once; where two asks are waiting on the same holder,
// only the ask whose own free() check next finds them is granted, and the
// other asks again."
func TestTwoAskersAtTheSameMomentOnlyOneWins(t *testing.T) {
	in := newAskerFixture(100 * askersGiB)
	book := askerBook(t, in)
	holder, x, y := askerSubject("holder"), askerSubject("x"), askerSubject("y")

	granted := book.Acquire(context.Background(), holder, nil, instrument.Card0, 70*askersGiB, 0)
	if granted.Outcome != rwire.AcquireOutcomeAcquired {
		t.Fatalf("holder's own acquire read %s", granted.Outcome)
	}
	in.occupy(holder.Program, 70*askersGiB)
	// Free is 30 GiB; each asker wants 60, and even after the holder's whole
	// 70 GiB is freed the pool is 100, so 60+60 does not both fit: at most one
	// of x and y can be acquired.

	var ready sync.WaitGroup
	ready.Add(1)
	start := make(chan struct{})
	go func() {
		defer ready.Done()
		<-start
		page, err := book.Observe(context.Background(), holder, "", 10000)
		if err != nil {
			return
		}
		for _, request := range page.Requests {
			book.Answer(holder, request.ID, rwire.YieldAnswerYielded, "")
		}
		in.occupy(holder.Program, 0)
		// A second asker's question may still be waiting once the first is
		// answered; the holder answers whatever it is shown next, mirroring a
		// real leases@1 client that observes again after one answer.
		for i := 0; i < 4; i++ {
			page, err := book.Observe(context.Background(), holder, page.Next, 500)
			if err != nil || len(page.Requests) == 0 {
				continue
			}
			for _, request := range page.Requests {
				book.Answer(holder, request.ID, rwire.YieldAnswerYielded, "")
			}
		}
	}()

	var results [2]rwire.AcquireResult
	var run sync.WaitGroup
	run.Add(2)
	go func() {
		defer run.Done()
		<-start
		results[0] = book.Acquire(context.Background(), x, nil, instrument.Card0, 60*askersGiB, 4000)
	}()
	go func() {
		defer run.Done()
		<-start
		results[1] = book.Acquire(context.Background(), y, nil, instrument.Card0, 60*askersGiB, 4000)
	}()
	close(start)
	run.Wait()
	ready.Wait()

	acquired, refused := 0, 0
	var winner *rwire.AcquireResult
	for i := range results {
		switch results[i].Outcome {
		case rwire.AcquireOutcomeAcquired:
			acquired++
			winner = &results[i]
		case rwire.AcquireOutcomeHoldersRefused:
			refused++
		default:
			t.Fatalf("outcome %s, want acquired or holders_refused", results[i].Outcome)
		}
	}
	if acquired != 1 || refused != 1 {
		t.Fatalf("outcomes %+v; want exactly one acquired and one holders_refused, never both and never neither", results)
	}
	// The book holds exactly the winner's grant: the holder's own lease was
	// released when it yielded, and the loser was never written one.
	live := book.Leases()
	if len(live) != 1 || live[0].ID != winner.Lease.ID || live[0].Amount != 60*askersGiB {
		t.Fatalf("the book holds %+v after the race, want exactly the winner's own %d-byte grant", live, 60*askersGiB)
	}
}

// A rights-less book leaves every subject free to hold, so an attached holder
// and a leased holder are asked in the order RES-L2 fixes: attached idle
// longest first, then leases. This is composeResources's own attachedHosts
// wired through a fixture instead of a real router host, proving the order
// still governs a resource with both kinds of holder at once.
func TestAttachedThenLeaseIsTheOrderEvenWithBothPresent(t *testing.T) {
	in := newAskerFixture(120 * askersGiB)
	engine := &askerAttached{name: "fixture-engine", holding: []string{"modelH"}, in: in, frees: 20 * askersGiB}
	book := askerBook(t, in, engine)
	holder, asker := askerSubject("holder"), askerSubject("asker")

	granted := book.Acquire(context.Background(), holder, nil, instrument.Card0, 80*askersGiB, 0)
	if granted.Outcome != rwire.AcquireOutcomeAcquired {
		t.Fatalf("holder's own acquire read %s", granted.Outcome)
	}
	in.occupy(holder.Program, 80*askersGiB)
	in.occupy(engine.Holder(), 20*askersGiB)
	// held = 100, pool = 120, free = 20; asker wants 90, which needs both.

	seen := yieldOnObserve(t, book, in, holder, 0, "")
	result := book.Acquire(context.Background(), asker, nil, instrument.Card0, 90*askersGiB, 5000)
	<-seen

	if result.Outcome != rwire.AcquireOutcomeAcquired {
		t.Fatalf("outcome %s after %+v", result.Outcome, result.Asked)
	}
	if len(result.Asked) != 2 {
		t.Fatalf("asked %+v, want the attached engine then the lease", result.Asked)
	}
	if result.Asked[0].Holder != engine.Holder() {
		t.Fatalf("asked %+v first, want the attached holder first (RES-L2)", result.Asked[0])
	}
	if result.Asked[1].Holder != holder.Program {
		t.Fatalf("asked %+v second, want the lease second (RES-L2)", result.Asked[1])
	}
}

// askerAttached is a fixture attached holder for
// TestAttachedThenLeaseIsTheOrderEvenWithBothPresent: a host predating OA,
// yielded synchronously through its own mechanism (CONTRACT.md RES-L3), with
// no rights policy gating it, matching this file's other fixture books.
type askerAttached struct {
	name    string
	holding []string
	frees   int64
	in      *askerFixture
}

func (a *askerAttached) Holder() string   { return "host:" + a.name }
func (a *askerAttached) Resource() string { return instrument.Card0 }
func (a *askerAttached) Holding(context.Context) ([]string, error) {
	return a.holding, nil
}
func (a *askerAttached) Yield(context.Context) error {
	a.in.occupy(a.Holder(), 0)
	a.holding = nil
	return nil
}
