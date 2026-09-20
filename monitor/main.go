// Command monitor presents service-owned work and configuration.
// The loopback UI uses a per-run bearer key; it does not claim native caller identity.
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"flag"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8734", "address to listen on; loopback only")
	open := flag.Bool("open", true, "open the window in the default browser")
	native := flag.Bool("native", windowed(), "draw a window on the desktop instead of serving a page")
	endpoint := flag.String("runtime-endpoint", "", "resolver endpoint of a runtime other than the installed one, such as an isolated `openabstractions serve runtime`; requires -runtime-program")
	program := flag.String("runtime-program", "", "absolute path of the executable that runtime must run as, under this account")
	flag.Parse()
	if *endpoint != "" || *program != "" {
		if err := bindExplicitRuntime(*endpoint, *program); err != nil {
			fail(*native, err)
		}
	}
	if err := runServicePanel(*addr, *open, *native); err != nil {
		fail(*native, err)
	}
}

// mint is this run's key. Not persisted: a key on disk is one more file to be
// read by exactly the local processes it could never have excluded anyway, and
// reopening the window is one command.
func mint() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		log.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// guard requires this run's key on everything but the page itself.
//
// Sent as a header rather than a query parameter: a form or an image on another
// origin can reach a loopback port and cannot set one, because doing so makes
// the browser ask this server for permission first and this server answers
// nothing. That closes the drive-by, which is the attack a window like this
// actually gets. It does not identify the caller, and nothing here pretends it
// does.
func guard(key string, next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Panel-Key")
		if got == "" {
			got = r.URL.Query().Get("k")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
			http.Error(rw, "this window is opened by the control panel itself, with the key it printed", http.StatusForbidden)
			return
		}
		next(rw, r)
	}
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func launch(url string) {
	if runtime.GOOS != "windows" {
		return
	}
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
