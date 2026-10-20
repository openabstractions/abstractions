// Package closebeforedispatch closes a local framed connection after receiving
// one complete selected request and before invoking its service callback.
package closebeforedispatch

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Exchanger is the frame transport shape generated Go clients accept.
type Exchanger interface {
	ExchangeFrame([]byte) ([]byte, error)
}

// Transport injects one close before dispatch, using an owned in-memory pipe.
// Every other complete request reaches inner unchanged. It never retries.
type Transport struct {
	inner    Exchanger
	method   string
	mu       sync.Mutex
	fired    bool
	received int
}

// New selects a method; an empty method selects Submit, as in lostreply.
func New(inner Exchanger, method string) *Transport {
	if method == "" {
		method = "Submit"
	}
	return &Transport{inner: inner, method: method}
}

const maxFrameBytes = 1 << 20

// ExchangeFrame writes a complete framed request and reads its reply through
// net.Pipe. On the selected close the reply read returns io.EOF: the caller
// has no nondispatch proof, although the fixture's Fired counter proves it.
func (t *Transport) ExchangeFrame(frame []byte) ([]byte, error) {
	if len(frame) > maxFrameBytes {
		return nil, fmt.Errorf("closebeforedispatch: request exceeds fixture frame limit")
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	deadline := time.Now().Add(5 * time.Second)
	_ = client.SetDeadline(deadline)
	_ = server.SetDeadline(deadline)
	done := make(chan error, 1)
	go func() { done <- t.serve(server) }()
	if err := writeFrame(client, frame); err != nil {
		_ = client.Close()
		<-done
		return nil, err
	}
	reply, err := readFrame(client)
	serverErr := <-done
	if serverErr != nil {
		return nil, serverErr
	}
	return reply, err
}

func (t *Transport) serve(conn net.Conn) error {
	defer conn.Close()
	frame, err := readFrame(conn)
	if err != nil {
		return err
	}
	var envelope struct {
		Method string `json:"method"`
	}
	selected := json.Unmarshal(frame, &envelope) == nil && envelope.Method == t.method
	t.mu.Lock()
	closeNow := selected && !t.fired
	if closeNow {
		t.fired, t.received = true, len(frame)
	}
	t.mu.Unlock()
	if closeNow {
		return nil
	} // Complete frame read; inner has not been called.
	reply, err := t.inner.ExchangeFrame(frame)
	if err != nil {
		return err
	}
	return writeFrame(conn, reply)
}

// Fired reports whether the selected complete payload was received and its
// byte count. This fixture-only observation is unavailable to the caller.
func (t *Transport) Fired() (bool, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fired, t.received
}

func readFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n > maxFrameBytes {
		return nil, fmt.Errorf("closebeforedispatch: incoming frame exceeds fixture limit")
	}
	frame := make([]byte, int(n))
	if _, err := io.ReadFull(r, frame); err != nil {
		return nil, err
	}
	return frame, nil
}

func writeFrame(w io.Writer, frame []byte) error {
	if len(frame) > maxFrameBytes {
		return fmt.Errorf("closebeforedispatch: reply exceeds fixture frame limit")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(frame)))
	_, err := io.Copy(w, io.MultiReader(bytes.NewReader(header[:]), bytes.NewReader(frame)))
	return err
}
