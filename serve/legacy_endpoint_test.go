package main

import (
	"context"
	"encoding/json"
	"errors"
	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	router "github.com/openabstractions/abstraction-router/go"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRetiredAliasRefusesDescribeAndResolver(t *testing.T) {
	endpoint := shortMediationEndpoint(t)
	stop := serveRetiredAliases(context.Background(), []retiredAlias{{endpoint, "framed"}}, func(err error) { t.Error(err) })
	defer stop()
	transport := listen.FrameClient{Endpoint: endpoint, Timeout: time.Second, MaxFrame: 1 << 20}
	_, err := wire.NewEndpointClient(transport).Describe()
	var refusal *wire.ServiceError
	if !errors.As(err, &refusal) || refusal.Code != wire.ServiceErrorCodeUnknownService {
		t.Fatalf("Describe did not refuse: %v", err)
	}
	_, err = wire.NewResolverClient(transport).Resolve(wire.ResolveRequest{Scope: wire.ScopeLocal})
	if !errors.As(err, &refusal) || refusal.Code != wire.ServiceErrorCodeUnknownService {
		t.Fatalf("resolver did not refuse: %v", err)
	}
}
func TestRetiredAliasesUseExistingLineRefusals(t *testing.T) {
	for _, codec := range []string{"asks", "rights", "router"} {
		t.Run(codec, func(t *testing.T) {
			endpoint := shortMediationEndpoint(t)
			stop := serveRetiredAliases(context.Background(), []retiredAlias{{endpoint, codec}}, func(err error) { t.Error(err) })
			defer stop()
			conn, err := listen.Dial(endpoint)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := json.NewEncoder(conn).Encode(map[string]string{"op": "not-forwarded"}); err != nil {
				t.Fatal(err)
			}
			switch codec {
			case "asks":
				var response asks.Response
				if err := json.NewDecoder(conn).Decode(&response); err != nil {
					t.Fatal(err)
				}
				if response.Code != asks.CodeUnknownOperation || response.Err() == nil {
					t.Fatal(response)
				}
			case "rights":
				var response rights.Response
				if err := json.NewDecoder(conn).Decode(&response); err != nil {
					t.Fatal(err)
				}
				if response.Code != rights.CodeUnknownOperation || response.Err() == nil {
					t.Fatal(response)
				}
			case "router":
				var response router.Response
				if err := json.NewDecoder(conn).Decode(&response); err != nil {
					t.Fatal(err)
				}
				if response.Code != router.CodeUnknownOperation || response.Err() == nil {
					t.Fatal(response)
				}
			}
		})
	}
}
func TestRetiredAliasBusyPreservesCurrentListener(t *testing.T) {
	endpoint := shortMediationEndpoint(t)
	first, err := listen.Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	reports := []error{}
	stop := serveRetiredAliases(context.Background(), []retiredAlias{{endpoint, "framed"}}, func(err error) { reports = append(reports, err) })
	defer stop()
	if len(reports) != 1 {
		t.Fatal("busy legacy name not reported", reports)
	}
	accepted := make(chan error, 1)
	go func() {
		conn, err := first.Accept()
		if err == nil {
			err = conn.Close()
		}
		accepted <- err
	}()
	// The client stays connected until Accept reports: on Windows a client that
	// hangs up before Accept issues its connect is dropped as ERROR_NO_DATA,
	// and Accept then waits for the next client.
	conn, err := listen.Dial(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("original listener was displaced")
	}
}
func TestRetiredAliasRefusesInsecureNamespace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix parent permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "old.sock")
	count := 0
	stop := serveRetiredAliases(context.Background(), []retiredAlias{{path, "framed"}}, func(error) { count++ })
	stop()
	if count != 1 {
		t.Fatal("unsafe legacy namespace not reported", count)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsafe alias created", err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatal("unsafe parent altered", err)
	}
}
func TestNamedRuntimeSkipsGlobalLegacyAliases(t *testing.T) {
	for _, options := range []runtimeFlags{{isolated: "test"}, {stateDir: "owned-state"}, {endpoint: "explicit"}} {
		stop := serveLegacyCompatibility(context.Background(), options, func(err error) { t.Fatalf("named runtime tried old names: %v", err) })
		stop()
	}
}
