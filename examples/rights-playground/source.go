package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// These are deliberately fake fixture bytes. Only setup and the receiver use
// them; they are never accepted from owner state or emitted in receipts.
type fixture struct {
	label    string
	name     string
	secret   []byte
	artifact []byte
}

func fixtureFor(label string) (fixture, error) {
	switch label {
	case "alpha":
		return fixture{label, "oa-playground-alpha", []byte("OA_DUMMY_ALPHA_628174_THIS_IS_NOT_A_REAL_SECRET"), []byte("Dummy alpha service access succeeded.\n")}, nil
	case "beta":
		return fixture{label, "oa-playground-beta", []byte("OA_DUMMY_BETA_947263_THIS_IS_NOT_A_REAL_SECRET"), []byte("Dummy beta service access succeeded.\n")}, nil
	default:
		return fixture{}, errors.New("this fixture accepts only alpha or beta")
	}
}

type receiver struct {
	mu       sync.Mutex
	requests map[string]int
	accepted map[string]int
}

func newReceiver() *receiver {
	return &receiver{requests: map[string]int{}, accepted: map[string]int{}}
}

func (s *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/stats" {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"requests": s.requests, "accepted": s.accepted, "data": "dummy fixture counters only"})
		return
	}
	label := r.URL.Path[1:]
	f, err := fixtureFor(label)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	s.requests[label]++
	s.mu.Unlock()
	expected := "Bearer " + string(f.secret)
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) != 1 {
		http.Error(w, "dummy credential required", http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	s.accepted[label]++
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(len(f.artifact)))
	_, _ = w.Write(f.artifact)
}

func serveSource(readyFile string, out io.Writer) error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	ready, err := json.Marshal(map[string]string{"origin": "http://" + listener.Addr().String()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(readyFile), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(readyFile, ready, 0o600); err != nil {
		return err
	}
	fmt.Fprintln(out, "dummy receiver ready:", listener.Addr())
	server := &http.Server{Handler: newReceiver(), ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, MaxHeaderBytes: 8 << 10}
	return server.Serve(listener)
}

func sourceStats(ctx context.Context, origin string, out io.Writer) error {
	if _, err := loopbackOrigin(origin); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/stats", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("fixture redirects refused") }}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("dummy receiver stats unavailable")
	}
	_, err = io.Copy(out, io.LimitReader(response.Body, 4096))
	return err
}
