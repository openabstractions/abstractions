//go:build ignore

// Live voice latency probe. All endpoints and upstreams are isolated fixtures.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iw "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	iservice "github.com/openabstractions/abstraction-inference/go/service"
	router "github.com/openabstractions/abstraction-router/go"
	probe "github.com/openabstractions/abstractions/conformance/clients/fixture/livevoice/wire/go/oa/test/livevoice"
)

type slot struct {
	ready chan struct{}
	audio []byte
}
type backend struct {
	mu    sync.Mutex
	slots map[int64]*slot
	url   string
}

func (b *backend) at(n int64) (*slot, error) {
	if n < 0 || n >= 10000 {
		return nil, errors.New("sequence outside probe bound")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.slots[n]; s != nil {
		return s, nil
	}
	s := &slot{ready: make(chan struct{})}
	b.slots[n] = s
	return s, nil
}
func (b *backend) Append(f probe.Frame) (probe.Sample, error) {
	if len(f.Audio) != 6400 {
		return probe.Sample{}, errors.New("expected 200 ms, 16 kHz mono PCM16")
	}
	s, err := b.at(f.Sequence)
	if err != nil {
		return probe.Sample{}, err
	}
	started := time.Now()
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Post(b.url, "application/octet-stream", bytes.NewReader(f.Audio))
	if err != nil {
		return probe.Sample{}, err
	}
	defer resp.Body.Close()
	audio, err := io.ReadAll(io.LimitReader(resp.Body, 6401))
	wait := time.Since(started).Nanoseconds()
	if err != nil || resp.StatusCode != 200 || !bytes.Equal(audio, f.Audio) {
		return probe.Sample{}, errors.New("fake upstream changed audio")
	}
	b.mu.Lock()
	select {
	case <-s.ready:
		b.mu.Unlock()
		return probe.Sample{}, errors.New("duplicate frame")
	default:
	}
	s.audio = audio
	close(s.ready)
	b.mu.Unlock()
	return probe.Sample{Sequence: f.Sequence, Audio: []byte{}, WaitNs: wait}, nil
}
func (b *backend) Observe(n int64) (probe.Sample, error) {
	s, err := b.at(n)
	if err != nil {
		return probe.Sample{}, err
	}
	started := time.Now()
	select {
	case <-s.ready:
	case <-time.After(3 * time.Second):
		return probe.Sample{}, errors.New("frame timeout")
	}
	wait := time.Since(started).Nanoseconds()
	return probe.Sample{Sequence: n, Audio: s.audio, WaitNs: wait}, nil
}

func serve(endpoint, chatEndpoint string, delay time.Duration) error {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		audio, err := io.ReadAll(io.LimitReader(r.Body, 6401))
		if err != nil || len(audio) != 6400 {
			http.Error(w, "bad fixture frame", 400)
			return
		}
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write(audio)
	}))
	defer echo.Close()
	b := &backend{slots: make(map[int64]*slot), url: echo.URL}
	// Actual chat provider runs alongside the voice probe and streams until cancellation.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			fmt.Fprint(w, `{"data":[{"id":"probe-model"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"probe token\"}}]}\n\n")
				w.(http.Flusher).Flush()
			}
		}
	}))
	defer upstream.Close()
	route := router.New(router.NewHosted("probe", upstream.URL, router.WireOpenAICompatible, "probe"))
	route.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) { return nil, nil })
	route.Survey()
	p, err := inference.New(inference.Config{Router: route, Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "permitted", nil }, Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
		return nil, "applied"
	}})
	if err != nil {
		return err
	}
	defer p.Close()
	h, err := iservice.Listen(chatEndpoint, p)
	if err != nil {
		return err
	}
	defer h.Close()
	go h.Serve(context.Background())
	listener, err := listen.Listen(endpoint)
	if err != nil {
		return err
	}
	defer listener.Close()
	fmt.Println("READY")
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
			if call != nil {
				defer call.Close()
			}
			if err != nil {
				return
			}
			time.Sleep(delay)
			reply, err := probe.ServeEndpoint(call.Frame, "live-probe", "", &probe.VoiceProbeDispatcher{Handler: b})
			if err == nil {
				_ = call.Reply(reply)
			} else {
				fmt.Fprintln(os.Stderr, err)
			}
		}()
	}
}

func chat(ctx context.Context, endpoint string, done chan<- error) {
	c := iw.NewChatClient(listen.FrameClient{Endpoint: endpoint, Timeout: 5 * time.Second})
	a, err := c.Start(iw.Request{Model: "probe-model", Credential: "probe", Guarantees: []iw.RequestGuarantee{iw.RequestGuaranteeHostedAllowed}, Messages: []iw.Message{{Role: iw.RoleUser, Parts: []iw.Part{{Kind: iw.PartKindText, Text: "stream"}}}}})
	if err != nil {
		done <- err
		return
	}
	if a.Outcome != iw.StartOutcomeAccepted {
		done <- fmt.Errorf("chat refused: %+v", a)
		return
	}
	defer c.Cancel(a.Operation)
	cursor := int64(0)
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		default:
		}
		page, err := c.Observe(a.Operation, cursor, 32, 65536, 200)
		if err != nil {
			done <- err
			return
		}
		if page.Outcome != iw.PageOutcomePage {
			done <- fmt.Errorf("chat page %s", page.Outcome)
			return
		}
		cursor = page.Next
	}
}
func run(endpoint, chatEndpoint string, count, warmup int) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go chat(ctx, chatEndpoint, done)
	audio := bytes.Repeat([]byte{1, 2}, 3200)
	appendTimes := make([]float64, 0, count)
	observeTimes := make([]float64, 0, count)
	errs := make(chan error, 1)
	go func() {
		c := probe.NewVoiceProbeClient(listen.FrameClient{Endpoint: endpoint, Timeout: 5 * time.Second})
		for i := 0; i < count+warmup; i++ {
			began := time.Now()
			s, e := c.Observe(int64(i))
			elapsed := time.Since(began).Nanoseconds() - s.WaitNs
			if e != nil {
				errs <- e
				return
			}
			if s.Sequence != int64(i) || !bytes.Equal(s.Audio, audio) {
				errs <- errors.New("observe frame mismatch")
				return
			}
			if i >= warmup {
				observeTimes = append(observeTimes, float64(elapsed)/1e6)
			}
		}
		errs <- nil
	}()
	c := probe.NewVoiceProbeClient(listen.FrameClient{Endpoint: endpoint, Timeout: 5 * time.Second})
	start := time.Now()
	for i := 0; i < count+warmup; i++ {
		time.Sleep(time.Until(start.Add(time.Duration(i) * 200 * time.Millisecond)))
		began := time.Now()
		s, e := c.Append(probe.Frame{Sequence: int64(i), Audio: audio})
		elapsed := time.Since(began).Nanoseconds() - s.WaitNs
		if e != nil {
			return fmt.Errorf("append: %w", e)
		}
		if s.Sequence != int64(i) {
			return errors.New("append sequence mismatch")
		}
		if i >= warmup {
			appendTimes = append(appendTimes, float64(elapsed)/1e6)
		}
	}
	if err := <-errs; err != nil {
		return fmt.Errorf("observe: %w", err)
	}
	cancel()
	if err := <-done; err != nil {
		return fmt.Errorf("chat: %w", err)
	}
	return report("go", appendTimes, observeTimes)
}
func report(language string, a, o []float64) error {
	result := map[string]any{"language": language, "frames": len(a), "frame_ms": 200, "chat_concurrent": true}
	var failure error
	for name, s := range map[string][]float64{"append": a, "observe": o} {
		sort.Float64s(s)
		p99 := s[(len(s)*99+99)/100-1]
		result[name+"_p99_ms"] = p99
		result[name+"_max_ms"] = s[len(s)-1]
		if p99 >= 200 {
			failure = fmt.Errorf("%s p99 %.3f ms exceeds 200 ms", name, p99)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return err
	}
	return failure
}
func main() {
	mode := flag.String("mode", "", "server or go client")
	ep := flag.String("endpoint", "", "isolated voice endpoint")
	cp := flag.String("chat-endpoint", "", "isolated chat endpoint")
	n := flag.Int("frames", 100, "measured 200ms frames")
	w := flag.Int("warmup", 5, "unmeasured frames")
	delay := flag.Int("fault-delay-ms", 0, "inject dispatch delay to demonstrate gate failure")
	flag.Parse()
	if *mode == "" {
		flag.PrintDefaults()
		return
	}
	if *n < 1 || *n+*w > 9999 || *w < 0 {
		fmt.Fprintln(os.Stderr, "invalid frame count")
		os.Exit(2)
	}
	var err error
	if *mode == "server" {
		err = serve(*ep, *cp, time.Duration(*delay)*time.Millisecond)
	} else if *mode == "go" {
		err = run(*ep, *cp, *n, *w)
	} else {
		err = errors.New("unknown mode")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
