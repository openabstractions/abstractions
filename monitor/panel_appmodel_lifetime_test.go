package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type panelBlockedOutput struct {
	io.WriteCloser
	entered chan struct{}
	once    sync.Once
}

func (p *panelBlockedOutput) Write(body []byte) (int, error) {
	p.once.Do(func() { close(p.entered) })
	return p.WriteCloser.Write(body)
}

type panelOutputModel struct{}

func (*panelOutputModel) Receive([]byte) ([]panelNativeActivation, [][]byte, error) {
	return nil, [][]byte{[]byte("canonical native output")}, nil
}
func (*panelOutputModel) Complete(string, []byte) ([]panelNativeActivation, [][]byte, error) {
	return nil, nil, nil
}
func (*panelOutputModel) Fault(string) ([]panelNativeActivation, [][]byte, error) {
	return nil, nil, nil
}
func (*panelOutputModel) Retire() error { return nil }
func (*panelOutputModel) Close() error  { return nil }

func TestPanelNativeBlockedOutputRetiresAndJoins(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	output := &panelBlockedOutput{WriteCloser: writer, entered: make(chan struct{})}
	frames := newPanelOwnedFrames(io.NopCloser(bytes.NewReader(nil)), output, nil)
	if err := frames.attachModel(&panelOutputModel{}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- frames.receive([]byte("input")) }()
	select {
	case <-output.entered:
	case <-time.After(time.Second):
		t.Fatal("controlled write did not start")
	}
	retired := make(chan struct{})
	go func() { frames.retire(); close(retired) }()
	select {
	case <-retired:
	case <-time.After(time.Second):
		_ = writer.Close() // settle the owned negative without leaving a blocked worker
		<-done
		<-retired
		t.Fatal("retirement waited for the lock held by blocked output")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed output reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("original writer did not join")
	}
	if err := frames.close(); err != nil {
		t.Fatal(err)
	}
}

type panelCompletionWriter struct {
	mu       sync.Mutex
	waiting  map[string]chan struct{}
	observed [][]byte
}

func (p *panelCompletionWriter) Write(body []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observed = append(p.observed, append([]byte(nil), body...))
	if bytes.Contains(body, []byte(`"kind":"owned-core-complete"`)) || bytes.Contains(body, []byte(`"kind":"owned-core-fault"`)) {
		for id, ready := range p.waiting {
			if bytes.Contains(body, []byte(`"id":"`+id+`"`)) {
				close(ready)
				delete(p.waiting, id)
			}
		}
	}
	return len(body), nil
}

func TestPanelNativeFaultUsesOriginalChildAck(t *testing.T) {
	started := make(chan struct{})
	writer := &panelCompletionWriter{waiting: map[string]chan struct{}{"1": started}}
	bridge := newNativeAsksBridge(context.Background(), writer)
	child := newNativePanelOwnedChild(bridge)
	bridge.owned = child
	manifest, err := panelOwnedResources.ReadFile("panel-application.json")
	if err != nil {
		t.Fatal(err)
	}
	child.model, err = newPanelChildModel(child, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = child.frames.attachModel(child.model); err != nil {
		t.Fatal(err)
	}
	if err = child.model.activation("1", "1", []byte(`{"page":"credentials"}`)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.frames.fault("1") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("fault did not reach original private child")
	}
	writer.mu.Lock()
	body := append([]byte(nil), writer.observed[0]...)
	writer.mu.Unlock()
	if !bytes.Contains(body, []byte(`"kind":"owned-core-fault"`)) || bytes.Contains(body, []byte(`"body"`)) {
		t.Fatalf("fault must be opaque call-only signal: %s", body)
	}
	select {
	case <-done:
		t.Fatal("fault returned before native acknowledgement")
	default:
	}
	if err = bridge.accept(nativeAsksReply{Kind: "owned-core-completed", ID: "1"}, nil); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = child.frames.fault("1"); err == nil {
		t.Fatal("duplicate callback was forwarded")
	}
	if err = bridge.accept(nativeAsksReply{Kind: "owned-core-completed", ID: "1"}, nil); err == nil {
		t.Fatal("stale acknowledgement accepted")
	}
	if err = child.frames.close(); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}

func TestPanelNativePriorActivationCannotBlockCompletionAck(t *testing.T) {
	first, second := make(chan struct{}), make(chan struct{})
	writer := &panelCompletionWriter{waiting: map[string]chan struct{}{"1": first, "2": second}}
	bridge := newNativeAsksBridge(context.Background(), writer)
	child := newNativePanelOwnedChild(bridge)
	bridge.owned = child
	manifest, err := panelOwnedResources.ReadFile("panel-application.json")
	if err != nil {
		t.Fatal(err)
	}
	child.model, err = newPanelChildModel(child, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.frames.attachModel(child.model); err != nil {
		t.Fatal(err)
	}
	activation := func(id string) error {
		return bridge.accept(nativeAsksReply{Kind: "owned-core-activation", ID: id, Args: "1", Body: base64.StdEncoding.EncodeToString([]byte(`{"page":"credentials"}`))}, nil)
	}
	if err := activation("1"); err != nil {
		t.Fatal(err)
	}
	completion := make(chan error, 1)
	go func() { completion <- child.frames.complete("1", []byte(`{"outcome":"applied","reason":""}`)) }()
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("controlled completion did not start")
	}
	reader := make(chan error, 1)
	go func() {
		if err := activation("2"); err != nil {
			reader <- err
			return
		}
		reader <- bridge.accept(nativeAsksReply{Kind: "owned-core-completed", ID: "1"}, nil)
	}()
	select {
	case err := <-completion:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		_ = child.model.acknowledge("1", "") // release this fixture's original waiter
		<-completion
		<-reader
		child.frames.retire()
		t.Fatal("prior activation blocked the sole acknowledgement reader")
	}
	if err := <-reader; err != nil {
		t.Fatal(err)
	}
	go func() {
		completion <- child.frames.complete("2", []byte(`{"outcome":"unavailable","reason":"fixture"}`))
	}()
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("second completion did not start")
	}
	if err := bridge.accept(nativeAsksReply{Kind: "owned-core-completed", ID: "2"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-completion; err != nil {
		t.Fatal(err)
	}
	if err := child.frames.close(); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}
