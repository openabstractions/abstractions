//go:build !windows

package main

import (
	"io"
	"net"
	"os"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
)

type listener struct{ l net.Listener }

// listenAt replicates listen.Listen's unix accept with the binding timed as
// its own step; listen.Listen binds inside Accept.
func listenAt(at string) (*listener, error) {
	os.Remove(at)
	l, err := net.Listen("unix", at)
	return &listener{l}, err
}

func (s *listener) Close() error { return s.l.Close() }

func (s *listener) accept(steps *steps, i int) (io.ReadWriteCloser, func() (*identity.Binding, error), error) {
	c, err := s.l.Accept()
	if err != nil {
		return nil, nil, err
	}
	at := time.Now()
	t := stamp()
	b, berr := identity.BindConn(c, &identity.Options{ConnectedAt: at})
	steps.addAfter(i, "bind (at accept on unix)", stamp()-t)
	return c, func() (*identity.Binding, error) { return b, berr }, nil
}
