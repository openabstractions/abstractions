package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// pipeClientPy writes one prepared frame to the named pipe in argv[1] and
// prints the reply frame. The interpreter opens the pipe itself, so the
// runtime binds the interpreter's own signed image as the peer.
const pipeClientPy = `import struct, sys
data = open(sys.argv[2], 'rb').read()
def exactly(pipe, n):
    out = b''
    while len(out) < n:
        chunk = pipe.read(n - len(out))
        if not chunk:
            raise SystemExit('the runtime closed the pipe before its reply')
        out += chunk
    return out
with open(sys.argv[1], 'r+b', buffering=0) as pipe:
    pipe.write(struct.pack('>I', len(data)) + data)
    size = struct.unpack('>I', exactly(pipe, 4))[0]
    sys.stdout.buffer.write(exactly(pipe, size))
`

// systemPowerShell is Windows PowerShell, present on every Windows host and
// signed through a system catalog by Microsoft Windows.
const systemPowerShell = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`

// pipeClientPS writes the prepared frame in $env:OA_TEST_FRAME, behind its
// four-byte big-endian length, to the named pipe $env:OA_TEST_PIPE through a
// .NET NamedPipeClientStream, and writes the reply frame's body to stdout.
// PowerShell opens the pipe itself, so the runtime binds powershell.exe's
// own catalog-signed image as the peer.
const pipeClientPS = `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$name = $env:OA_TEST_PIPE.Substring(9)
$data = [IO.File]::ReadAllBytes($env:OA_TEST_FRAME)
$pipe = New-Object IO.Pipes.NamedPipeClientStream('.', $name, [IO.Pipes.PipeDirection]::InOut)
$pipe.Connect(30000)
function Exactly([int]$n) {
  $buf = New-Object byte[] $n
  $got = 0
  while ($got -lt $n) {
    $r = $pipe.Read($buf, $got, $n - $got)
    if ($r -le 0) { throw 'the runtime closed the pipe before its reply' }
    $got += $r
  }
  return ,$buf
}
$frame = New-Object byte[] (4 + $data.Length)
$frame[0] = ($data.Length -shr 24) -band 255
$frame[1] = ($data.Length -shr 16) -band 255
$frame[2] = ($data.Length -shr 8) -band 255
$frame[3] = $data.Length -band 255
[Array]::Copy($data, 0, $frame, 4, $data.Length)
$pipe.Write($frame, 0, $frame.Length)
$pipe.Flush()
$h = Exactly 4
$size = ([int]$h[0] -shl 24) -bor ([int]$h[1] -shl 16) -bor ([int]$h[2] -shl 8) -bor [int]$h[3]
$reply = Exactly $size
$out = [Console]::OpenStandardOutput()
$out.Write($reply, 0, $reply.Length)
$out.Flush()
$pipe.Dispose()
`

// encodedCommand is script as powershell.exe -EncodedCommand takes it:
// UTF-16LE, base64.
func encodedCommand(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 2*len(units))
	for i, u := range units {
		raw[2*i], raw[2*i+1] = byte(u), byte(u>>8)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// frameCapture keeps the frame a generated client sends and answers with
// reply, so a request can be encoded here and its reply decoded here while
// another process carries both.
type frameCapture struct{ frame, reply []byte }

func (c *frameCapture) ExchangeFrame(frame []byte) ([]byte, error) {
	c.frame = append([]byte(nil), frame...)
	if c.reply == nil {
		return nil, errors.New("captured")
	}
	return c.reply, nil
}

func publisherRequest() iwire.Request {
	return iwire.Request{Model: "anthropic/claude-sonnet-5", Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Credential: "openrouter",
		Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "hi"}}}}}
}

// publisherRuntime is the runtime's inference decision over a native
// inference endpoint: complete is not granted, and each refusal admits a
// first-use question into book. It keeps the peer each decision carried.
type publisherRuntime struct {
	r        *runtimeRights
	book     *asks.Book
	endpoint string
	account  string
	mu       sync.Mutex
	peers    []*identity.Peer
}

func newPublisherRuntime(t *testing.T) *publisherRuntime {
	t.Helper()
	if err := identity.CanEver(listen.Program); err != nil || runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[{"id":"anthropic/claude-sonnet-5"}]}`))
	}))
	t.Cleanup(upstream.Close)
	routes := router.New(router.NewHosted("openrouter", upstream.URL+"/api/v1", router.WireOpenAICompatible, "openrouter"))
	routes.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) {
		return map[string]string{"Authorization": "Bearer fixture"}, nil
	})
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "rights.json"), firstUseActions)
	if err != nil {
		t.Fatal(err)
	}
	a, book := firstUseBook(t)
	p := &publisherRuntime{r: &runtimeRights{policy: policy, owner: account.Uid, firstUse: a.admitFirstUse}, book: book, account: account.Uid}
	provider, err := inference.New(inference.Config{Router: routes,
		Decide: func(ctx context.Context, s inference.Subject, action, resource string) (string, error) {
			p.mu.Lock()
			p.peers = append(p.peers, inference.PeerFromContext(ctx))
			p.mu.Unlock()
			return p.r.decideInference(ctx, s, action, resource)
		},
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "not_permitted"
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.Close() })
	if runtime.GOOS == "windows" {
		p.endpoint = fmt.Sprintf(`\\.\pipe\oa-publisher-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	} else {
		dir, err := os.MkdirTemp("/tmp", "oa-publisher-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		p.endpoint = filepath.Join(dir, "s")
	}
	h, err := inferenceservice.Listen(p.endpoint, provider)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- h.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-served })
	return p
}

// decided returns the one peer the runtime decided with and the subject it
// binds.
func (p *publisherRuntime) decided(t *testing.T) (*identity.Peer, inference.Subject) {
	t.Helper()
	p.mu.Lock()
	peers := append([]*identity.Peer(nil), p.peers...)
	p.mu.Unlock()
	if len(peers) != 1 || peers[0] == nil {
		t.Fatalf("the native route decided with peers %v", peers)
	}
	bound, err := inferenceservice.SubjectFromPeer(peers[0])
	if err != nil {
		t.Fatal(err)
	}
	return peers[0], bound
}

// expectQuestion requires one question about program carrying want.
func (p *publisherRuntime) expectQuestion(t *testing.T, program string, want asks.Publisher) {
	t.Helper()
	records, _, err := p.book.ApplicationHistory()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range records {
		if q.About != program {
			continue
		}
		if q.Publisher != want.Subject || q.PublisherTrusted != want.Trusted {
			t.Fatalf("the question about %s must carry publisher %+v: %+v", program, want, q)
		}
		return
	}
	t.Fatalf("no first-use question about %s: %+v", program, records)
}

// TestInferenceFirstUseRecordsTheNativePeersCode: an inference first-use
// question admitted through the native route records the publisher read from
// the code of the peer the route bound (ASK-A6). That peer is evidence only
// for the subject it binds: offered for another program or account, or
// absent, it gives the decision no peer. The caller is this test program, so
// the case runs wherever Program proof does.
func TestInferenceFirstUseRecordsTheNativePeersCode(t *testing.T) {
	p := newPublisherRuntime(t)
	native := iwire.NewChatClient(listen.FrameClient{Endpoint: p.endpoint}.WithDefaults(35*time.Second, inferenceservice.MaxFrameBytes))
	if admission, err := native.Start(publisherRequest()); err != nil || admission.Outcome != iwire.StartOutcomeNotPermitted || admission.Reason != "rights:not_granted" {
		t.Fatalf("native start: %+v %v", admission, err)
	}
	peer, bound := p.decided(t)
	want := asks.Publisher{}
	if code, proof := peer.Code.Get(); proof > identity.ProofNone {
		want = asks.CodePublisher(code)
	}
	p.expectQuestion(t, bound.Program, want)

	subject := rwire.Subject{Account: bound.Account, Program: bound.Program}
	carried := inference.WithPeer(context.Background(), peer)
	if boundPeer(carried, subject) != peer {
		t.Fatal("the peer was not taken for the subject it binds")
	}
	for _, forged := range []rwire.Subject{
		{Account: bound.Account, Program: filepath.Join(t.TempDir(), "other.exe")},
		{Account: bound.Account + "-other", Program: bound.Program},
	} {
		if boundPeer(carried, forged) != nil {
			t.Fatalf("a peer bound for %+v was taken as evidence for %+v", subject, forged)
		}
	}
	if boundPeer(context.Background(), subject) != nil || boundPeer(inference.WithPeer(carried, nil), subject) != nil {
		t.Fatal("a decision without a peer was given one")
	}
}

// TestInferenceFirstUseRecordsASignedCallersPublisher: a signed caller opens
// the runtime's pipe itself and writes a chat@1 Start frame; its first-use
// question names the caller's publisher, and the same peer offered for
// another program records none (ASK-A6). The caller is Windows PowerShell,
// whose catalog signer, Microsoft Windows, every Windows host carries.
func TestInferenceFirstUseRecordsASignedCallersPublisher(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the signed native caller is arranged over a Windows named pipe")
	}
	p := newPublisherRuntime(t)
	var capture frameCapture
	iwire.NewChatClient(&capture).Start(publisherRequest())
	dir := t.TempDir()
	framePath := filepath.Join(dir, "start.frame")
	if err := os.WriteFile(framePath, capture.frame, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(systemPowerShell, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodedCommand(pipeClientPS))
	cmd.Env = append(os.Environ(), "OA_TEST_PIPE="+p.endpoint, "OA_TEST_FRAME="+framePath)
	// PowerShell writes progress records to a redirected stderr; they are
	// shown only when the client fails.
	var stderr strings.Builder
	cmd.Stderr = &stderr
	reply, err := cmd.Output()
	if err != nil {
		t.Fatalf("powershell pipe client: %v\n%s", err, stderr.String())
	}
	capture.reply = reply
	if admission, err := iwire.NewChatClient(&capture).Start(publisherRequest()); err != nil || admission.Outcome != iwire.StartOutcomeNotPermitted || admission.Reason != "rights:not_granted" {
		t.Fatalf("native start from powershell: %+v %v", admission, err)
	}
	peer, bound := p.decided(t)
	if !strings.EqualFold(bound.Program, systemPowerShell) {
		t.Fatalf("the runtime bound %s, not %s", bound.Program, systemPowerShell)
	}
	code, proof := peer.Code.Get()
	want := asks.CodePublisher(code)
	if proof <= identity.ProofNone || !want.Trusted || want.Subject != "Microsoft Windows" {
		t.Fatalf("powershell.exe must bind trusted code signed by Microsoft Windows: %+v at %s", code, proof)
	}
	p.expectQuestion(t, bound.Program, want)

	other := inference.Subject{Account: p.account, Program: filepath.Join(dir, "other.exe")}
	if word, _ := p.r.decideInference(inference.WithPeer(context.Background(), peer), other, inference.ActionComplete, inference.ResourceHost("openrouter")); word != "not_granted" {
		t.Fatalf("forged-peer decision %q", word)
	}
	p.expectQuestion(t, other.Program, asks.Publisher{})
}

// runPipePython has the interpreter write frame to the named pipe endpoint
// itself and returns the reply frame it printed.
func runPipePython(t *testing.T, pythonExe string, pythonArgs []string, endpoint string, frame []byte) []byte {
	t.Helper()
	dir := t.TempDir()
	framePath, scriptPath := filepath.Join(dir, "request.frame"), filepath.Join(dir, "pipe_client.py")
	if err := os.WriteFile(framePath, frame, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte(pipeClientPy), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(pythonExe, append(pythonArgs, scriptPath, endpoint, framePath)...)
	cmd.Stderr = os.Stderr
	reply, err := cmd.Output()
	if err != nil {
		t.Fatalf("python pipe client: %v", err)
	}
	return reply
}

// signedPythonPublisher is pythonPublisher for an interpreter whose code names
// its signer. It skips where the interpreter embeds no signer name.
func signedPythonPublisher(t *testing.T) (string, []string, string, asks.Publisher) {
	t.Helper()
	pythonExe, pythonArgs, program, want := pythonPublisher(t)
	if want.Subject == "" {
		t.Skipf("the Python interpreter %s carries no embedded signer name", program)
	}
	return pythonExe, pythonArgs, program, want
}

// pythonPublisher binds the Python interpreter on PATH over a named pipe of
// its own, as a runtime route binds it, and returns the interpreter, the
// program it binds as and the publisher its code names, which is empty for an
// unsigned interpreter. It skips outside Windows.
func pythonPublisher(t *testing.T) (string, []string, string, asks.Publisher) {
	t.Helper()
	pythonExe, pythonArgs, peer := pythonPipePeer(t)
	subject, err := inferenceservice.SubjectFromPeer(peer)
	if err != nil {
		t.Fatal(err)
	}
	want := asks.Publisher{}
	if code, proof := peer.Code.Get(); proof > identity.ProofNone {
		want = asks.CodePublisher(code)
	}
	return pythonExe, pythonArgs, subject.Program, want
}

// pythonPipePeer is the peer a named pipe binds for the Python interpreter on
// PATH when it opens the pipe itself. It skips outside Windows.
func pythonPipePeer(t *testing.T) (string, []string, *identity.Peer) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("the signed caller is arranged over a Windows named pipe")
	}
	if err := identity.CanEver(listen.Program); err != nil {
		t.Skip("Program proof unavailable")
	}
	pythonExe, pythonArgs, _ := scriptPython(t)
	endpoint := fmt.Sprintf(`\\.\pipe\oa-python-peer-%d-%d`, os.Getpid(), time.Now().UnixNano())
	l, err := listen.ListenFramed(endpoint, listen.Program)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	bound := make(chan *identity.Peer, 1)
	go func() {
		defer close(bound)
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		call, err := listen.ReceiveFramed(context.Background(), conn, listen.Program, 1<<10)
		if err != nil {
			return
		}
		defer call.Close()
		if peer, err := call.Peer(); err == nil {
			bound <- peer
		}
		//unchecked: the peer is already taken; the client only waits for any reply
		call.Reply([]byte("bound"))
	}()
	runPipePython(t, pythonExe, pythonArgs, endpoint, []byte("bind"))
	peer := <-bound
	if peer == nil {
		t.Fatal("the pipe did not bind the Python interpreter")
	}
	return pythonExe, pythonArgs, peer
}

// questionPublisher reads the publisher a running runtime recorded for
// question id.
func questionPublisher(t *testing.T, options runtimeFlags, id string) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	operator, err := client.NewVerified(options.endpoint, runtimeExpectation(t)).ResolveAsksOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	attributedRead, err := operator.ReadAttributedQuestionContext(ctx, id)
	read := attributedRead.Question
	if err != nil || read.Outcome.String() != "found" {
		t.Fatalf("read question %s: %+v %v", id, read, err)
	}
	return read.Publisher, read.PublisherTrusted
}
