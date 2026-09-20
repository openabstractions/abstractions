package main

import (
	"net"

	"github.com/openabstractions/abstraction-identity/listen"
)

func dial(at string) (net.Conn, error) { return listen.Dial(at) }
