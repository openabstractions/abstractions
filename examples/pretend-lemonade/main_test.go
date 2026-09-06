package main

import (
	"bytes"
	"crypto/sha256"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// The example is the only honest caller of the facade, so it is the only place
// the advertised API is exercised as an adopter would exercise it. Building it
// proves nothing; a caller that compiles and then hangs forever waiting for
// bytes nobody started is exactly the defect this is here to catch.
func TestTheWholeIntegration(t *testing.T) {
	payload := make([]byte, 1<<20)
	rand.New(rand.NewSource(1)).Read(payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "weights.bin", time.Now(), bytes.NewReader(payload))
	}))
	defer srv.Close()

	t.Setenv("ABSTRACTION_STORE", t.TempDir())
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	if err := run([]string{srv.URL + "/weights.bin"}, &out); err != nil {
		t.Fatal(err)
	}

	final := strings.TrimSpace(strings.TrimPrefix(lastLine(out.String()), "delivered to "))
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("%s: %v\n%s", final, err, out.String())
	}
	if sha256.Sum256(got) != sha256.Sum256(payload) {
		t.Fatalf("%s does not match what the server served", final)
	}
	t.Log(out.String())
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
