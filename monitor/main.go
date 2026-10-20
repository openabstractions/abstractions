// Command monitor presents service-owned work and configuration.
// The loopback UI uses a per-run bearer key; it does not claim native caller identity.
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
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8734", "with -native=false, the loopback address the developer page listens on")
	open := flag.Bool("open", true, "with -native=false, open the developer page in the default browser")
	native := flag.Bool("native", windowed(), "open the Panel window, which answers its own requests and binds no listener; false serves the developer page on loopback HTTP")
	page := flag.String("page", "overview", "initial Panel page: overview, questions, credentials, setup or activity; permission-review opens a standalone native permission dialog")
	reviewFeature := flag.String("review-feature", "", "with -page permission-review, propose notifications or picture-workflow for the person's review")
	reviewProgram := flag.String("review-program", "", "with notifications, propose an existing local executable; the Panel verifies its identity before the person chooses")
	reviewApp := flag.String("review-app", "", "with picture-workflow, propose the registered application's name")
	reviewHost := flag.String("review-host", "", "with picture-workflow, propose an image server from the runtime's current inventory")
	reviewReceipt := flag.String("review-receipt", "", "with native permission-review, write the explicit human decision to a new file in an existing private evidence folder")
	operation := flag.String("operation", "", "with -page activity, the job operation `id` to open Activity at")
	// COM adds -Embedding when Windows starts this program to deliver a
	// notification click to its registered activator.
	flag.Bool("Embedding", false, "set by Windows when it starts the background presenter for a notification click (internal)")
	quiet := flag.Bool("quiet", false, "do not print this run's keyed Panel URL")
	nativeAsks := flag.Bool("native-asks", false, "exchange native questions with the macOS launcher over its owned pipes (internal)")
	checkWebView2 := flag.Bool("check-webview2", false, "check installed WebView2 runtime availability without opening a window or browser")
	background := flag.Bool("background", false, "present pending questions in the unlocked Windows session without opening the Panel")
	tray := flag.Bool("tray", false, "compatibility alias for -background")
	endpoint := flag.String("runtime-endpoint", "", "resolver endpoint of a runtime other than the installed one, such as an isolated `openabstractions serve runtime`; requires -runtime-program. That runtime admits this Panel as its operator when started with --operator-program naming this executable")
	program := flag.String("runtime-program", "", "absolute path of the executable that runtime must run as, under this account")
	stateDir := flag.String("runtime-state-dir", "", "absolute --state-dir of the runtime at -runtime-endpoint; Setup keeps its state under it and refuses without it")
	registerApplication := flag.String("register-application", "", "show the application manifest in this JSON `file` (the Facade's ApplicationDescriptor) and register it after the person confirms; for an application's installer")
	removeApplication := flag.String("remove-application", "", "show the application registered under this `name` and remove it after the person confirms; for an application's uninstaller")
	flag.Parse()
	request, startPage, err := parseApplicationStart(*registerApplication, *removeApplication, *page)
	if err != nil {
		fail(*native, err)
	}
	if _, err := parsePanelStartPage(startPage); err != nil {
		fail(*native, err)
	}
	startPermissionReview, err = parsePermissionReviewStart(startPage, *reviewFeature, *reviewApp, *reviewProgram, *reviewHost)
	if err != nil {
		fail(*native, err)
	}
	if err := validatePermissionReviewLaunch(startPermissionReview, permissionReviewLaunchFlags{Native: *native, NativeAsks: *nativeAsks, Background: *background, Tray: *tray, CheckWebView2: *checkWebView2, Operation: *operation, Receipt: *reviewReceipt}); err != nil {
		fail(*native, err)
	}
	if *reviewReceipt != "" {
		if err := preparePermissionReceiptPath(*reviewReceipt); err != nil {
			fail(*native, err)
		}
		startPermissionReviewReceipt = *reviewReceipt
	}
	if *operation != "" {
		if len(*operation) > 128 || strings.Trim(*operation, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			fail(*native, fmt.Errorf("-operation names one job operation id: lowercase letters, digits and '-'"))
		}
		startOperation = *operation
	}
	if request != (applicationStart{}) && !flagSet("addr") {
		// An installer may start this beside a running Panel; the window
		// takes any free loopback port.
		*addr = "127.0.0.1:0"
	}
	if *checkWebView2 {
		if code := reportWebView2Availability(os.Stdout, nativeWebView2Available); code != 0 {
			os.Exit(code)
		}
		return
	}
	if *endpoint != "" || *program != "" || *stateDir != "" {
		if err := bindExplicitRuntime(*endpoint, *program, *stateDir); err != nil {
			fail(*native, err)
		}
	}
	if *nativeAsks {
		// With -register-application or -remove-application the child asks
		// that one confirmation on the launcher's question panel and exits.
		if err := runNativeAsks(request); err != nil {
			fail(*native, err)
		}
		return
	}
	if *background || *tray {
		if err := runBackground(); err != nil {
			fail(*native, err)
		}
		return
	}
	if err := runServicePanel(*addr, *open, *native, startPage, *quiet, request); err != nil {
		fail(*native, err)
	}
}

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
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
