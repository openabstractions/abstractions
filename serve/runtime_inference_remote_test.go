package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"github.com/openabstractions/abstraction-identity/remote"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
)

// remoteTrustFiles writes an isolated CA, a server certificate for
// lab.runtime.test and a client certificate and key as PEM files, and returns
// the server's TLS configuration and the client key's digest.
func remoteTrustFiles(t *testing.T, dir string) (*tls.Config, [32]byte, inferenceRemoteTrust) {
	t.Helper()
	caPub, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture remote CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ = x509.ParseCertificate(caDER)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	issue := func(serial int64, usage x509.ExtKeyUsage) ([]byte, ed25519.PrivateKey, [32]byte) {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		c := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{"lab.runtime.test"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		der, err := x509.CreateCertificate(rand.Reader, c, ca, pub, caKey)
		if err != nil {
			t.Fatal(err)
		}
		parsed, _ := x509.ParseCertificate(der)
		return der, key, sha256.Sum256(parsed.RawSubjectPublicKeyInfo)
	}
	serverDER, serverKey, serverDigest := issue(2, x509.ExtKeyUsageServerAuth)
	clientDER, clientKey, clientDigest := issue(3, x509.ExtKeyUsageClientAuth)
	write := func(name, kind string, der []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	trust := inferenceRemoteTrust{ServerName: "lab.runtime.test", Roots: write("roots.pem", "CERTIFICATE", caDER),
		Certificate: write("client.pem", "CERTIFICATE", clientDER), Key: write("client-key.pem", "PRIVATE KEY", keyDER), ServerKeySHA256: hex.EncodeToString(serverDigest[:])}
	server := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{serverDER}, PrivateKey: serverKey}}, ClientCAs: roots}
	return server, clientDigest, trust
}

func TestRemoteAdmissionTracksCurrentRegistry(t *testing.T) {
	key := [32]byte{1}
	file := providerFile{Generation: "first", Declaration: providerDeclaration{Name: "lab", Remote: &inferenceRemoteTrust{ServerKeySHA256: hex.EncodeToString(key[:])}}}
	supervisor := &providerSupervisor{file: file}
	p := &runtimeProviders{supervisors: map[string]*providerSupervisor{"lab": supervisor}}
	admit := p.remoteAdmit(file)
	peer := remote.Peer{Key: key}
	if err := admit(peer); err != nil {
		t.Fatal(err)
	}
	if err := admit(remote.Peer{Key: [32]byte{2}}); !errors.Is(err, remote.ErrTrust) {
		t.Fatalf("wrong key: %v", err)
	}
	// A pin-only edit keeps the published V1 projection and generation, but
	// must invalidate a retained TLS client against the current internal record.
	replacement := *file.Declaration.Remote
	replacement.ServerKeySHA256 = hex.EncodeToString(make([]byte, 32))
	supervisor.file.Declaration.Remote = &replacement
	if err := admit(peer); !errors.Is(err, remote.ErrTrust) {
		t.Fatalf("same-generation pin edit retained client: %v", err)
	}
	supervisor.file = file
	supervisor.file.Disabled = true
	if err := admit(peer); !errors.Is(err, remote.ErrTrust) {
		t.Fatalf("disabled retained client: %v", err)
	}
	supervisor.file.Disabled = false
	delete(p.supervisors, "lab")
	if err := admit(peer); !errors.Is(err, remote.ErrTrust) {
		t.Fatalf("removed retained client: %v", err)
	}
	p.supervisors["lab"] = supervisor
	supervisor.file.Generation = "replacement"
	if err := admit(peer); !errors.Is(err, remote.ErrTrust) {
		t.Fatalf("replaced retained client: %v", err)
	}
	supervisor.file = file
	if err := admit(peer); err != nil {
		t.Fatalf("restored declaration: %v", err)
	}
	p.closed = true
	if err := admit(peer); !errors.Is(err, remote.ErrTrust) {
		t.Fatalf("closed owner: %v", err)
	}
}

func TestRemotePinRequiresCanonicalExplicitValue(t *testing.T) {
	for _, value := range []string{"", strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		r := &inferenceRemoteTrust{ServerKeySHA256: value}
		if _, err := r.config(); !errors.Is(err, remote.ErrTrust) {
			t.Fatalf("pin %q before file access: %v", value, err)
		}
	}
}

// The shipped runtime declares another runtime with provider add --remote: the
// declaration keeps the trust file paths in the registry, endpoint@1 Describe
// over mutual TLS reads it ready, and the runtime's router@1 lists the remote's
// hosts in its domain. hosts.json holds no remote entry. A trust file that is
// missing is invalid before anything is written, and withdrawing the
// declaration removes the remote's hosts.
func TestRuntimeRegistersARemoteRuntime(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	address, trust := startRemoteRuntime(t)
	options, _ := isolatedRuntime(t)
	if err := os.MkdirAll(filepath.Join(options.stateDir, "inference"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.stateDir, "inference", inferenceHostsFile), []byte(`{"local":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	args := func(key string) []string {
		return append([]string{"add", "lab", "--remote", address, "--credential", "openrouter", "--server-name", trust.ServerName,
			"--trust", trust.Roots, "--client", trust.Certificate, "--client-key", key, "--server-key-sha256", trust.ServerKeySHA256}, endpoint...)
	}
	if out, err := runProvider(t, args(filepath.Join(t.TempDir(), "missing.pem"))...); err == nil || !strings.Contains(err.Error(), "remote") {
		t.Fatalf("a missing key file: %v\n%s", err, out)
	}
	if out, err := runProvider(t, args(trust.Key)...); err != nil {
		t.Fatalf("provider add --remote: %v\n%s", err, out)
	}
	var lab fwire.DeclarationState
	eventually(t, 20*time.Second, "the remote runtime reading ready", func() bool {
		lab = providerStates(t, endpoint)["lab"]
		return lab.Readiness == fwire.DeclarationReadinessReady
	})
	d := lab.Declaration
	if d.Transport != fwire.DeclarationTransportRemote || d.Activation != fwire.ActivationRemote || d.Remote == nil || d.Remote.Key != trust.Key || d.Remote.Credential != "openrouter" ||
		d.Endpoint != "tls://"+address || len(lab.Described) != 7 || lab.Described[1].Contract != "abstraction.inference/embed@1" ||
		lab.Described[2].Contract != "abstraction.inference/transcription@1" || lab.Described[3].Contract != "abstraction.inference/speech@1" ||
		lab.Described[4].Contract != "abstraction.inference/image@1" || lab.Described[5].Contract != "abstraction.inference/live@1" {
		t.Fatalf("remote declaration %+v", lab)
	}
	routerHosts := func() []string {
		call, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		routes, err := client.New(options.endpoint).ResolveRouter(call, client.Requirements{})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := routes.HostsContext(call, true)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range snapshot.Hosts {
			out = append(out, h.Host+"@"+h.Domain)
		}
		return out
	}
	if hosts := routerHosts(); !slices.Contains(hosts, "lab/openrouter@lab") {
		t.Fatalf("router@1 hosts lack the remote's host: %v", hosts)
	}
	call, stop := context.WithTimeout(context.Background(), 30*time.Second)
	registry, err := client.New(options.endpoint).ResolveRegistryV3(call, client.Requirements{})
	if err != nil {
		stop()
		t.Fatal(err)
	}
	page, err := registry.Registrations(call, "", 64)
	stop()
	if err != nil || page.Outcome.String() != "page" {
		t.Fatalf("registered remote: %+v %v", page, err)
	}
	found := false
	for _, record := range page.Registrations {
		for _, remote := range record.Spec.Remotes {
			if remote.Name == "lab" {
				found = true
				if remote.ServerName != trust.ServerName || remote.ServerKeySHA256 != trust.ServerKeySHA256 || remote.Endpoint != "tls://"+address || remote.Credential != "openrouter" || record.Spec.Program != "" {
					t.Fatalf("registered remote trust: %+v", remote)
				}
			}
		}
	}
	if !found {
		t.Fatal("registry lost the paired remote")
	}
	hosts, err := os.ReadFile(filepath.Join(options.stateDir, "inference", inferenceHostsFile))
	if err != nil || strings.Contains(string(hosts), "lab") {
		t.Fatalf("hosts.json holds the remote: %v\n%s", err, hosts)
	}
	if out, err := runProvider(t, append([]string{"remove", "lab"}, endpoint...)...); err != nil {
		t.Fatalf("provider remove: %v\n%s", err, out)
	}
	if hosts := routerHosts(); slices.ContainsFunc(hosts, func(h string) bool { return strings.HasPrefix(h, "lab/") }) {
		t.Fatalf("router@1 still lists the withdrawn remote: %v", hosts)
	}
}

// remoteTestPin admits exactly the isolated fixture certificate's leaf key.
func remoteTestPin(t *testing.T, config *tls.Config) func(remote.Peer) error {
	t.Helper()
	cert, err := x509.ParseCertificate(config.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return func(peer remote.Peer) error {
		if peer.Key != key {
			return remote.ErrTrust
		}
		return nil
	}
}
