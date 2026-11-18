//go:build windows && presenter

// Command "OpenAbstractions Presenter" is the Asks presenter: the one program
// that draws native questions and notifications in the person's session
// (ARCHITECTURE: native questions and notifications are drawn by the Asks
// presenter process; the Panel draws none). It is the monitor package built
// with -tags presenter; the runtime's sign-in entry and supervisor start it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/openabstractions/abstractions/monitor/internal/setup"

	// The presenter's icon, native manifest and version resource.
	_ "github.com/openabstractions/abstractions/monitor/internal/presenterres"
)

// presenterBuild is true in the presenter program (-tags presenter) only.
const presenterBuild = true

func main() {
	exitDuringUpgrade()
	// COM adds -Embedding when Windows starts this program to deliver a
	// notification click to its registered activator.
	flag.Bool("Embedding", false, "set by Windows when it starts the presenter for a notification click (internal)")
	flag.Bool("background", false, "present pending questions and notifications in this session (the default)")
	flag.Bool("tray", false, "compatibility alias for -background")
	present := flag.String("present", "", "show the one Book occurrence with this `id` a Panel operation handed over, then exit with its outcome (internal)")
	endpoint := flag.String("runtime-endpoint", "", "resolver endpoint of a runtime other than the installed one; requires -runtime-program")
	program := flag.String("runtime-program", "", "absolute path of the executable that runtime must run as, under this account")
	stateDir := flag.String("runtime-state-dir", "", "absolute --state-dir of the runtime at -runtime-endpoint")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "the presenter takes no arguments")
		os.Exit(2)
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
	if *present != "" {
		code := runPresentOnce(*present)
		release()
		os.Exit(code)
	}
	defer release()
	// Connector images an earlier update moved aside while they ran are
	// deleted once no helper holds them; a held copy waits for the next start.
	go setup.RemoveSetAsideAgentImages(context.Background())
	if err := runPresenter(); err != nil {
		release()
		log.Fatal(err)
	}
}
