package main

import (
	"io"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

type listener struct{ l listen.Listener }

func listenAt(at string) (*listener, error) {
	l, err := listen.Listen(at)
	return &listener{l}, err
}

func (s *listener) Close() error { return s.l.Close() }

// accept returns the pipe connection; Windows binds after the first read, so
// the caller times Bind as its own step.
func (s *listener) accept(steps *steps, i int) (io.ReadWriteCloser, func() (*identity.Binding, error), error) {
	c, err := s.l.Accept()
	if err != nil {
		return nil, nil, err
	}
	return c, c.Bind, nil
}
