package main

// The Mac private child projects bytes/tickets only. Its signed Swift parent
// owns the common native core; this adapter owns no MCP or schema decisions.
import (
	"encoding/base64"
	"errors"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"strconv"
	"sync"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
)

type panelChildModel struct {
	child    *nativePanelOwnedChild
	manifest registrationwire.Manifest
	mu       sync.Mutex
	waiting  map[string]chan error
	retired  bool
}

func newPanelChildModel(child *nativePanelOwnedChild, manifest []byte) (*panelChildModel, error) {
	p := &panelChildModel{child: child, waiting: map[string]chan error{}}
	parsed, err := registrationwire.DecodeManifest(manifest)
	if err != nil {
		return nil, err
	}
	p.manifest = *parsed
	return p, nil
}

func (p *panelChildModel) Receive(body []byte) ([]panelNativeActivation, [][]byte, error) {
	p.mu.Lock()
	retired := p.retired
	p.mu.Unlock()
	if retired {
		return nil, nil, errors.New("native parent mediation closed")
	}
	err := p.child.bridge.write(nativeAsksFrame{Kind: "owned-core-input", Body: base64.StdEncoding.EncodeToString(body)})
	return nil, nil, err
}

func (p *panelChildModel) Complete(id string, body []byte) ([]panelNativeActivation, [][]byte, error) {
	return p.finishCallback("owned-core-complete", id, body)
}

func (p *panelChildModel) Fault(id string) ([]panelNativeActivation, [][]byte, error) {
	return p.finishCallback("owned-core-fault", id, nil)
}

func (p *panelChildModel) finishCallback(kind, id string, body []byte) ([]panelNativeActivation, [][]byte, error) {
	if _, err := strconv.ParseUint(id, 16, 64); err != nil {
		return nil, nil, err
	}
	done := make(chan error, 1)
	p.mu.Lock()
	if _, exists := p.waiting[id]; exists {
		p.mu.Unlock()
		return nil, nil, errors.New("native completion already pending")
	}
	p.waiting[id] = done
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.waiting, id); p.mu.Unlock() }()
	if err := p.child.bridge.write(nativeAsksFrame{Kind: kind, ID: id, Body: base64.StdEncoding.EncodeToString(body)}); err != nil {
		return nil, nil, err
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		return nil, nil, err
	case <-p.child.bridge.ctx.Done():
		return nil, nil, p.child.bridge.ctx.Err()
	case <-timer.C:
		return nil, nil, errors.New("native completion acknowledgement unavailable")
	}
}

func (p *panelChildModel) acknowledge(id, failure string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	done := p.waiting[id]
	if done == nil {
		return errors.New("unknown native completion acknowledgement")
	}
	var err error
	if failure != "" {
		err = errors.New("native core refused completion")
	}
	select {
	case done <- err:
		return nil
	default:
		return errors.New("duplicate native completion acknowledgement")
	}
}

func (p *panelChildModel) activation(id, callback string, payload []byte) error {
	n, err := strconv.ParseUint(callback, 10, 64)
	if err != nil || n == 0 || n > uint64(len(p.manifest.Verbs)) {
		return errors.New("unknown native activation callback")
	}
	if _, err := strconv.ParseUint(id, 16, 64); err != nil {
		return err
	}
	request, err := applications.ToJSON(&applications.ActivateRequest{Application: p.manifest.Name, Verb: p.manifest.Verbs[n-1].Name, Payload: applications.Raw(payload)})
	if err != nil {
		return err
	}
	return p.child.frames.activation(panelNativeActivation{ID: id, Request: request})
}

func (p *panelChildModel) Retire() error {
	p.mu.Lock()
	if p.retired {
		p.mu.Unlock()
		return nil
	}
	p.retired = true
	p.mu.Unlock()
	return p.child.bridge.write(nativeAsksFrame{Kind: "owned-core-retire"})
}
func (p *panelChildModel) Close() error { return p.Retire() }
