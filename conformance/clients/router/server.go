// Isolated fixture: the actual framed Go service reads fake local model hosts.
package main

import (
	"context"
	"flag"
	"fmt"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstraction-router/go/service"
	"github.com/openabstractions/abstractions/conformance/clients/fixture"
	"os"
	"os/user"
	"strconv"
	"sync"
)

func main() {
	endpoint := flag.String("endpoint", "", "isolated framed endpoint")
	resolverEndpoint := flag.String("resolver", "", "isolated resolver endpoint")
	trace := flag.String("trace", "", "HTTP method trace")
	empty := flag.Bool("empty", false, "survey no hosts")
	flag.Parse()
	out, err := os.Create(*trace)
	if err != nil {
		panic(err)
	}
	defer out.Close()
	var mu sync.Mutex
	lemonade, studio, closeHosts := fixture.ModelHosts(func(method string) {
		mu.Lock()
		fmt.Fprintln(out, method)
		mu.Unlock()
	})
	defer closeHosts()
	var r *router.Router
	if *empty {
		r = router.New()
	} else {
		r = router.New(router.Lemonade(lemonade), router.LMStudio(studio), router.Ollama(fixture.UnreachableOllama))
	}
	r.Survey()
	h, err := service.Listen(*endpoint, r)
	if err != nil {
		panic(err)
	}
	defer h.Close()
	h.OnError = func(e error) { fmt.Fprintln(os.Stderr, e) }
	owner, e := user.Current()
	if e != nil {
		panic(e)
	}
	catalog, e := resolution.New([]resolution.Candidate{{Ready: true, Reference: wire.ServiceReference{
		Provider: "router-fixture", Capability: "abstraction.router", Contract: "abstraction.router/router@1", Scope: "local", Transport: resolution.LocalTransport, Endpoint: *endpoint}}})
	if e != nil {
		panic(e)
	}
	resolver, e := resolution.Listen(*resolverEndpoint, catalog, func(peer *identity.Peer, _ wire.ServiceReference) bool {
		observed, e := peer.User.AtLeast(listen.Program.User)
		if e != nil {
			return false
		}
		if observed.Kind == "windows" {
			return observed.SID == owner.Uid
		}
		return strconv.Itoa(observed.UID) == owner.Uid
	})
	if e != nil {
		panic(e)
	}
	defer resolver.Close()
	go func() {
		if e := resolver.Serve(context.Background()); e != nil {
			panic(e)
		}
	}()
	fmt.Println("router-v1: listening", *endpoint)
	if err = h.Serve(context.Background()); err != nil {
		panic(err)
	}
}
