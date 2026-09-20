// breakdown times each step of one framed call on this host's native IPC, on
// both sides, with the production listen and identity packages.
//
//	breakdown fresh <calls>      a new connection per call, as FRAMING.md specifies today
//	breakdown bound <calls>      one bound connection carrying every call (the floor for reuse)
//	breakdown session <calls>    every call on one session, served by listen.Sessions and ReceiveFramed
//
// The server runs in this process and the client in a child process, so the
// server binds a genuinely different peer. Each mode prints one STEP line per
// step with p50, p90 and p99 in microseconds.
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

type steps struct {
	names []string
	at    map[string][]time.Duration
}

func newSteps() *steps { return &steps{at: map[string][]time.Duration{}} }

func (s *steps) add(name string, d time.Duration) {
	if _, ok := s.at[name]; !ok {
		s.names = append(s.names, name)
	}
	s.at[name] = append(s.at[name], d)
}

func (s *steps) print(side string) {
	for _, name := range s.names {
		v := s.at[name]
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		rank := func(p float64) float64 {
			i := int(float64(len(v))*p+0.999999) - 1
			if i < 0 {
				i = 0
			}
			return float64(v[i].Microseconds()) + float64(v[i].Nanoseconds()%1000)/1000
		}
		fmt.Printf("STEP %s %-28s n=%d p50=%.1f p90=%.1f p99=%.1f us\n", side, name, len(v), rank(0.5), rank(0.9), rank(0.99))
	}
}

const warmup = 20

func main() {
	if len(os.Args) < 3 {
		fmt.Println("breakdown fresh|bound|session <calls>")
		if len(os.Args) == 2 && os.Args[1] == "--help" {
			return
		}
		os.Exit(2)
	}
	calls, err := strconv.Atoi(os.Args[2])
	if err != nil || calls < 1 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "session":
		sessionServer(calls)
	case "client-session":
		sessionClient(calls, os.Args[3])
	case "fresh", "bound":
		// Every step below pays two clock reads; report what one costs.
		began := stamp()
		for i := 0; i < 10000; i++ {
			stamp()
		}
		fmt.Printf("STEP clock one read %.2f us\n", float64((stamp()-began).Nanoseconds())/10000/1000)
		server(os.Args[1], calls)
	case "client-fresh", "client-bound":
		client(os.Args[1], calls, os.Args[3])
	default:
		os.Exit(2)
	}
}

func endpoint() string {
	name := "oa-breakdown-" + strconv.Itoa(os.Getpid())
	if runtime.GOOS == "windows" {
		return `\\.\pipe\` + name
	}
	return filepath.Join(os.TempDir(), name+".sock")
}

var frame = []byte(`{"version":1,"service":"abstraction.facade/caller@1","method":"Observe","arguments":{}}`)

func server(mode string, calls int) {
	at := endpoint()
	l, err := listenAt(at)
	fatal(err)
	defer l.Close()
	self, err := os.Executable()
	fatal(err)
	child := exec.Command(self, "client-"+mode, strconv.Itoa(calls), at)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	fatal(child.Start())
	s := newSteps()
	total := warmup + calls
	if mode == "bound" {
		conn, bind, err := l.accept(s, -1)
		fatal(err)
		var h [4]byte
		_, err = io.ReadFull(conn, h[:])
		fatal(err)
		b, err := bind()
		fatal(err)
		fatal(b.Check(listen.Program))
		for i := 0; i < total; i++ {
			if i > 0 {
				t := stamp()
				_, err = io.ReadFull(conn, h[:])
				fatal(err)
				s.addAfter(i, "read header (waits for client)", stamp()-t)
			}
			t := stamp()
			body := make([]byte, binary.BigEndian.Uint32(h[:]))
			_, err = io.ReadFull(conn, body)
			fatal(err)
			s.addAfter(i, "read body", stamp()-t)
			t = stamp()
			_, err = b.Peer()
			fatal(err)
			s.addAfter(i, "binding recheck", stamp()-t)
			t = stamp()
			fatal(writeFrame(conn, body))
			s.addAfter(i, "write reply", stamp()-t)
		}
		b.Close()
		conn.Close()
	} else {
		for i := 0; i < total; i++ {
			t := stamp()
			conn, bind, err := l.accept(s, i)
			fatal(err)
			accepted := stamp()
			s.addAfter(i, "accept (waits for client)", accepted-t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t = stamp()
			var h [4]byte
			_, err = io.ReadFull(conn, h[:])
			fatal(err)
			s.addAfter(i, "read header", stamp()-t)
			t = stamp()
			b, err := bind()
			fatal(err)
			s.addAfter(i, "bind", stamp()-t)
			t = stamp()
			fatal(b.Check(listen.Program))
			s.addAfter(i, "check need", stamp()-t)
			t = stamp()
			_ = listen.SeenBy(b, nil)
			s.addAfter(i, "caller description", stamp()-t)
			t = stamp()
			body := make([]byte, binary.BigEndian.Uint32(h[:]))
			_, err = io.ReadFull(conn, body)
			fatal(err)
			s.addAfter(i, "read body", stamp()-t)
			t = stamp()
			_, err = b.Peer()
			fatal(err)
			s.addAfter(i, "binding recheck", stamp()-t)
			t = stamp()
			fatal(writeFrame(conn, body))
			s.addAfter(i, "write reply", stamp()-t)
			t = stamp()
			var one [1]byte
			if _, err := conn.Read(one[:]); !errors.Is(err, io.EOF) && err != nil && !isClosed(err) {
				fatal(fmt.Errorf("waiting for EOF: %w", err))
			}
			s.addAfter(i, "wait for client EOF", stamp()-t)
			t = stamp()
			b.Close()
			conn.Close()
			s.addAfter(i, "close", stamp()-t)
			s.addAfter(i, "server total after accept", stamp()-accepted)
			cancel()
			_ = ctx
		}
	}
	fatal(child.Wait())
	s.print("server")
}

func (s *steps) addAfter(i int, name string, d time.Duration) {
	if i >= warmup {
		s.add(name, d)
	}
}

func isClosed(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) || err.Error() == "The pipe has been ended."
}

func writeFrame(w io.Writer, body []byte) error {
	out := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(out, uint32(len(body)))
	copy(out[4:], body)
	_, err := w.Write(out)
	return err
}

func client(mode string, calls int, at string) {
	s := newSteps()
	total := warmup + calls
	if mode == "client-bound" {
		conn, err := dial(at)
		fatal(err)
		for i := 0; i < total; i++ {
			began := stamp()
			fatal(writeFrame(conn, frame))
			s.addAfter(i, "write request", stamp()-began)
			t := stamp()
			var h [4]byte
			_, err = io.ReadFull(conn, h[:])
			fatal(err)
			s.addAfter(i, "read header (waits for server)", stamp()-t)
			body := make([]byte, binary.BigEndian.Uint32(h[:]))
			_, err = io.ReadFull(conn, body)
			fatal(err)
			s.addAfter(i, "call total", stamp()-began)
		}
		conn.Close()
		s.print("client")
		return
	}
	for i := 0; i < total; i++ {
		began := stamp()
		conn, err := dial(at)
		fatal(err)
		s.addAfter(i, "dial", stamp()-began)
		t := stamp()
		fatal(conn.SetDeadline(time.Now().Add(5 * time.Second)))
		s.addAfter(i, "set deadline", stamp()-t)
		t = stamp()
		fatal(writeFrame(conn, frame))
		s.addAfter(i, "write request", stamp()-t)
		t = stamp()
		var h [4]byte
		_, err = io.ReadFull(conn, h[:])
		fatal(err)
		s.addAfter(i, "read header (waits for server)", stamp()-t)
		t = stamp()
		body := make([]byte, binary.BigEndian.Uint32(h[:]))
		_, err = io.ReadFull(conn, body)
		fatal(err)
		s.addAfter(i, "read body", stamp()-t)
		t = stamp()
		conn.Close()
		s.addAfter(i, "close", stamp()-t)
		s.addAfter(i, "call total", stamp()-began)
	}
	s.print("client")
}

// sessionServer serves every call of one session the way a production host
// does: listen.Sessions, then ReceiveFramed, Reply and Close per exchange.
func sessionServer(calls int) {
	at := endpoint()
	inner, err := listen.Listen(at)
	fatal(err)
	l := listen.Sessions(inner, listen.SessionOptions{})
	defer l.Close()
	self, err := os.Executable()
	fatal(err)
	child := exec.Command(self, "client-session", strconv.Itoa(calls), at)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	fatal(child.Start())
	s := newSteps()
	for i := 0; i < warmup+calls; i++ {
		t := stamp()
		conn, err := l.Accept()
		fatal(err)
		accepted := stamp()
		s.addAfter(i, "accept next exchange (waits)", accepted-t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		t = stamp()
		call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 0)
		fatal(err)
		s.addAfter(i, "ReceiveFramed (recheck, need)", stamp()-t)
		t = stamp()
		fatal(call.Reply(call.Frame))
		s.addAfter(i, "Reply", stamp()-t)
		t = stamp()
		call.Close()
		cancel()
		s.addAfter(i, "Close", stamp()-t)
		s.addAfter(i, "server total after accept", stamp()-accepted)
	}
	fatal(child.Wait())
	s.print("server")
}

func sessionClient(calls int, at string) {
	s := newSteps()
	client := listen.FrameClient{Endpoint: at, Timeout: 5 * time.Second, Sessions: true}
	for i := 0; i < warmup+calls; i++ {
		t := stamp()
		_, err := client.ExchangeFrame(frame)
		fatal(err)
		s.addAfter(i, "FrameClient.ExchangeFrame", stamp()-t)
	}
	s.print("client")
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "breakdown:", err)
		os.Exit(1)
	}
}

var _ = identity.ProofNone
