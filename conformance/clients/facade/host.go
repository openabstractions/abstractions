// Test fixture composition: all protocol, identity and storage behavior below
// comes from the production services/providers. The router has no external hosts.
package main

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strconv"

	config "github.com/openabstractions/abstraction-config/go/service"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	logservice "github.com/openabstractions/abstraction-logging/go/service"
	router "github.com/openabstractions/abstraction-router/go"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

type service interface {
	Serve(context.Context) error
	Close() error
}

func main() {
	if len(os.Args) < 3 {
		panic("host <logging|config|router|resolver> <endpoint> [log-file|logging config router]")
	}
	var host service
	var err error
	switch os.Args[1] {
	case "logging":
		if len(os.Args) != 4 {
			panic("logging requires an output file")
		}
		sink, openErr := logging.OpenFileSink(os.Args[3])
		if openErr != nil {
			panic(openErr)
		}
		defer sink.Close()
		host, err = logservice.Listen(os.Args[2], sink)
	case "config":
		host, err = config.Listen(os.Args[2])
	case "router":
		// No Survey/Poll: this deliberately empty provider performs no network,
		// GPU utility invocation or discovery of the owner's actual model hosts.
		host, err = routerservice.Listen(os.Args[2], router.New())
	case "resolver":
		if len(os.Args) != 6 {
			panic("resolver requires three provider endpoints")
		}
		owner, ownerErr := user.Current()
		if ownerErr != nil {
			panic(ownerErr)
		}
		var candidates []resolution.Candidate
		for i, capability := range []string{"logging", "config", "router"} {
			contract := []string{"sink", "reader", "router"}[i]
			candidates = append(candidates, resolution.Candidate{Ready: true, Reference: wire.ServiceReference{
				Provider: "facade-fixture", Capability: "abstraction." + capability,
				Contract: "abstraction." + capability + "/" + contract + "@1",
				Scope:    wire.ScopeLocal, Transport: resolution.LocalTransport, Endpoint: os.Args[3+i],
				Guarantees: []string{},
			}})
		}
		editor := candidates[1]
		editor.Reference.Contract = "abstraction.config/editor@1"
		candidates = append(candidates, editor)
		history := candidates[0]
		history.Reference.Contract = "abstraction.logging/reader@1"
		candidates = append(candidates, history)
		catalog, catalogErr := resolution.New(candidates)
		if catalogErr != nil {
			panic(catalogErr)
		}
		host, err = resolution.Listen(os.Args[2], catalog, func(peer *identity.Peer, _ wire.ServiceReference) bool {
			observed, err := peer.User.AtLeast(listen.Program.User)
			if err != nil {
				return false
			}
			if observed.Kind == "windows" {
				return observed.SID == owner.Uid
			}
			return observed.Kind == "posix" && strconv.Itoa(observed.UID) == owner.Uid
		})
	default:
		panic("unknown service")
	}
	if err != nil {
		panic(err)
	}
	defer host.Close()
	fmt.Println("READY", os.Args[1])
	if err := host.Serve(context.Background()); err != nil {
		panic(err)
	}
}
