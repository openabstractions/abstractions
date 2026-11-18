package main

// The native resource adapter transports complete bodies to the shared SDK's
// McpSession. JSON-RPC syntax and dispatch remain owned by that session.
import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/listen"
)

const panelOwnedFrameLimit = 1 << 20
const panelOwnedFrameQueue = 16

type panelOwnedFrames struct {
	ctx        context.Context
	cancel     context.CancelFunc
	in         io.ReadCloser
	out        io.Writer
	frames     chan []byte
	done       chan struct{}
	writeMu    sync.Mutex
	readMu     sync.Mutex
	retireOnce sync.Once
	onRetire   func()
	modelMu    sync.Mutex
	callbackMu sync.Mutex
	model      panelNativeModel
	pending    map[string]bool
	readError  error // published by closing done after the original reader returns
}

func newPanelOwnedFrames(in io.ReadCloser, out io.Writer, onRetire func()) *panelOwnedFrames {
	ctx, cancel := context.WithCancel(context.Background())
	return &panelOwnedFrames{ctx: ctx, cancel: cancel, in: in, out: out,
		frames: make(chan []byte, panelOwnedFrameQueue), done: make(chan struct{}), onRetire: onRetire, pending: map[string]bool{}}
}

func (p *panelOwnedFrames) attachModel(model panelNativeModel) error {
	p.modelMu.Lock()
	defer p.modelMu.Unlock()
	if model == nil || p.model != nil || p.ctx.Err() != nil {
		return errors.New("Panel native model unavailable")
	}
	p.model = model
	return nil
}

// schedule never waits for native state or owned I/O. In particular the sole
// Mac bridge reader can enqueue an earlier callback before a completion ack.
func (p *panelOwnedFrames) schedule(actions []panelNativeActivation) error {
	p.callbackMu.Lock()
	defer p.callbackMu.Unlock()
	for _, action := range actions {
		if action.ID == "" || len(action.ID) > 64 || len(action.Request) > panelOwnedFrameLimit || p.pending[action.ID] {
			return errors.New("invalid native callback")
		}
		body := append([]byte(`{"id":`), []byte(strconv.Quote(action.ID))...)
		body = append(body, []byte(`,"request":`)...)
		body = append(body, strconv.Quote(string(action.Request))...)
		body = append(body, '}')
		if len(body) > panelOwnedFrameLimit {
			return errors.New("Panel callback envelope exceeds frame bound")
		}
		p.pending[action.ID] = true
		if p.ctx.Err() != nil {
			continue
		} // retain its owner until window close
		select {
		case p.frames <- body:
		default:
			return errors.New("Panel native callback queue full")
		}
	}
	return nil
}

func (p *panelOwnedFrames) emit(output [][]byte) error {
	for _, body := range output {
		if err := p.writeFrame(body); err != nil {
			return err
		}
	}
	return nil
}

func (p *panelOwnedFrames) receive(body []byte) error {
	p.modelMu.Lock()
	if p.model == nil {
		p.modelMu.Unlock()
		select {
		case p.frames <- body:
			return nil
		default:
			return errors.New("Panel frame queue full")
		}
	}
	actions, output, err := p.model.Receive(body)
	if err == nil {
		err = p.schedule(actions)
	}
	p.modelMu.Unlock()
	if err != nil {
		return err
	}
	return p.emit(output)
}

func (p *panelOwnedFrames) activation(action panelNativeActivation) error {
	if p.ctx.Err() != nil {
		return errors.New("Panel native mediation closed")
	}
	return p.schedule([]panelNativeActivation{action})
}

func (p *panelOwnedFrames) complete(id string, body []byte) error {
	return p.finishCallback(id, body, false)
}

func (p *panelOwnedFrames) fault(id string) error {
	return p.finishCallback(id, nil, true)
}

func (p *panelOwnedFrames) finishCallback(id string, body []byte, fault bool) error {
	p.modelMu.Lock()
	if p.model == nil {
		p.modelMu.Unlock()
		if fault {
			return errors.New("native callback owner unavailable")
		}
		return p.writeFrame(body)
	}
	p.callbackMu.Lock()
	known := p.pending[id]
	p.callbackMu.Unlock()
	if !known {
		p.modelMu.Unlock()
		return errors.New("unknown native activation callback")
	}
	var actions []panelNativeActivation
	var output [][]byte
	var err error
	if fault {
		actions, output, err = p.model.Fault(id)
	} else {
		actions, output, err = p.model.Complete(id, body)
	}
	if err == nil {
		p.callbackMu.Lock()
		delete(p.pending, id)
		p.callbackMu.Unlock()
		err = p.schedule(actions)
	}
	p.modelMu.Unlock()
	if err != nil {
		return err
	}
	return p.emit(output)
}

// read belongs to the one owned input. A full queue refuses further mediation;
// it cannot grow with an unresponsive or replaced document.
func (p *panelOwnedFrames) read() {
	defer close(p.done)
	defer p.retire()
	for {
		body, err := listen.ReadFrameFrom(p.in, panelOwnedFrameLimit)
		if err != nil {
			p.readError = err
			return
		}
		if p.ctx.Err() != nil {
			return
		}
		if err := p.receive(body); err != nil {
			p.readError = err
			return
		}
	}
}

// retire ends callbacks and presence. Window ownership is deliberately separate.
func (p *panelOwnedFrames) retire() {
	p.retireOnce.Do(func() {
		p.cancel()
		// Release a blocked owned read/write before waiting for native state.
		_ = p.in.Close()
		if closer, ok := p.out.(io.Closer); ok {
			_ = closer.Close()
		}
		p.modelMu.Lock()
		if p.model != nil {
			if err := p.model.Retire(); err != nil {
				log.Printf("panel: the App Model presence was not retired: %v", err)
			}
		}
		p.modelMu.Unlock()
		if p.onRetire != nil {
			p.onRetire()
		}
	})
}

// close is called when the native window exits and releases a blocked input read.
func (p *panelOwnedFrames) close() error {
	p.retire()
	err := p.in.Close()
	p.modelMu.Lock()
	defer p.modelMu.Unlock()
	if p.model != nil {
		body, _ := applications.ToJSON(&applications.HostActivationResult{Outcome: applications.HostActivationOutcomeUnavailable, Reason: "Panel window closed"})
		p.callbackMu.Lock()
		ids := make([]string, 0, len(p.pending))
		for id := range p.pending {
			ids = append(ids, id)
		}
		p.callbackMu.Unlock()
		for _, id := range ids {
			if _, _, failure := p.model.Complete(id, body); failure != nil {
				return failure
			}
			p.callbackMu.Lock()
			delete(p.pending, id)
			p.callbackMu.Unlock()
		}
		if failure := p.model.Close(); failure != nil {
			return failure
		}
	}
	return err
}

func (p *panelOwnedFrames) write(body []byte) error {
	err := p.writeFrame(body)
	if err != nil {
		p.retire()
	}
	return err
}

func (p *panelOwnedFrames) writeFrame(body []byte) error {
	if len(body) > panelOwnedFrameLimit {
		return listen.ErrFrameTooLarge
	}
	if !p.writeMu.TryLock() {
		return errors.New("Panel host write already pending")
	}
	defer p.writeMu.Unlock()
	if p.ctx.Err() != nil {
		return errors.New("Panel host mediation ended")
	}
	if err := listen.WriteFrameTo(p.out, body, panelOwnedFrameLimit); err != nil {
		return err
	}
	return nil
}

// callbacks is installed only on the owned native resource run. Ordinary HTTP
// Panel routes never receive this handler. The existing window key is retained.
func (p *panelOwnedFrames) callbacks(key string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/native-host/read", "/native-host/write":
			guard(key, p.callback).ServeHTTP(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func (p *panelOwnedFrames) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if p.ctx.Err() != nil && r.URL.Path == "/native-host/read" {
		http.Error(w, "Panel host mediation ended", http.StatusGone)
		return
	}
	if r.URL.Path == "/native-host/read" {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !p.readMu.TryLock() {
			w.WriteHeader(http.StatusConflict)
			return
		}
		defer p.readMu.Unlock()
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		select {
		case <-p.ctx.Done():
			w.WriteHeader(http.StatusGone)
		case <-r.Context().Done():
			w.WriteHeader(http.StatusRequestTimeout)
		case <-timer.C:
			w.WriteHeader(http.StatusNoContent)
		case body := <-p.frames:
			if p.ctx.Err() != nil {
				w.WriteHeader(http.StatusGone)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(body)
		}
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Content-Type") != "application/octet-stream" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, panelOwnedFrameLimit+1))
	if err != nil || len(body) > panelOwnedFrameLimit {
		// A known issued callback with an unreadable/oversized completion is
		// terminally failed by the same native owner; unknown tickets do nothing.
		if failure := p.fault(r.URL.Query().Get("call")); failure != nil {
			p.retire()
		}
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	fault := r.URL.Query().Get("fault")
	if fault != "" && (fault != "1" || len(body) != 0) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if fault == "1" {
		err = p.fault(r.URL.Query().Get("call"))
	} else {
		err = p.complete(r.URL.Query().Get("call"), body)
	}
	if err != nil {
		p.retire()
		w.WriteHeader(http.StatusGone)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
