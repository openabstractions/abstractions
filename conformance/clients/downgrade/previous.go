//go:build ignore

// The previous release's managed HTTP runtime storage handling, built against
// published job and download modules. run.py copies this file into a generated
// module outside go.work and builds it there.
//
// usage: previous STORE_ROOT preflight|open
//
// preflight is CheckManaged with the release's HTTP execution profile, as its
// runtime storage check calls it. open is OpenManaged. One JSON line is printed.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: previous STORE_ROOT preflight|open")
		os.Exit(2)
	}
	root, check := os.Args[1], os.Args[2]
	var err error
	switch check {
	case "preflight":
		err = acceptanceprovider.CheckManaged(root, downloadserve.HTTPExecution{})
	case "open":
		_, err = acceptanceprovider.OpenManaged(root, downloadserve.HTTPExecution{})
	default:
		fmt.Fprintln(os.Stderr, "unknown check", check)
		os.Exit(2)
	}
	result := map[string]any{"check": check, "ok": err == nil}
	if err != nil {
		result["error"] = err.Error()
	}
	json.NewEncoder(os.Stdout).Encode(result)
}
