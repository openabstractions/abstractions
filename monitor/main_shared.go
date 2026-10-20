package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
)

// flagSet reports whether the command line named the flag.
func flagSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

// reportWebView2Availability is an installed-Panel diagnostic for package
// qualification. Exit 3 is an explicit unavailable result, consistent with
// the Panel's refusal to open a window without WebView2; it does not mark the
// installation broken.
func reportWebView2Availability(out io.Writer, available func() bool) int {
	if available() {
		if _, err := fmt.Fprintln(out, "WebView2: available (native rendering untested)"); err != nil {
			return 1
		}
		return 0
	}
	if _, err := fmt.Fprintln(out, "WebView2: unavailable (no Panel window)"); err != nil {
		return 1
	}
	return 3
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
	childproc.Hidden(exec.Command("rundll32", "url.dll,FileProtocolHandler", url)).Start()
}

// windowsAppsSettings is Windows Settings at Apps, the one non-web target a
// Panel link opens.
const windowsAppsSettings = "ms-settings:appsfeatures"

// externalTarget reports whether a link leaving the Panel may be launched:
// a web page, or Windows Settings at Apps.
func externalTarget(rawURL string) bool {
	return strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") || rawURL == windowsAppsSettings
}
