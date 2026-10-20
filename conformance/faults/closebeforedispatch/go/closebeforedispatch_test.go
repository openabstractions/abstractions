package closebeforedispatch

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"

	acceptance "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

type recorder struct {
	mu     sync.Mutex
	frames [][]byte
	err    error
}

func (r *recorder) ExchangeFrame(frame []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, append([]byte(nil), frame...))
	return []byte(`{"ok":true}`), r.err
}
func request(method string) []byte {
	return []byte(`{"method":"` + method + `","arguments":{"key":"retained-before-send"}}`)
}

func TestCompleteRequestClosesBeforeDispatch(t *testing.T) {
	inner := &recorder{}
	fault := New(inner, "")
	frame := request("Submit")
	reply, err := fault.ExchangeFrame(frame)
	if !errors.Is(err, io.EOF) || reply != nil {
		t.Fatalf("caller reply=%q error=%v; want real reply-read EOF", reply, err)
	}
	if fired, n := fault.Fired(); !fired || n != len(frame) {
		t.Fatalf("complete request: fired=%v bytes=%d want=%d", fired, n, len(frame))
	}
	if len(inner.frames) != 0 {
		t.Fatalf("service callback dispatched %d times", len(inner.frames))
	}
	// Inspect before any second call: the helper sent no automatic retry.
	if reply, err := fault.ExchangeFrame(frame); err != nil || len(reply) == 0 {
		t.Fatalf("explicit next call: %q %v", reply, err)
	}
	if len(inner.frames) != 1 || !bytes.Equal(inner.frames[0], frame) {
		t.Fatal("next request was not passed through exactly once")
	}
}

func TestOtherAndMalformedRequestsPassThrough(t *testing.T) {
	inner := &recorder{}
	fault := New(inner, "Submit")
	for _, frame := range [][]byte{request("Observe"), []byte(`not json "method":"Submit"`), {0, '\n', 255}} {
		if _, err := fault.ExchangeFrame(frame); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(inner.frames[len(inner.frames)-1], frame) {
			t.Fatal("payload changed")
		}
	}
	if fired, _ := fault.Fired(); fired {
		t.Fatal("unselected request fired")
	}
}

func TestTruncatedRequestNeverFiresOrDispatches(t *testing.T) {
	for _, raw := range [][]byte{{0, 0}, {0, 0, 0, 8, 'x', 'y'}} {
		inner := &recorder{}
		fault := New(inner, "Submit")
		client, server := net.Pipe()
		done := make(chan error, 1)
		go func() { done <- fault.serve(server) }()
		if _, err := client.Write(raw); err != nil {
			t.Fatal(err)
		}
		_ = client.Close()
		if err := <-done; !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("truncated request error=%v", err)
		}
		if fired, _ := fault.Fired(); fired || len(inner.frames) != 0 {
			t.Fatal("truncation counted as complete-request fault")
		}
	}
}

func TestOversizeAndCallbackFailure(t *testing.T) {
	inner := &recorder{err: errors.New("callback failure")}
	fault := New(inner, "Submit")
	if _, err := fault.ExchangeFrame(make([]byte, maxFrameBytes+1)); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err := fault.ExchangeFrame(request("Observe")); !errors.Is(err, inner.err) {
		t.Fatalf("callback error=%v", err)
	}
	if fired, _ := fault.Fired(); fired {
		t.Fatal("fault fired without selected complete frame")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], maxFrameBytes+1)
	if _, err := readFrame(bytes.NewReader(header[:])); err == nil {
		t.Fatal("oversize incoming header accepted")
	}
}

func TestFiresOnceUnderConcurrency(t *testing.T) {
	inner := &recorder{}
	fault := New(inner, "Submit")
	var wg sync.WaitGroup
	var mu sync.Mutex
	lost := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := fault.ExchangeFrame(request("Submit"))
			if errors.Is(err, io.EOF) {
				mu.Lock()
				lost++
				mu.Unlock()
			} else if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if lost != 1 || len(inner.frames) != 15 {
		t.Fatalf("lost=%d dispatches=%d", lost, len(inner.frames))
	}
}

type generatedHistoryService struct {
	observations int
	unexpected   []string
}

func (s *generatedHistoryService) GetHistoryWindow() (acceptance.HistoryWindow, error) {
	s.observations++
	return acceptance.HistoryWindow{LogicalOwner: "fixture-owner", HistoryEpoch: "fixture-epoch", MinimumRetentionMs: 60000}, nil
}
func (s *generatedHistoryService) Submit(acceptance.Submission) (acceptance.AcceptanceResult, error) {
	s.unexpected = append(s.unexpected, "Submit")
	return acceptance.AcceptanceResult{}, errors.New("unexpected fixture Submit callback")
}
func (s *generatedHistoryService) Reconcile(acceptance.RequestIdentity) (acceptance.AcceptanceResult, error) {
	s.unexpected = append(s.unexpected, "Reconcile")
	return acceptance.AcceptanceResult{}, errors.New("unexpected fixture Reconcile callback")
}
func (s *generatedHistoryService) CancelWork(acceptance.RequestIdentity) (acceptance.CancellationResult, error) {
	s.unexpected = append(s.unexpected, "CancelWork")
	return acceptance.CancellationResult{}, errors.New("unexpected fixture CancelWork callback")
}

type countedGeneratedTransport struct {
	inner  Exchanger
	frames [][]byte
}

func (t *countedGeneratedTransport) ExchangeFrame(frame []byte) ([]byte, error) {
	t.frames = append(t.frames, append([]byte(nil), frame...))
	return t.inner.ExchangeFrame(frame)
}

func TestGeneratedClientUnknownWithoutDispatchOrRetry(t *testing.T) {
	service := &generatedHistoryService{}
	dispatcher := &acceptance.RecoverableAcceptanceDispatcher{Handler: service}
	fault := New(dispatcher, "GetHistoryWindow")
	transport := &countedGeneratedTransport{inner: fault}
	client := acceptance.NewRecoverableAcceptanceClient(transport)
	result, err := client.GetHistoryWindow()
	if !errors.Is(err, io.EOF) || result != (acceptance.HistoryWindow{}) {
		t.Fatalf("unknown reply: %+v %v", result, err)
	}
	if len(transport.frames) != 1 || service.observations != 0 {
		t.Fatalf("client attempts=%d dispatches=%d", len(transport.frames), service.observations)
	}
	var frame struct {
		Version int    `json:"version"`
		Service string `json:"service"`
		Method  string `json:"method"`
	}
	if err := json.Unmarshal(transport.frames[0], &frame); err != nil || frame.Version != 1 || frame.Service != "abstraction.job/acceptance@1" || frame.Method != "GetHistoryWindow" {
		t.Fatalf("actual generated frame: %+v %v", frame, err)
	}
	if fired, n := fault.Fired(); !fired || n != len(transport.frames[0]) {
		t.Fatalf("complete generated payload: fired=%v bytes=%d", fired, n)
	}
	// The caller chooses this second readonly call. Neither generated client
	// nor fault transport repeated it after the uncertain first result.
	result, err = client.GetHistoryWindow()
	if err != nil || result.LogicalOwner != "fixture-owner" || result.HistoryEpoch != "fixture-epoch" || result.MinimumRetentionMs != 60000 {
		t.Fatalf("explicit readonly repeat: %+v %v", result, err)
	}
	if len(transport.frames) != 2 || service.observations != 1 || !bytes.Equal(transport.frames[0], transport.frames[1]) {
		t.Fatalf("attempts=%d dispatches=%d", len(transport.frames), service.observations)
	}
	if len(service.unexpected) != 0 {
		t.Fatal("readonly call reached unexpected callbacks", service.unexpected)
	}
	// Unexpected methods must return through the actual dispatcher and pipe.
	// A fixture callback must never Goexit its service goroutine and strand
	// ExchangeFrame waiting for its completion notification.
	id := acceptance.RequestIdentity{Key: "fixture-key", HistoryEpoch: "fixture-epoch"}
	for _, call := range []func() error{
		func() error {
			_, err := client.Submit(acceptance.Submission{Identity: id, Kind: "download", Spec: []byte("opaque"), RequiredGuarantees: []string{"reconcile@1"}})
			return err
		},
		func() error { _, err := client.Reconcile(id); return err },
		func() error { _, err := client.CancelWork(id); return err },
	} {
		if err := call(); err == nil {
			t.Fatal("unexpected callback failure was hidden")
		}
	}
	if len(transport.frames) != 5 || len(service.unexpected) != 3 || service.unexpected[0] != "Submit" || service.unexpected[1] != "Reconcile" || service.unexpected[2] != "CancelWork" {
		t.Fatal("unexpected callback controls did not complete exactly once", service.unexpected)
	}
}
