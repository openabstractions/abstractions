package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	download "github.com/openabstractions/abstraction-download/go"
	job "github.com/openabstractions/abstraction-job/go"
)

func main() {
	store, err := job.NewFileStore("store")
	if err != nil {
		panic(err)
	}
	id, err := store.Submit(job.Record{Kind: "stranger", Spec: json.RawMessage("{}")})
	if err != nil {
		panic(err)
	}
	back, err := store.Load(id)
	if err != nil {
		panic(err)
	}
	fmt.Printf("state=%s kind=%s sink=%s digest=%s\n", back.State, back.Kind,
		download.Portable(`models\x.gguf`), download.NormalDigest(strings.Repeat("a", 64)))
	if back.ID != id {
		os.Exit(1)
	}
}
