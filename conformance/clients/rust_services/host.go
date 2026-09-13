// Isolated composition of production runtime services; no OS registration.
package main

import (
	"bufio"
	"context"
	"fmt"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	runtimehost "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	"os"
	"path/filepath"
	"time"
)

type sink struct{ *logging.FileSink }

func (s sink) Write(record logging.Record) error {
	if err := s.FileSink.Write(record); err != nil {
		return err
	}
	data, err := record.Encode()
	if err == nil {
		fmt.Println("RECORD " + string(data))
	}
	return err
}

type forged struct{}

func (forged) Resolve(request wire.ResolveRequest) (wire.ResolveResult, error) {
	return wire.ResolveResult{Status: "resolved", Reference: &wire.ServiceReference{Provider: "fixture", Capability: "wrong-capability", Contract: request.Contracts[0], Scope: "local", Transport: "oa-framed-local@1", Endpoint: "must-not-connect"}}, nil
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "bootstrap" {
		endpoint, err := resolution.CheckedDefaultEndpoint()
		if err != nil {
			panic(err)
		}
		fmt.Println(endpoint)
		return
	}
	if len(os.Args) != 3 {
		panic("host <runtime|oversized|quiet> <endpoint>")
	}
	endpoint := os.Args[2]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() { bufio.NewReader(os.Stdin).ReadString('\n'); cancel() }()
	if os.Args[1] != "runtime" {
		listener, err := listen.Listen(endpoint)
		if err != nil {
			panic(err)
		}
		defer listener.Close()
		go func() { <-ctx.Done(); listener.Close() }()
		fmt.Println("READY")
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		go func() { <-ctx.Done(); conn.Close() }()
		if os.Args[1] == "forged" {
			call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
			if err != nil {
				panic(err)
			}
			defer call.Close()
			dispatcher := wire.ResolverDispatcher{Handler: forged{}}
			reply, err := dispatcher.ExchangeFrame(call.Frame)
			if err != nil {
				panic(err)
			}
			if err = call.Reply(reply); err != nil {
				panic(err)
			}
			return
		}
		buf := make([]byte, 4096)
		conn.Read(buf)
		if os.Args[1] == "oversized" {
			conn.Write([]byte{0x7f, 0xff, 0xff, 0xff})
		}
		<-ctx.Done()
		return
	}
	dir, err := os.MkdirTemp("", "oa-rust-history-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	file, err := logging.OpenFileSink(filepath.Join(dir, "records"))
	if err != nil {
		panic(err)
	}
	defer file.Close()
	host, err := runtimehost.Listen(runtimehost.Options{Endpoint: endpoint, LogEndpoint: endpoint + "-log", ConfigEndpoint: endpoint + "-config", Sink: sink{file}, OnError: func(err error) { fmt.Fprintln(os.Stderr, err) }})
	if err != nil {
		panic(err)
	}
	defer host.Close()
	fmt.Println("READY")
	if err := host.Serve(ctx); err != nil {
		panic(err)
	}
}
