package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
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
	endpoint := os.Getenv("ABSTRACTION_RUNTIME_ENDPOINT")
	if len(os.Args) == 2 {
		endpoint = os.Args[1]
	} else if len(os.Args) != 1 || endpoint == "" {
		log.Fatal("usage: logging-quickstart [RUNTIME_ENDPOINT]")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	machine := facade.New(endpoint)
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
