package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// sourcePayload is small enough for the example folder. The slow route serves
// these same bytes over thirty seconds to leave time to exit and cancel.
func sourcePayload() []byte {
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i % 251)
	}
	return data
}

type sourceCounts struct {
	requests atomic.Int64
	bytes    atomic.Int64
}

func sourceHandler(data []byte, counts *sourceCounts) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]int64{"requests": counts.requests.Load(), "bytes": counts.bytes.Load()}); err != nil {
			return // The response connection closed; there is no second response to send.
		}
	})
	for _, route := range []string{"/fixture.bin", "/slow.bin"} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "GET or HEAD required", http.StatusMethodNotAllowed)
				return
			}
			counts.requests.Add(1)
			if r.URL.Path == "/fixture.bin" || r.Method == http.MethodHead || r.Header.Get("Range") != "" {
				http.ServeContent(&countedResponse{ResponseWriter: w, counts: counts}, r, "fixture.bin", time.Unix(0, 0), bytes.NewReader(data))
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.Header().Set("Content-Type", "application/octet-stream")
			for offset := 0; offset < len(data); offset += 32768 {
				if offset > 0 {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(time.Second):
					}
				}
				end := min(offset+32768, len(data))
				n, err := w.Write(data[offset:end])
				counts.bytes.Add(int64(n))
				if err != nil {
					return
				}
				if flush, ok := w.(http.Flusher); ok {
					flush.Flush()
				}
			}
		})
	}
	return mux
}

type countedResponse struct {
	http.ResponseWriter
	counts *sourceCounts
}

func (w *countedResponse) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.counts.bytes.Add(int64(n))
	return n, err
}

func serveSource(out io.Writer) error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close() //unchecked: Serve owns the listener and closes it before returning
	data := sourcePayload()
	url := "http://" + listener.Addr().String()
	if _, err := fmt.Fprintf(out, "example source: %s %d sha256:%x\n", url, len(data), sha256.Sum256(data)); err != nil {
		return err
	}
	server := &http.Server{Handler: sourceHandler(data, &sourceCounts{}), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	return server.Serve(listener)
}
