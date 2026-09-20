package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	router "github.com/openabstractions/abstraction-router/go"
)

// inferenceRemoteTrust is the mutual-TLS trust of an oa-remote@1 host as
// a remote declaration keeps it: PEM file paths on this machine, never key material.
type inferenceRemoteTrust struct {
	ServerName  string `json:"server_name"`
	Roots       string `json:"roots"`
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
	// Credential names the credential the remote holds and applies.
	Credential string `json:"credential,omitempty"`
}

// absolute is path made absolute against the working directory, or path when
// it is empty or cannot be.
func absolute(path string) string {
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// remoteAddress is the host:port of a tls://host:port base.
func remoteAddress(base string) (string, error) {
	address, ok := strings.CutPrefix(base, "tls://")
	host, port, err := net.SplitHostPort(address)
	if n, perr := strconv.Atoi(port); !ok || err != nil || host == "" || perr != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("remote base %q is not tls://<host>:<port>", base)
	}
	return address, nil
}

// remoteTLS loads the trust files: the roots trusted for the server, and this
// runtime's client certificate and key.
func (r *inferenceRemoteTrust) config() (*tls.Config, error) {
	if r == nil {
		return nil, errors.New("remote trust required")
	}
	for _, path := range []string{r.Roots, r.Certificate, r.Key} {
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("remote trust path %q is not absolute", path)
		}
	}
	pem, err := os.ReadFile(r.Roots)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s holds no certificate", r.Roots)
	}
	pair, err := tls.LoadX509KeyPair(r.Certificate, r.Key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: roots, ServerName: r.ServerName}, nil
}

// remoteRouterHost is the router host of a remote declaration.
func remoteRouterHost(d providerDeclaration) (*router.Host, error) {
	address, err := remoteAddress(d.Endpoint)
	if err != nil {
		return nil, err
	}
	config, err := d.Remote.config()
	if err != nil {
		return nil, fmt.Errorf("providers: remote runtime %s: %w", d.Name, err)
	}
	return router.NewRemote(d.Name, address, config, d.Remote.Credential)
}
