// What the hand-written Go peer makes of the corpus and of stored records.
//
//	verdicts <corpus dir> <record path or dir> ...
//
// One tab-separated line per input, read by scripts/peers/corpus.sh:
//
//	peer      go
//	toolchain go1.26.0 windows/amd64
//	verdict   <fixture>  ok | refused | unknown-model
//	roundtrip <fixture>  same | differs          (accepted fixtures only)
//	wire      <record>   read | moved | refused
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	job "github.com/openabstractions/abstraction-job/go"
)

func jsons(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var out []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// A fixture whose canonical form is not its own bytes says so in a sibling file:
// `<fixture>.roundtrip` holds what a decode and re-encode must produce. That is
// how a payload's insignificant whitespace is tested — [JOB-E7] deliberately does
// not carry it, so the input and the expectation are different bytes on purpose.
func wanted(path string, raw []byte) []byte {
	expectation, err := os.ReadFile(path + ".roundtrip")
	if err != nil {
		return raw
	}
	return expectation
}

func verdict(raw []byte) (string, *job.Record) {
	r, err := job.Decode(raw)
	switch {
	case err == nil:
		return "ok", r
	case errors.Is(err, job.ErrUnknownSchema):
		return "unknown-model", nil
	default:
		return "refused", nil
	}
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: verdicts <corpus dir> <record path or dir> ...")
		os.Exit(2)
	}
	fmt.Println("peer\tgo")
	fmt.Printf("toolchain\t%s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)

	for _, p := range jsons(os.Args[1]) {
		raw, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		word, record := verdict(raw)
		fmt.Printf("verdict\t%s\t%s\n", filepath.Base(p), word)
		if record == nil {
			continue
		}
		out, err := record.Encode()
		if err != nil || string(out) != string(wanted(p, raw)) {
			fmt.Printf("roundtrip\t%s\tdiffers\n", filepath.Base(p))
			continue
		}
		fmt.Printf("roundtrip\t%s\tsame\n", filepath.Base(p))
	}

	var records []string
	for _, arg := range os.Args[2:] {
		if info, err := os.Stat(arg); err == nil && info.IsDir() {
			records = append(records, jsons(arg)...)
		} else {
			records = append(records, arg)
		}
	}
	for _, p := range records {
		raw, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		r, err := job.Decode(raw)
		if err != nil {
			fmt.Printf("wire\t%s\trefused\t%v\n", filepath.Base(p), err)
			continue
		}
		out, err := r.Encode()
		if err != nil {
			fmt.Printf("wire\t%s\trefused\t%v\n", filepath.Base(p), err)
			continue
		}
		if string(out) != string(raw) {
			fmt.Printf("wire\t%s\tmoved\n", filepath.Base(p))
			continue
		}
		fmt.Printf("wire\t%s\tread\n", filepath.Base(p))
	}
}
