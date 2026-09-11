// Test fixture composition: all protocol, identity and storage behavior below
// comes from the production services/providers. The router has no external hosts.
package main

import (
	"context"
	"fmt"
	"os"

	config "github.com/openabstractions/abstraction-config/go/service"
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
		panic("host <logging|config|router> <endpoint> [log-file]")
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
