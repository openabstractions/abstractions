package main

import (
	"context"
	"fmt"
	credservice "github.com/openabstractions/abstraction-credentials/go/service"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"runtime"
	"sync"
	"time"
)

type applicationsHost struct {
	listener  listen.Listener
	directory *applicationDirectory
	owner     string
	report    func(error)
	ctx       context.Context
	cancel    context.CancelFunc
	once      sync.Once
	workers   sync.WaitGroup
	slots     chan struct{}
}

func listenApplications(endpoint string, directory *applicationDirectory, owner string, report func(error)) (*applicationsHost, error) {
	l, err := listen.Listen(endpoint)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &applicationsHost{listener: l, directory: directory, owner: owner, report: report, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 64)}, nil
}

func (h *applicationsHost) Close() error {
	var err error
	h.once.Do(func() { h.cancel(); err = h.listener.Close() })
	return err
}

func (h *applicationsHost) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { h.Close() })
	defer stop()
	defer h.workers.Wait()
	defer h.Close()
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || h.ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case h.slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		h.workers.Add(1)
		go func() {
			defer h.workers.Done()
			defer func() { <-h.slots }()
			defer conn.Close()
			callCtx, cancel := context.WithTimeout(h.ctx, (maxObserveWait+5000)*time.Millisecond)
			defer cancel()
			call, err := listen.ReceiveFramed(callCtx, conn, listen.Program, 1<<20)
			if call != nil {
				defer call.Close()
			}
			if err == nil {
				var reply []byte
				receiver := &applicationsReceiver{host: h, call: call, ctx: callCtx}
				if reply, err = (&wire.ApplicationsDispatcher{Handler: receiver}).ExchangeFrame(call.Frame); err == nil {
					err = call.Reply(reply)
				}
			}
			if err != nil && h.report != nil && h.ctx.Err() == nil {
				h.report(fmt.Errorf("applications: %w", err))
			}
		}()
	}
}

// applicationsReceiver binds each call's caller from the connection's Program proof.
type applicationsReceiver struct {
	host *applicationsHost
	call *listen.FramedCall
	ctx  context.Context
}

func (r *applicationsReceiver) caller() rights.Subject {
	if runtime.GOOS == "darwin" {
		return rights.Subject{}
	}
	peer, err := r.call.Peer()
	if err != nil {
		return rights.Subject{}
	}
	subject, err := credservice.SubjectFromPeer(peer)
	if err != nil || subject.Account != r.host.owner || r.call.Recheck() != nil {
		return rights.Subject{}
	}
	return rights.Subject{Account: subject.Account, Program: subject.Program}
}

func (r *applicationsReceiver) Register(v wire.ApplicationDescriptor) (wire.ApplicationChange, error) {
	return r.host.directory.register(r.ctx, r.caller(), v), nil
}
func (r *applicationsReceiver) Remove(name string) (wire.ApplicationChange, error) {
	return r.host.directory.remove(r.ctx, r.caller(), name), nil
}
func (r *applicationsReceiver) Announce(v wire.ApplicationPresence) (wire.ApplicationChange, error) {
	caller := r.caller()
	peer, err := r.call.Peer()
	var session string
	var ok bool
	if err == nil {
		session, ok = applicationSession(peer)
	}
	if !ok {
		caller = rights.Subject{}
		session = ""
	}
	return r.host.directory.announceInSession(r.ctx, caller, session, v), nil
}
func (r *applicationsReceiver) Withdraw(name, instance string) (wire.ApplicationChange, error) {
	caller := r.caller()
	peer, err := r.call.Peer()
	var session string
	var ok bool
	if err == nil {
		session, ok = applicationSession(peer)
	}
	if !ok {
		caller = rights.Subject{}
		session = ""
	}
	return r.host.directory.withdrawInSession(r.ctx, caller, session, name, instance), nil
}
func (r *applicationsReceiver) Observe(cursor string, waitMs int64) (wire.ApplicationPage, error) {
	return r.host.directory.observe(r.ctx, r.caller(), cursor, waitMs), nil
}
func (r *applicationsReceiver) Activate(application string) (wire.ApplicationActivationResult, error) {
	caller := r.caller()
	peer, err := r.call.Peer()
	var session string
	var ok bool
	if err == nil {
		session, ok = applicationSession(peer)
	}
	if !ok {
		caller = rights.Subject{}
		session = ""
	}
	return r.host.directory.activateInSession(r.ctx, caller, session, application), nil
}
