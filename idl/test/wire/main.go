package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	job "github.com/openabstractions/abstraction-job/go"
	rec "idl/out/go/rec"
)

func proposal() job.Record {
	return job.Record{
		Kind:  "download",
		State: job.StatePending,
		Spec:  json.RawMessage(`{"artifact":{"bytes":23068672}}`),
	}
}

func script(s job.Store) []string {
	var lines []string
	say := func(op string, err error, detail string) {
		v := verdictOf(err)
		if v == "" {
			v = "ok"
		}
		lines = append(lines, op+"\t"+v+"\t"+detail)
	}

	id, err := s.Submit(proposal())
	say("submit", err, strconv.FormatBool(id != ""))

	r, err := s.Load(id)
	say("load", err, state(r))

	all, err := s.List()
	say("list", err, strconv.Itoa(len(all)))

	free, err := s.Orphans()
	say("orphans", err, strconv.Itoa(len(free)))

	if r != nil {
		say("claimable", nil, strconv.FormatBool(s.Claimable(r)))
	}

	r, err = s.Claim(id, "w1", minute)
	say("claim", err, epoch(r))
	held := epochOf(r)

	r, err = s.Renew(id, held, minute)
	say("renew", err, epoch(r))

	r, err = s.Update(id, held, func(x *job.Record) error {
		x.Progress.Done = 42
		return nil
	})
	say("write", err, done(r))

	r, err = s.SetIntent(id, job.WantCancel, "tester")
	say("set_intent", err, want(r))

	r, err = s.Recall(id, held, "needed elsewhere", "tester", halfMinute)
	say("recall", err, recallReason(r))

	say("release", s.Release(id, held), "")

	_, err = s.Load("no-such-id")
	say("load-missing", err, "")

	r, err = s.Claim(id, "w2", minute)
	say("reclaim", err, epoch(r))

	_, err = s.Claim(id, "w3", minute)
	say("claim-held", err, "")

	_, err = s.Renew(id, 9999, minute)
	say("renew-stale", err, "")

	return lines
}

const (
	minute     = 60000000000
	halfMinute = 30000000000
)

func state(r *job.Record) string {
	if r == nil {
		return ""
	}
	return string(r.State)
}

func epochOf(r *job.Record) int64 {
	if r == nil {
		return 0
	}
	return r.Lease.Epoch
}

func epoch(r *job.Record) string { return strconv.FormatInt(epochOf(r), 10) }

func done(r *job.Record) string {
	if r == nil {
		return ""
	}
	return strconv.FormatInt(r.Progress.Done, 10)
}

func want(r *job.Record) string {
	if r == nil || r.Intent == nil {
		return ""
	}
	return string(r.Intent.Want)
}

func recallReason(r *job.Record) string {
	if r == nil || r.Lease.Recall == nil {
		return ""
	}
	return r.Lease.Recall.Reason
}

func listen() net.Listener {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	return ln
}

func storeIn(root, name string) job.Store {
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	s, err := job.NewFileStore(dir)
	if err != nil {
		panic(err)
	}
	return s
}

// unknownOperation is the one exchange no Store interface can express, so it is
// asked directly. A peer that has never heard of an operation must say so in
// the word the definition declares rather than closing the connection.
func unknownOperation(address string) string {
	resp, err := (wirePeer{address: address}).do(&rec.Request{Op: "no-such-op"})
	if resp == nil {
		return "no answer: " + err.Error()
	}
	return resp.Kind
}

// generatedDocumentAccepted sends a record the generated document encoder
// produced, through the generated envelope, to the hand-written server.
func generatedDocumentAccepted(address string) string {
	body := rec.Encode(&rec.Record{
		Content: []string{"abstraction.job/base@1"},
		Id:      "",
		Kind:    "download",
		State:   "pending",
		Spec:    `{"artifact":{"bytes":1}}`,
		Progress: rec.Progress{
			UpdatedAt: "2026-09-08T05:07:14.951609Z",
		},
		Lease:     rec.Lease{ExpiresAt: "2026-09-08T05:07:14.951609Z"},
		CreatedAt: "2026-09-08T05:07:14.951609Z",
		UpdatedAt: "2026-09-08T05:07:14.951609Z",
	})
	resp, err := (wirePeer{address: address}).do(&rec.Request{Op: "submit", Record: string(body)})
	if err != nil {
		return verdictOf(err) + ": " + resp.Error
	}
	return "accepted"
}

func report(name string, lines []string) {
	fmt.Printf("--- %s\n", name)
	for _, l := range lines {
		fmt.Println("    " + strings.ReplaceAll(l, "\t", "  "))
	}
}

type run struct {
	name  string
	lines []string
}

// otherLanguage runs a peer written in another language against a hand-written
// server this process is already listening on. The child's exit is what says it
// finished, so nothing here waits on a duration.
func otherLanguage(root string, argv []string) *run {
	ln := listen()
	go job.Serve(ln, storeIn(root, "other"))
	out := filepath.Join(root, "other-transcript.txt")
	cmd := exec.Command(argv[0], append(argv[1:], ln.Addr().String(), out)...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("UNPROVEN: %s did not finish: %v\n", argv[0], err)
		return nil
	}
	body, err := os.ReadFile(out)
	if err != nil {
		fmt.Printf("UNPROVEN: %s wrote no transcript: %v\n", argv[0], err)
		return nil
	}
	return &run{"generated " + argv[0] + " client -> hand-written server",
		strings.Split(strings.TrimRight(string(body), "\n"), "\n")}
}

func main() {
	root := os.Args[1]

	handwritten := listen()
	go job.Serve(handwritten, storeIn(root, "handwritten"))
	hw := handwritten.Addr().String()

	generated := listen()
	go serveGenerated(generated, storeIn(root, "generated"))
	gen := generated.Addr().String()

	control := listen()
	go job.Serve(control, storeIn(root, "control"))

	runs := []*run{
		{"generated client -> hand-written server", script(wirePeer{address: hw})},
		{"hand-written client -> generated server", script(job.NewRemoteStore("tcp", gen))},
		{"hand-written client -> hand-written server", script(job.NewRemoteStore("tcp", control.Addr().String()))},
	}
	if len(os.Args) > 2 {
		if other := otherLanguage(root, os.Args[2:]); other != nil {
			runs = append(runs, other)
		}
	}
	for _, t := range runs {
		report(t.name, t.lines)
	}
	fmt.Printf("unknown operation, hand-written server: %s\n", unknownOperation(hw))
	fmt.Printf("unknown operation, generated server:    %s\n", unknownOperation(gen))
	fmt.Printf("generated document to hand-written server: %s\n", generatedDocumentAccepted(hw))
	os.Exit(compare(runs))
}

// compare separates the two things a transcript can disagree about, because
// they belong to different documents and only one of them is this generator's.
// The operation asked and the verdict returned are the envelope; the third
// column is a value read back out of the record, so a peer that could not
// decode one disagrees there and nowhere else.
func compare(runs []*run) int {
	envelope, whole := 0, 0
	for _, t := range runs[1:] {
		for i, line := range t.lines {
			want := ""
			if i < len(runs[0].lines) {
				want = runs[0].lines[i]
			}
			if line == want {
				continue
			}
			whole++
			where := "document"
			if envelopeOf(line) != envelopeOf(want) {
				where = "envelope"
				envelope++
			}
			fmt.Printf("DISAGREES (%s): %s\n    wanted  %s\n    got     %s\n",
				where, t.name, tabless(want), tabless(line))
		}
	}
	fmt.Printf("%d exchanges, %d peer pairs, %d lines differ, %d of them in the envelope\n",
		len(runs[0].lines), len(runs), whole, envelope)
	return envelope
}

func envelopeOf(line string) string {
	if op, rest, ok := strings.Cut(line, "\t"); ok {
		verdict, _, _ := strings.Cut(rest, "\t")
		return op + "\t" + verdict
	}
	return line
}

func tabless(line string) string { return strings.ReplaceAll(line, "\t", "  ") }
