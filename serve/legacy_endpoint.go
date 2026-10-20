package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	router "github.com/openabstractions/abstraction-router/go"
	"io"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type retiredAlias struct{ endpoint, codec string }

// This finite compatibility roster serves refusals only for one train.
func retiredRuntimeAliases() []retiredAlias {
	aliases := []retiredAlias{}
	for _, service := range []string{"runtime-v1", "logging-v1", "config-v1", "job-acceptance-v1", "model-v1", "router-v1", "resource-table-v1", "storage-content-v1", "credentials-v1", "rights-authorization-v1", "asks-application-v1", "applications-v1", "registry-v1", "inference-v1", "inference-remote-v1", "lend-v1"} {
		aliases = append(aliases, retiredAlias{listen.Endpoint(service), "framed"})
	}
	for _, service := range []string{"asks", "rights", "router"} {
		aliases = append(aliases, retiredAlias{listen.Endpoint(service), service})
	}
	return aliases
}
func serveLegacyCompatibility(ctx context.Context, options runtimeFlags, report func(error)) func() {
	if options.isolated != "" || options.stateDir != "" {
		return func() {}
	}
	for _, item := range endpointFlags(options) {
		if item.value != "" {
			return func() {}
		}
	}
	return serveRetiredAliases(ctx, retiredRuntimeAliases(), report)
}
func serveRetiredAliases(parent context.Context, aliases []retiredAlias, report func(error)) func() {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	listeners := []listen.Listener{}
	slots := make(chan struct{}, 16)
	for _, alias := range aliases {
		if ctx.Err() != nil {
			break
		}
		if runtime.GOOS != "windows" {
			if err := privatedir.CheckDir(filepath.Dir(alias.endpoint)); err != nil {
				if report != nil {
					report(fmt.Errorf("legacy compatibility unavailable for %s: %w", alias.endpoint, err))
				}
				continue
			}
		}
		listener, err := listen.Listen(alias.endpoint)
		if err != nil {
			if report != nil {
				report(fmt.Errorf("legacy compatibility unavailable for %s: %w", alias.endpoint, err))
			}
			continue
		}
		listeners = append(listeners, listener)
		wg.Add(1)
		go func(l listen.Listener, codec string) {
			defer wg.Done()
			for {
				conn, err := l.Accept()
				if err != nil {
					return
				}
				if ctx.Err() != nil { //unchecked: canceled refusal-only accept
					conn.Close()
					return
				}
				select {
				case slots <- struct{}{}:
				default: //unchecked: no work is admitted when the finite refusal slots are full
					conn.Close()
					continue
				}
				wg.Add(1)
				go func() { defer wg.Done(); defer func() { <-slots }(); refuseRetiredConnection(ctx, conn, codec) }()
			}
		}(listener, alias.codec)
	}
	var once sync.Once
	closeListeners := func() {
		once.Do(func() {
			for _, l := range listeners {
				if err := l.Close(); err != nil && report != nil {
					report(err)
				}
			}
		})
	}
	watch := context.AfterFunc(ctx, closeListeners)
	return func() { cancel(); closeListeners(); watch(); wg.Wait() }
}

type retiredEndpoint struct{}

func (retiredEndpoint) Describe() (wire.Description, error) {
	return wire.Description{}, &wire.ServiceError{Code: wire.ServiceErrorCodeUnknownService, Message: "endpoint renamed; use the current per-account endpoint"}
}
func refuseRetiredConnection(parent context.Context, conn listen.Conn, codec string) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	var once sync.Once
	closeConn := func() {
		once.Do(func() { //unchecked: this refusal-only connection is being closed on every path
			conn.Close()
		})
	}
	watch := context.AfterFunc(ctx, closeConn)
	defer watch()
	defer closeConn()
	if codec == "framed" {
		call, err := listen.ReceiveFramed(ctx, conn, identity.Need{User: identity.ProofKernel}, 1<<20)
		if err != nil {
			return
		}
		defer call.Close()
		reply, err := (&wire.EndpointDispatcher{Handler: retiredEndpoint{}}).ExchangeFrame(call.Frame)
		if err == nil { //unchecked: a failed refusal reply cannot grant a capability or forward work
			call.Reply(reply)
		}
		return
	}
	line, err := bufio.NewReader(io.LimitReader(conn, (1<<20)+1)).ReadBytes('\n')
	if err != nil || len(line) > 1<<20 {
		return
	}
	encoder := json.NewEncoder(conn)
	switch codec {
	case "asks":
		var request asks.Request
		if json.Unmarshal(line, &request) == nil { //unchecked: a failed refusal reply has no service effects
			encoder.Encode(asks.Response{Code: asks.CodeUnknownOperation, Error: "endpoint renamed"})
		}
	case "rights":
		var request rights.Request
		if json.Unmarshal(line, &request) == nil { //unchecked: a failed refusal reply has no service effects
			encoder.Encode(rights.Response{Code: rights.CodeUnknownOperation, Error: "endpoint renamed"})
		}
	case "router":
		var request router.Request
		if json.Unmarshal(line, &request) == nil { //unchecked: a failed refusal reply has no service effects
			encoder.Encode(router.Response{Code: router.CodeUnknownOperation, Error: "endpoint renamed"})
		}
	}
}
