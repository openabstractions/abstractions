package delegation_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	download "github.com/openabstractions/abstraction-download/go"
)

// This is an isolated external-provider fixture protocol. Only the Go service
// receives its endpoint, TLS trust and credential; the application uses job IPC.
type remoteAdapter struct {
	*adapter
	endpoint, token string
	client          *http.Client
	unknown         <-chan struct{}
}

func remoteBackend(t *testing.T, e *external) *remoteAdapter {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret)
	unknown := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/start":
			if r.Method != http.MethodPost {
				http.Error(w, "method", 405)
				return
			}
			_, _ = e.Start(r.Context(), download.Spec{Request: r.URL.Query().Get("id")}, 0)
			// Commit the single external effect, then lose the actual network reply.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
		case "/locate":
			id, err := e.Locate(r.Context(), r.URL.Query().Get("id"))
			if err != nil {
				select {
				case unknown <- struct{}{}:
				default:
				}
				http.Error(w, "unknown", http.StatusConflict)
				return
			}
			fmt.Fprint(w, id)
		case "/poll":
			if r.URL.Query().Get("id") != "external-1" {
				http.Error(w, "unknown", 404)
				return
			}
			fmt.Fprint(w, len(e.body))
		case "/result":
			if r.URL.Query().Get("id") != "external-1" {
				http.Error(w, "unknown", 404)
				return
			}
			e.mu.Lock()
			e.deliveries++
			body := append([]byte(nil), e.body...)
			e.mu.Unlock()
			w.Write(body)
		default:
			http.Error(w, "unknown", 404)
		}
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 2 * time.Second
	r := &remoteAdapter{adapter: &adapter{e}, endpoint: server.URL, token: token, client: client, unknown: unknown}
	t.Cleanup(client.CloseIdleConnections)
	// Demonstrate that the endpoint alone does not authorize content access.
	response, err := client.Get(server.URL + "/result?id=external-1")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("remote credential bypass")
	}
	return r
}

func (r *remoteAdapter) call(ctx context.Context, method, path, id string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.endpoint+path+"?id="+url.QueryEscape(id), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	response, err := r.client.Do(req)
	if err != nil {
		return nil, download.ErrOutcomeUnknown
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, download.ErrOutcomeUnknown
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(body) > 65536 {
		return nil, download.ErrOutcomeUnknown
	}
	return body, nil
}

func (r *remoteAdapter) Start(ctx context.Context, spec download.Spec, _ int64) (string, error) {
	body, err := r.call(ctx, http.MethodPost, "/start", spec.Request)
	return string(body), err
}
func (r *remoteAdapter) Locate(ctx context.Context, id string) (string, error) {
	body, err := r.call(ctx, http.MethodGet, "/locate", id)
	return strings.TrimSpace(string(body)), err
}
func (r *remoteAdapter) Poll(ctx context.Context, id string) (download.Status, error) {
	body, err := r.call(ctx, http.MethodGet, "/poll", id)
	if err != nil {
		return download.Status{}, err
	}
	size, err := strconv.ParseInt(string(body), 10, 64)
	if err != nil || size < 0 {
		return download.Status{}, download.ErrOutcomeUnknown
	}
	return download.Status{State: download.DelegateTransferred, Done: size, Total: size}, nil
}
func (r *remoteAdapter) Finalize(ctx context.Context, id, dest string) error {
	body, err := r.call(ctx, http.MethodGet, "/result", id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	return os.WriteFile(dest, body, 0600)
}
