package remoteinference_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/remote"
	inference "github.com/openabstractions/abstraction-inference/go"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-inference/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

const (
	secret        = "sk-REMOTE-FIXTURE-7c1d"
	remoteProgram = "/opt/openabstractions/remote/openabstractions"
	localProgram  = "/usr/local/bin/aider"
	namespace     = "lab-peer/origin-runtime"
	model         = "anthropic/claude-sonnet-5"
)

// trust makes an isolated CA, the remote runtime's server certificate, and two
// client certificates: the origin runtime's, which the remote maps, and an
// unmapped one.
func trust(t *testing.T) (server *tls.Config, origin, stranger *tls.Config, originKey [32]byte) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated remote inference CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	if ca, err = x509.ParseCertificate(der); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	issue := func(serial int64, usage x509.ExtKeyUsage) (tls.Certificate, [32]byte) {
		p, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		c := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "runtime"}, DNSNames: []string{"lab.runtime.test"},
			NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		b, err := x509.CreateCertificate(rand.Reader, c, ca, p, key)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := x509.ParseCertificate(b)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{b}, PrivateKey: k}, sha256.Sum256(parsed.RawSubjectPublicKeyInfo)
	}
	serverCert, _ := issue(2, x509.ExtKeyUsageServerAuth)
	originCert, originKey := issue(3, x509.ExtKeyUsageClientAuth)
	strangerCert, _ := issue(4, x509.ExtKeyUsageClientAuth)
	client := func(c tls.Certificate) *tls.Config {
		return &tls.Config{Certificates: []tls.Certificate{c}, RootCAs: roots, ServerName: "lab.runtime.test"}
	}
	return &tls.Config{Certificates: []tls.Certificate{serverCert}, ClientCAs: roots}, client(originCert), client(strangerCert), originKey
}

// upstream is the remote runtime's hosted provider: it lists one model and
// streams a reply only to a request carrying the remote's credential.
type upstream struct {
	*httptest.Server
	mu    sync.Mutex
	chats []string
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, `{"data":[{"id":%q}]}`, model)
			return
		}
		u.mu.Lock()
		u.chats = append(u.chats, r.Header.Get("Authorization"))
		u.mu.Unlock()
		for _, c := range []string{"Hello ", "from ", "the lab"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", c)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(u.Close)
	return u
}

// records collects a provider's records.
type records struct {
	mu   sync.Mutex
	list []inference.Record
}

func (r *records) add(rec inference.Record) { r.mu.Lock(); r.list = append(r.list, rec); r.mu.Unlock() }
func (r *records) all() []inference.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]inference.Record(nil), r.list...)
}

// A local runtime registers another runtime as an oa-remote@1 host under
// explicit mutual-TLS trust. Its Hosts reading comes from the remote fixture
// runtime and is attributed to the remote's domain; a chat delegated there is
// served with the remote's credential, which the local runtime never holds;
// the remote's audit records the local program as a claim attributed to the
// certificate's namespace, and the local audit records the delegation with
// the remote's domain. An unmapped certificate is refused.
func TestRemoteRuntimeHostsDelegationAndAttributedClaim(t *testing.T) {
	serverTLS, originTLS, strangerTLS, originKey := trust(t)
	up := newUpstream(t)

	// The remote fixture runtime: a hosted host whose credential its own holder
	// applies for the subject the certificate maps to.
	remoteRouter := router.New(router.NewHosted("openrouter", up.URL+"/api/v1", router.WireOpenAICompatible, "openrouter"))
	var applyMu sync.Mutex
	var applied []string
	holder := func(subject inference.Subject, name string) (map[string]string, string) {
		applyMu.Lock()
		defer applyMu.Unlock()
		applied = append(applied, subject.Account+" "+subject.Program+" "+name)
		if name != "openrouter" {
			return nil, "unknown"
		}
		return map[string]string{"Authorization": "Bearer " + secret}, "applied"
	}
	remoteRouter.UseCredentials(func(_ context.Context, _, name, _ string) (map[string]string, error) {
		headers, outcome := holder(inference.Subject{Account: "remote-runtime", Program: remoteProgram}, name)
		if outcome != "applied" {
			return nil, &router.CredentialRefusal{Outcome: outcome, Name: name}
		}
		return headers, nil
	})
	remoteRouter.Survey()
	remoteRecords := &records{}
	remoteProvider, err := inference.New(inference.Config{Router: remoteRouter, Record: remoteRecords.add,
		Decide: func(_ context.Context, s inference.Subject, action, resource string) (string, error) {
			if s.Account == namespace && s.Program == remoteProgram && action == inference.ActionComplete && resource == "host:openrouter" {
				return "permitted", nil
			}
			return "not_granted", nil
		},
		Apply: func(_ context.Context, s inference.Subject, _, name, _ string) (map[string]string, string) { return holder(s, name) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remoteProvider.Close() })
	authorize := func(_ context.Context, peer remote.Peer) (inference.Subject, string, error) {
		if peer.Key != originKey {
			return inference.Subject{}, "", errors.New("unmapped certificate key")
		}
		return inference.Subject{Account: namespace, Program: remoteProgram}, namespace, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- (remote.Server{TLS: serverTLS, Handler: service.RemoteHandler(remoteProvider, remoteRouter, authorize), Timeout: 30 * time.Second,
			MaxFrame: 8 << 20, MaxConnections: 16}).Serve(ctx, listener)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-served; !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	address := listener.Addr().String()

	// The origin runtime: one oa-remote@1 host and no credential of its own.
	lab, err := router.NewRemote("lab", address, originTLS, "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	localRouter := router.New(lab)
	localRouter.Survey()
	states, _, _, _, _ := localRouter.Residency(false)
	byHost := map[string]router.HostState{}
	for _, s := range states {
		byHost[s.Host] = s
	}
	if s := byHost["lab"]; !s.Up || s.Wire != router.WireRemote || s.Domain != "lab" || s.Installed != 1 {
		t.Fatalf("remote host %+v (all %+v)", s, states)
	}
	if s := byHost["lab/openrouter"]; !s.Up || s.Domain != "lab" || !s.Hosted || s.Credential != "openrouter" || s.Installed != 1 {
		t.Fatalf("the remote's host through Hosts %+v (all %+v)", s, states)
	}

	localRecords := &records{}
	var localApplies int
	localProvider, err := inference.New(inference.Config{Router: localRouter, Record: localRecords.add,
		Decide: func(_ context.Context, s inference.Subject, action, resource string) (string, error) {
			if s.Program == localProgram && resource == "host:lab" {
				return "permitted", nil
			}
			return "not_granted", nil
		},
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			localApplies++
			return nil, "unknown"
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { localProvider.Close() })
	caller := inference.Subject{Account: "origin-user", Program: localProgram}
	admission := localProvider.Start(context.Background(), caller, wire.Request{Model: model, Credential: "openrouter",
		Guarantees: []wire.RequestGuarantee{wire.RequestGuaranteeHostedAllowed},
		Messages:   []wire.Message{{Role: wire.RoleUser, Parts: []wire.Part{{Kind: wire.PartKindText, Text: "hello"}}}}})
	if admission.Outcome != wire.StartOutcomeAccepted || admission.Host != "lab" {
		t.Fatalf("admission %+v", admission)
	}
	var text strings.Builder
	var end *wire.Reply
	for cursor := int64(0); end == nil; {
		page := localProvider.Observe(context.Background(), caller, admission.Operation, cursor, 256, 65536, 5000)
		if page.Outcome != wire.PageOutcomePage {
			t.Fatalf("observe %+v", page)
		}
		for _, d := range page.Deltas {
			if d.Part != nil {
				text.WriteString(d.Part.Text)
			}
			if d.End != nil {
				end = d.End
			}
		}
		cursor = page.Next
	}
	if end.Outcome != wire.ReplyOutcomeCompleted || text.String() != "Hello from the lab" || end.Usage.Output != 3 {
		t.Fatalf("reply %+v text %q", end, text.String())
	}
	up.mu.Lock()
	chats := append([]string(nil), up.chats...)
	up.mu.Unlock()
	if len(chats) != 1 || chats[0] != "Bearer "+secret {
		t.Fatalf("upstream chats %q", chats)
	}
	applyMu.Lock()
	sawMapped := false
	for _, a := range applied {
		sawMapped = sawMapped || a == namespace+" "+remoteProgram+" openrouter"
	}
	applyMu.Unlock()
	if !sawMapped || localApplies != 0 {
		t.Fatalf("remote applies %v, local applies %d", applied, localApplies)
	}

	// The audits: the remote records the claim attributed to the mapped domain,
	// and the local runtime records the delegation to the remote's domain.
	eventuallyRecord := func(r *records, match func(inference.Record) bool) inference.Record {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			for _, rec := range r.all() {
				if match(rec) {
					return rec
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("no matching record in %+v", r.all())
		return inference.Record{}
	}
	remoteRec := eventuallyRecord(remoteRecords, func(r inference.Record) bool { return r.Outcome == "completed" })
	if remoteRec.Route != inference.RouteRemote || remoteRec.Account != namespace || remoteRec.Program != remoteProgram ||
		remoteRec.Claim != localProgram || remoteRec.Domain != namespace || remoteRec.Host != "openrouter" || remoteRec.Credential != "openrouter" {
		t.Fatalf("remote record %+v", remoteRec)
	}
	localRec := eventuallyRecord(localRecords, func(r inference.Record) bool { return r.Outcome == "completed" })
	if localRec.Program != localProgram || localRec.Host != "lab" || localRec.Domain != "lab" || localRec.Claim != "" || localRec.Credential != "openrouter" {
		t.Fatalf("local record %+v", localRec)
	}
	for _, rec := range append(remoteRecords.all(), localRecords.all()...) {
		if strings.Contains(fmt.Sprintf("%+v", rec), secret) {
			t.Fatalf("a record carries the secret: %+v", rec)
		}
	}

	// A certificate the remote does not map reads nothing.
	stranger, err := router.NewRemote("stranger", address, strangerTLS, "openrouter")
	if err != nil {
		t.Fatal(err)
	}
	strangers := router.New(stranger)
	strangers.Survey()
	if states, _, _, _, _ := strangers.Residency(false); len(states) != 1 || states[0].Up {
		t.Fatalf("an unmapped certificate read %+v", states)
	}
}
