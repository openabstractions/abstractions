// Isolated composition of production runtime services; no OS registration.
package main

import (
	"bufio"
	"context"
	"fmt"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	runtimehost "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	"os"
	"os/user"
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

func main() {
	if len(os.Args) == 2 && os.Args[1] == "principal" {
		account, err := user.Current()
		if err != nil {
			panic(err)
		}
		fmt.Println(account.Uid)
		return
	}
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
		buf := make([]byte, 4096)
		conn.Read(buf)
		if os.Args[1] == "oversized" {
			conn.Write([]byte{0x7f, 0xff, 0xff, 0xff})
		}
		<-ctx.Done()
		return
	}
	dir, err := os.MkdirTemp("", "oa-python-history-")
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
