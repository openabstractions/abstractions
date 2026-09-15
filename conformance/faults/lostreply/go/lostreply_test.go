package lostreply

import (
	"errors"
	"io"
	"sync"
	"testing"
)

type recorder struct {
	mu     sync.Mutex
	frames []string
	fail   error
	writes int
}

func (r *recorder) ExchangeFrame(frame []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, string(frame))
	if r.fail != nil {
		return nil, r.fail
	}
	return []byte(`{"version":1,"ok":true,"payload":{"value":{"outcome":"accepted"}}}`), nil
}

func (r *recorder) WriteFrame([]byte) error { r.writes++; return nil }

func frame(method, argument string) []byte {
	return []byte(`{"version":1,"service":"abstraction.job/acceptance@1","method":"` + method + `","arguments":{"key":"` + argument + `"}}`)
}

func TestLosesOneCommittedReply(t *testing.T) {
	inner := &recorder{}
	fault := New(inner, "")
	if _, err := fault.ExchangeFrame(frame("GetHistoryWindow", "Submit")); err != nil {
		t.Fatalf("other method affected: %v", err)
	}
	reply, err := fault.ExchangeFrame(frame("Submit", "k"))
	if !errors.Is(err, ErrLostReply) || !errors.Is(err, io.ErrUnexpectedEOF) || reply != nil {
		t.Fatalf("first Submit: reply %q err %v", reply, err)
	}
	if fired, discarded := fault.Fired(); !fired || discarded == 0 {
		t.Fatalf("fired=%v discarded=%d", fired, discarded)
	}
	if len(inner.frames) != 2 {
		t.Fatalf("the lost request was not delivered exactly once: %d frames", len(inner.frames))
	}
	if reply, err := fault.ExchangeFrame(frame("Submit", "k")); err != nil || len(reply) == 0 {
		t.Fatalf("fault fired twice: %q %v", reply, err)
	}
	if err := fault.WriteFrame(frame("Write", "")); err != nil || inner.writes != 1 {
		t.Fatalf("one-way frame not forwarded: %v", err)
	}
}

func TestTransportErrorsPassThroughWithoutFiring(t *testing.T) {
	broken := errors.New("dial failed")
	fault := New(&recorder{fail: broken}, "Submit")
	if _, err := fault.ExchangeFrame(frame("Submit", "k")); !errors.Is(err, broken) {
		t.Fatalf("transport error replaced: %v", err)
	}
	if fired, _ := fault.Fired(); fired {
		t.Fatal("fault fired without a committed reply")
	}
}

func TestMalformedFrameIsForwardedUnchanged(t *testing.T) {
	fault := New(&recorder{}, "Submit")
	if _, err := fault.ExchangeFrame([]byte(`not json "method":"Submit"`)); err != nil {
		t.Fatalf("malformed frame matched: %v", err)
	}
}

func TestFiresOnceUnderConcurrency(t *testing.T) {
	fault := New(&recorder{}, "Submit")
	var wg sync.WaitGroup
	var mu sync.Mutex
	lost := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := fault.ExchangeFrame(frame("Submit", "k")); errors.Is(err, ErrLostReply) {
				mu.Lock()
				lost++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if lost != 1 {
		t.Fatalf("lost %d replies, want 1", lost)
	}
}
