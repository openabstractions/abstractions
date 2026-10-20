//go:build !presenter

// Command monitor presents service-owned work and configuration.
// The loopback UI uses a per-run bearer key; it does not claim native caller identity.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func main() {
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case "--maintain-desktop-integration=apply":
			os.Exit(runInstallerDesktopUpgrade(false))
		case "--maintain-desktop-integration=defer":
			os.Exit(runInstallerDesktopUpgrade(true))
		}
	}
	readConnection, connectConnection, prepareConnection := product.InstalledAgentConnectionCLI()
	connection := installedConnection()
	// The held plugin child has one fixed read-only operation. Keep it outside
	// the public flag set; it accepts no caller path, identity or runtime tuple.
	if len(os.Args) == 2 && os.Args[1] == readConnection {
		if err := runInstalledAgentConnection(true); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	addr := flag.String("addr", "127.0.0.1:8734", "with -native=false, the loopback address the developer page listens on")
	open := flag.Bool("open", true, "with -native=false, open the developer page in the default browser")
	native := flag.Bool("native", windowed(), "open the Panel window, which answers its own requests and binds no listener; false serves the developer page on loopback HTTP")
	page := flag.String("page", "overview", "initial Panel page: overview, questions, credentials, setup, activity or agent:<registered application>; permission-review opens a standalone native permission dialog")
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
	nativeOwnedChild := flag.Bool("native-owned-child", false, "serve the Swift main's private owned document channel (internal)")
	ownedHost := flag.Bool("owned-host", false, "serve native Panel requests over its owned private framed channel (internal)")
	checkWebView2 := flag.Bool("check-webview2", false, "check installed WebView2 runtime availability without opening a window or browser")
	checkCodexConnection := flag.Bool("check-codex-connection", false, "check installed Codex setup readiness without opening a window or changing registrations")
	background := flag.Bool("background", false, "present pending questions in the unlocked Windows session without opening the Panel")
	tray := flag.Bool("tray", false, "compatibility alias for -background")
	setupMode := flag.Bool("setup", false, "run the setup step: one question, then the work it approves, reported through notifications; the installer's finish page, Settings and the background presenter start it")
	setupUse := flag.String("setup-use", "", "with -setup: the use an app's setup request named (code, image or video); the background presenter passes it")
	setupApp := flag.String("setup-app", "", "with -setup: the display name of the app whose setup request started the step; the question names it")
	setupRequest := flag.String("setup-request", "", "with -setup-use and -setup-app, the runtime-issued request id passed by the presenter")
	openApp := flag.String("open-app", "", "open the registered application with this `name` through the runtime's owned launch, as its Start menu shortcut does; when it does not open, the Panel opens on its page")
	finish := flag.Bool("finish", false, "Setup's Finish: run the setup step when it has something to ask or resume, otherwise open the Panel window")
	update := flag.Bool("update", false, "with -finish, the installer is upgrading OpenAbstractions (internal)")
	endpoint := flag.String("runtime-endpoint", "", "resolver endpoint of a runtime other than the installed one, such as an isolated `openabstractions serve runtime`; requires -runtime-program. That runtime admits this Panel as its operator when started with --operator-program naming this executable")
	program := flag.String("runtime-program", "", "absolute path of the executable that runtime must run as, under this account")
	stateDir := flag.String("runtime-state-dir", "", "absolute --state-dir of the runtime at -runtime-endpoint; Setup keeps its state under it and refuses without it")
	setupTransport := flag.String("setup-transport", "", "bounded loopback qualification transport startup JSON; requires -native=false -open=false -page setup")
	registerApplication := flag.String("register-application", "", "register the application manifest in this JSON `file` (the Facade's ApplicationDescriptor) without a question; a refused one waits in Apps as Not connected; for an application's installer")
	removeApplication := flag.String("remove-application", "", "remove the application registered under this `name` when its program is the one its installer put in place; for an application's uninstaller")
	prepareIntegration := flag.String(prepareConnection, "", "prepare the installed "+connection.title+" MCP image and write its path to a new private installer receipt; grants no enrollment")
	connectIntegration := flag.Bool(connectConnection, false, "open the native "+connection.title+" connection review; grants no answering permission")
	exampleName := flag.String("review-examples", "", "private panel_review demonstration isolated runtime name")
	exampleProgram := flag.String("review-example-program", "", "private panel_review example executable")
	exampleSource := flag.String("review-example-source", "", "private panel_review loopback source")
	flag.Parse()
	var setupFlags []string
	flag.Visit(func(value *flag.Flag) { setupFlags = append(setupFlags, value.Name) })
	if err := setupFinishFlags(*finish, setupFlags, flag.NArg()); err != nil {
		fail(*native, err)
	}
	if *openApp != "" {
		valid := flag.NArg() == 0
		flag.Visit(func(value *flag.Flag) {
			if value.Name != "open-app" {
				valid = false
			}
		})
		if !valid {
			fail(*native, fmt.Errorf("-open-app must be used alone"))
		}
		next := openAppFromShortcut(*openApp)
		if next == "" {
			return
		}
		*page = next
	}
	if *connectIntegration {
		valid := flag.NArg() == 0
		flag.Visit(func(value *flag.Flag) {
			if value.Name != connectConnection {
				valid = false
			}
		})
		if !valid {
			fmt.Fprintln(os.Stderr, "-"+connectConnection+" must be used alone")
			os.Exit(2)
		}
		if err := runInstalledAgentConnection(false); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *prepareIntegration != "" {
		valid := flag.NArg() == 0
		flag.Visit(func(value *flag.Flag) {
			if value.Name != prepareConnection {
				valid = false
			}
		})
		if !valid {
			fmt.Fprintln(os.Stderr, "-"+prepareConnection+" must be used alone")
			os.Exit(2)
		}
		if err := runInstalledIntegrationPreparation(*prepareIntegration); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(integrationPreparationExitCode(err))
		}
		return
	}
	examplesMode := *exampleName != "" || *exampleProgram != "" || *exampleSource != ""
	if examplesMode {
		if *nativeAsks || *nativeOwnedChild || *ownedHost || *background || *tray || *checkWebView2 || *checkCodexConnection || *open || *registerApplication != "" || *removeApplication != "" || *setupTransport != "" || flag.NArg() != 0 {
			fail(*native, fmt.Errorf("review examples require the ordinary isolated demonstration Panel with -open=false"))
		}
		self, err := os.Executable()
		if err != nil {
			fail(*native, err)
		}
		if err := (reviewExamples{Name: *exampleName, Program: *exampleProgram, Source: *exampleSource}).validate(&explicitRuntime{Endpoint: *endpoint, Program: *program, StateDir: *stateDir}, self); err != nil {
			fail(*native, err)
		}
	}
	if *checkCodexConnection {
		valid := flag.NArg() == 0
		flag.Visit(func(value *flag.Flag) {
			if value.Name != "check-codex-connection" {
				valid = false
			}
		})
		if !valid {
			fmt.Fprintln(os.Stderr, "-check-codex-connection must be used alone")
			os.Exit(2)
		}
		panel := new(servicePanel)
		os.Exit(reportCodexConnectionAvailability(context.Background(), os.Stdout, panel.codexConnectionsForPanel().read))
	}
	if *finish {
		open, err := runSetupStepNative(nil, true, *update)
		if err != nil {
			log.Printf("setup: %v", err)
		}
		if !open {
			return
		}
	}
	if *setupMode {
		valid := flag.NArg() == 0
		flag.Visit(func(value *flag.Flag) {
			if value.Name != "setup" && value.Name != "native" && value.Name != "quiet" && value.Name != "setup-use" && value.Name != "setup-app" && value.Name != "setup-request" {
				valid = false
			}
		})
		requester, err := stepRequesterFlags(*setupUse, *setupApp, *setupRequest)
		if !valid || err != nil {
			fail(*native, fmt.Errorf("-setup runs the installed runtime's setup step and takes only -setup-use, -setup-app and -setup-request"))
		}
		// The step reports its own failure as one notice; an error box at
		// each sign-in would be a second channel.
		if _, err := runSetupStepNative(requester, false); err != nil {
			log.Printf("setup: %v", err)
		}
		return
	}
	var transportFlags []string
	flag.Visit(func(value *flag.Flag) { transportFlags = append(transportFlags, value.Name) })
	if err := validateSetupTransportMode(*setupTransport, setupTransportMode{Native: *native, NativeExplicit: flagSet("native"), Open: *open, Page: *page, Endpoint: *endpoint, Program: *program, StateDir: *stateDir, Flags: transportFlags, Args: flag.NArg()}); err != nil {
		log.Fatal(err)
	}
	if *nativeOwnedChild {
		valid := runtime.GOOS == "darwin" && *nativeAsks && flag.NArg() == 0
		flag.Visit(func(value *flag.Flag) {
			if value.Name != "native-asks" && value.Name != "native-owned-child" {
				valid = false
			}
		})
		if !valid {
			log.Fatal("invalid private native child recipe")
		}
		if err := runNativeOwnedChild(os.Stdin, os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *ownedHost {
		if err := validateOwnedPanelFlags(*native, *quiet, *page); err != nil {
			log.Fatal(err)
		}
		if *endpoint != "" || *program != "" || *stateDir != "" {
			if err := bindExplicitRuntime(*endpoint, *program, *stateDir); err != nil {
				log.Fatal(err)
			}
		}
		release, err := panelSettingsGuard(context.Background())
		if err != nil {
			log.Fatal(err)
		}
		defer release()
		if err := runOwnedPanel(); err != nil {
			log.Fatal(err)
		}
		return
	}
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
	releaseSettings, err := panelSettingsGuard(context.Background())
	if err != nil {
		fail(*native, err)
	}
	defer releaseSettings()
	if request != (applicationStart{}) {
		// An installer's request asks nothing and opens no window
		// (application_native.go): the run applies it and ends. A refused
		// registration waits in Apps as Not connected.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err := (&servicePanel{}).applyApplicationRequest(ctx, request)
		cancel()
		if err != nil {
			log.Print(err)
			releaseSettings()
			os.Exit(1)
		}
		return
	}
	if *nativeAsks {
		if err := runNativeAsks(); err != nil {
			fail(*native, err)
		}
		return
	}
	if *exampleName != "" || *exampleProgram != "" || *exampleSource != "" {
		v := reviewExamples{Name: *exampleName, Program: *exampleProgram, Source: *exampleSource}
		self, err := os.Executable()
		if err != nil {
			fail(*native, err)
		}
		if err := v.validate(panelExplicit, self); err != nil {
			fail(*native, err)
		}
		if *background || *tray || request != (applicationStart{}) {
			fail(*native, fmt.Errorf("review examples require the ordinary demonstration Panel"))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		pending, err := populateReviewExamples(ctx, v)
		cancel()
		if err != nil {
			fail(*native, err)
		}
		if pending != "" {
			fmt.Fprintln(os.Stderr, pending)
		}
	}
	// Connector images an earlier update moved aside while they ran are
	// deleted once no helper holds them; a held copy waits for the next start.
	go setup.RemoveSetAsideAgentImages(context.Background())
	if *background || *tray {
		if err := runBackground(); err != nil {
			fail(*native, err)
		}
		return
	}
	var transport *setup.PublicTransfer
	if *setupTransport != "" {
		input, err := readSetupTransport(*setupTransport)
		if err != nil {
			fail(*native, err)
		}
		if panelExplicit == nil {
			transport, err = installedSetupTransport(input)
		} else {
			transport, err = setupTransportForPanel(input, panelExplicit, panelMachine())
		}
		if err != nil {
			fail(*native, err)
		}
	}
	if err := runServicePanelWithSetupTransfer(*addr, *open, *native, startPage, *quiet, request, transport); err != nil {
		fail(*native, err)
	}
}
