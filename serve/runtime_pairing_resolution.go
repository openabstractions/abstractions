package main

import (
	"context"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-identity/remote"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	rrights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

// pairedResolution owns native endpoints for supported paired contracts.
// Endpoint identity includes the exact peer and consent transcript.
type pairedResolution struct {
	p       *runtimePairing
	mu      sync.Mutex
	routes  map[string]*pairedResolutionRoute
	workers sync.WaitGroup
	closed  bool
}

type pairedResolutionRoute struct {
	listener                                  listen.Listener
	key, generation, name, contract, endpoint string
}

func (m *pairedResolution) current(ctx context.Context, route *pairedResolutionRoute) error {
	if err := messageGeneration(ctx, m.p.state, route.key, route.generation); err != nil {
		return err
	}
	files, _, err := m.p.services.providers.read()
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.Declaration.Name == route.name {
			return errPairingConflict
		}
	}
	return nil
}

func (m *pairedResolution) sync() error {
	// This owner refresh follows a committed pairing edit. Serialize its local
	// disk snapshot with other owner reads; caller admission still uses TryLock.
	m.p.state.mu.Lock()
	doc, _, err := m.p.state.read(m.p.ctx)
	m.p.state.mu.Unlock()
	if err != nil {
		return err
	}
	peers := doc.Peers
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errPairingState
	}
	desired := map[string]bool{}
	base, err := runtimeModelEndpoint(m.p.options)
	if err != nil {
		return err
	}
	for key, peer := range peers {
		if peer.State != "active" || peer.Consent == nil || peer.PeerServiceAddress == "" {
			continue
		}
		generation := hex.EncodeToString(peer.Transcript[:])
		contracts := []string{inferenceservice.RouterContract}
		if m.p.messageHost != nil {
			contracts = append(contracts, new(msg.MessagesDispatcher).ServiceContract())
		}
		for _, contract := range contracts {
			id := "paired:" + key + ":" + generation + ":" + contract
			desired[id] = true
			if m.routes[id] != nil {
				continue
			}
			endpoint := mediatedProviderEndpoint(base, id)
			listener, err := listen.ListenFramed(endpoint, listen.Program)
			if err != nil {
				return err
			}
			route := &pairedResolutionRoute{listener: listen.Sessions(listener, listen.SessionOptions{}), key: key, generation: generation, name: peer.BindingName, contract: contract, endpoint: endpoint}
			m.routes[id] = route
			m.workers.Add(1)
			go m.serve(route)
		}
	}
	for id, route := range m.routes {
		if !desired[id] {
			//unchecked: withdrawn listeners stay withdrawn even if Close reports an error
			_ = route.listener.Close()
			delete(m.routes, id)
		}
	}
	return nil
}

func (m *pairedResolution) candidates() []resolution.Candidate {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []resolution.Candidate
	for _, route := range m.routes {
		if m.current(m.p.ctx, route) != nil {
			continue
		}
		capability, _, _ := strings.Cut(route.contract, "/")
		result = append(result, resolution.Candidate{Machine: route.name, Ready: true, Reference: fwire.ServiceReference{Provider: route.name, Capability: capability, Contract: route.contract, Scope: fwire.ScopeRemote, Transport: resolution.LocalTransport, Endpoint: route.endpoint, Guarantees: []string{}}})
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i].Reference, result[j].Reference
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Contract < b.Contract
	})
	return result
}

func (m *pairedResolution) close() {
	m.mu.Lock()
	m.closed = true
	for _, route := range m.routes {
		//unchecked: shutdown retains no listener or admission after cancellation
		_ = route.listener.Close()
	}
	m.mu.Unlock()
	m.workers.Wait()
}

func (m *pairedResolution) serve(route *pairedResolutionRoute) {
	defer m.workers.Done()
	slots := listen.NewCallAdmission(16)
	var calls sync.WaitGroup
	defer calls.Wait()
	for {
		conn, err := route.listener.Accept()
		if err != nil {
			return
		}
		release, admitted := slots.Take(conn)
		if !admitted {
			continue
		}
		calls.Add(1)
		go func() {
			defer calls.Done()
			defer release()
			defer conn.Close()
			ctx, cancel := context.WithTimeout(m.p.ctx, 35*time.Second)
			defer cancel()
			if err := m.handle(ctx, conn, route); err != nil && m.p.ctx.Err() == nil && m.p.report != nil {
				m.p.report(err)
			}
		}()
	}
}

func (m *pairedResolution) handle(ctx context.Context, conn listen.Conn, route *pairedResolutionRoute) error {
	call, err := listen.ReceiveFramed(ctx, conn, listen.Program, remoteRequestBytes)
	if err != nil {
		return err
	}
	defer call.Close()
	if err := m.current(ctx, route); err != nil {
		return err
	}
	var service fwire.ServedService
	if route.contract == inferenceservice.RouterContract {
		peer, err := call.Peer()
		if err != nil {
			return err
		}
		subject, err := inferenceservice.SubjectFromPeer(peer)
		if err != nil {
			return err
		}
		check := func() error {
			if err := call.Recheck(); err != nil {
				return err
			}
			if err := m.current(ctx, route); err != nil {
				return err
			}
			decision := m.p.rights.decide(ctx, rrights.Subject{Account: subject.Account, Program: subject.Program}, routerservice.ActionInventory, routerservice.ResourceInventory)
			if decision.Outcome != rrights.DecisionOutcomePermitted {
				return errors.New("paired resolution: inventory not permitted")
			}
			return nil
		}
		if err := check(); err != nil {
			return err
		}
		config, address, err := m.p.state.outgoingTLS(ctx, route.key)
		if err != nil {
			return err
		}
		transport, err := (remote.Dialer{TLS: config, Timeout: 30 * time.Second, MaxFrame: remoteRouterReplyBytes, AdmitContext: func(dialCtx context.Context, peer remote.Peer) error {
			if hex.EncodeToString(peer.Key[:]) != route.key {
				return remote.ErrTrust
			}
			if err := m.p.state.admitContext(dialCtx, peer); err != nil {
				return err
			}
			return messageGeneration(dialCtx, m.p.state, route.key, route.generation)
		}}).Client(address)
		if err != nil {
			return err
		}
		service = &rwire.RouterDispatcher{Handler: rwire.NewRouterClient(pairedCheckedTransport{transport.WithContext(ctx), check})}
	} else {
		receiver := &messagesReceiver{host: m.p.messageHost, call: call, ctx: ctx, selectedPeer: route.key, selectedGeneration: route.generation}
		service = &msg.MessagesDispatcher{Handler: receiver}
	}
	reply, err := fwire.ServeEndpointRemote(call.Frame, "openabstractions", "", func(contract, method, profile string) error {
		if contract != route.contract || !remoteOfferedMethod(contract, method) {
			return errors.New("paired resolution: contract or method refused")
		}
		if err := call.Recheck(); err != nil {
			return err
		}
		return m.current(ctx, route)
	}, service)
	if err != nil {
		return err
	}
	if err := m.current(ctx, route); err != nil {
		return err
	}
	return call.Reply(reply)
}

type pairedCheckedTransport struct {
	transport interface{ ExchangeFrame([]byte) ([]byte, error) }
	check     func() error
}

func (t pairedCheckedTransport) ExchangeFrame(frame []byte) ([]byte, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	reply, err := t.transport.ExchangeFrame(frame)
	if err != nil {
		return nil, err
	}
	if err := t.check(); err != nil {
		return nil, err
	}
	return reply, nil
}
