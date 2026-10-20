package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
)

func main() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		log.Fatal(err)
	}
	if len(os.Args) == 2 && os.Args[1] == "--program" {
		fmt.Println(filepath.Clean(exe))
		return
	}
	flags := flag.NewFlagSet("logging-quickstart", flag.ExitOnError)
	runtimeProgram := flags.String("runtime-program", "", "verify the selected runtime as this account's named executable")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: logging-quickstart [--runtime-program PATH] [RUNTIME_ENDPOINT]\n       logging-quickstart --program")
	}
	flags.Parse(os.Args[1:])
	endpoint := os.Getenv("ABSTRACTION_RUNTIME_ENDPOINT")
	if flags.NArg() == 1 {
		endpoint = flags.Arg(0)
	} else if flags.NArg() != 0 || endpoint == "" {
		flags.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	machine := facade.New(endpoint)
	if *runtimeProgram != "" {
		if !filepath.IsAbs(*runtimeProgram) {
			log.Fatal("--runtime-program requires an absolute executable path")
		}
		account, err := user.Current()
		if err != nil {
			log.Fatal(err)
		}
		who := identity.User{Kind: "windows", SID: account.Uid, UID: -1, GID: -1}
		if runtime.GOOS != "windows" {
			uid, err := strconv.Atoi(account.Uid)
			if err != nil {
				log.Fatal(err)
			}
			who = identity.User{Kind: "posix", UID: uid, GID: -1}
		}
		machine = client.NewVerified(endpoint, listen.ServerExpectation{Principal: who, Program: identity.CanonicalProgramPath(filepath.Clean(*runtimeProgram))})
	}
	need := facade.Requirements{Scope: facade.ScopeLocal}
	sink, err := machine.ResolveLog(ctx, need)
	if err != nil {
		log.Fatal(err)
	}
	eventID := fmt.Sprintf("quickstart-%d-%d", time.Now().UTC().UnixNano(), os.Getpid())
	message := "worker started"
	if err := sink.LogContext(ctx, 0, message, map[string]string{
		"component": "quickstart",
		"event_id":  eventID,
	}); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote: %s (%s)\n", message, eventID)

	history, err := machine.ResolveLogReader(ctx, need)
	if err != nil {
		log.Fatal(err)
	}
	cursor := ""
	for {
		page, err := history.ReadContext(ctx, cursor, 256, 65536)
		if err != nil {
			log.Fatal(err)
		}
		if page.Outcome != logwire.PageOutcomePage {
			log.Fatalf("history returned %s", page.Outcome)
		}
		for _, record := range page.Records {
			if record.Attrs["event_id"] == eventID {
				fmt.Printf("read back: %s (component=%s)\n", record.Msg, record.Attrs["component"])
				return
			}
		}
		cursor = page.Next
		if !page.AtEnd {
			continue
		}
		select {
		case <-ctx.Done():
			log.Fatal("record was not retained before the deadline")
		case <-time.After(50 * time.Millisecond):
		}
	}
}
