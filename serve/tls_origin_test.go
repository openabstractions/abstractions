package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTrustedTLSServer serves h over https and points http.DefaultClient, which
// the in-process download service uses, at a client that trusts httptest's
// certificate and dials [::1] at 127.0.0.1 until the test ends. A named
// credential travels only over https (download DL-K5); [::1] is a second
// host address the certificate carries.
func newTrustedTLSServer(t testing.TB, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	transport := srv.Client().Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.Replace(addr, "[::1]:", "127.0.0.1:", 1))
	}
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: transport}
	t.Cleanup(func() { http.DefaultClient = old })
	return srv
}
