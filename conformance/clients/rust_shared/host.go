// Protocol peer for shared transport tests; no capability or C++ server.
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	"github.com/openabstractions/abstraction-identity/listen"
	"io"
	"os"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "bootstrap" {
		s, e := resolution.CheckedDefaultEndpoint()
		if e != nil {
			panic(e)
		}
		fmt.Println(s)
		return
	}
	if len(os.Args) != 3 {
		panic("peer <echo|oversized|header|body|quiet|oneway> <endpoint>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() { var b [1]byte; os.Stdin.Read(b[:]); cancel() }()
	l, e := listen.Listen(os.Args[2])
	if e != nil {
		panic(e)
	}
	defer l.Close()
	go func() { <-ctx.Done(); l.Close() }()
	fmt.Println("READY")
	c, e := l.Accept()
	if e != nil {
		return
	}
	defer c.Close()
	go func() { <-ctx.Done(); c.Close() }()
	var header [4]byte
	if _, e = io.ReadFull(c, header[:]); e != nil {
		panic(e)
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > 1024 {
		panic("unexpected request size")
	}
	payload := make([]byte, size)
	if _, e = io.ReadFull(c, payload); e != nil {
		panic(e)
	}
	switch os.Args[1] {
	case "echo":
		c.Write(header[:])
		c.Write(payload)
	case "oversized":
		c.Write([]byte{0x7f, 0xff, 0xff, 0xff})
	case "header":
		c.Write([]byte{0, 0})
	case "body":
		c.Write([]byte{0, 0, 0, 5, 1, 2})
	case "quiet":
		<-ctx.Done()
	case "oneway":
		return
	default:
		panic("invalid mode")
	}
}
