package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	rec "idl/out/go/rec"
)

func main() {
	out, corpus := os.Args[1], os.Args[2]
	names, err := filepath.Glob(filepath.Join(corpus, "*.json"))
	if err != nil || len(names) == 0 {
		panic("no corpus at " + corpus)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		in, err := os.ReadFile(n)
		if err != nil {
			panic(err)
		}
		stem := strings.TrimSuffix(filepath.Base(n), ".json")
		v, err := rec.Decode(in)
		if err != nil {
			r := err.(*rec.Refusal)
			b.WriteString(stem + "\t" + r.Word + "\t" + strconv.Itoa(r.Offset) + "\n")
			continue
		}
		b.WriteString(stem + "\tok\n")
		write(out, "go-rt-"+stem+".json", rec.Encode(v))
	}
	write(out, "go-corpus.txt", []byte(b.String()))
}

func write(dir, name string, body []byte) {
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
		panic(err)
	}
}
