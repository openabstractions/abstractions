package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	rec "idl/out/go/rec"
)

func cp(r ...rune) string { return string(r) }

func awkward() *rec.Record {
	const ts = "2026-09-08T05:07:14.951609Z"
	by := "Ada L" + cp(0x016B) + "velace <ada@" + cp(0x4F8B, 0x3048) +
		".jp> & co " + cp(0x2702, 0xFE0F) + " " + cp(0x1F9FF)
	errText := "line1" + cp(0x0A) + "line2" + cp(0x09) + "tabbed" + cp(0x01) +
		"ctrl " + cp(0x22) + "quoted" + cp(0x22) + " back" + cp(0x5C) + "slash"
	return &rec.Record{
		Content: []string{"abstraction.job/base@1", "abstraction.job/intent@1",
			"abstraction.job/envelope@1", "abstraction.job/step@1"},
		Critical: []string{"abstraction.job/base@1"},
		Id:       "1787202430967-a752f9a9c2c77b123ffd",
		Kind:     "download",
		Envelope: &rec.Envelope{
			Schema:  "nas.example/transfer@2",
			Actions: []string{"cancel", "nas.example/transfer@2#re-mirror"},
		},
		State: "pending",
		Spec:     `{"artifact":{"bytes":9223372036854775807,"empty_obj":{},"empty_arr":[]},"note":"a<b&c>d","nested":{"deep":{"x":1.50,"neg":-0.0}}}`,
		Progress: rec.Progress{
			Total:     9223372036854775807,
			UpdatedAt: ts,
			Step:      &rec.Step{Ordinal: 1, Total: -9223372036854775808},
		},
		Lease:  rec.Lease{ExpiresAt: ts},
		Error:  errText,
		Intent: &rec.Intent{Want: "cancel", By: by, At: ts},
		Extensions: map[string]rec.Raw{
			"zz.example/v1":          `{"k":"v"}`,
			"aa.example/v1":          `[1,2,3]`,
			cp(0x00E9) + ".example":  `null`,
			cp(0xFFFD) + ".example":  `true`,
			cp(0x1D11E) + ".example": `{}`,
		},
		CreatedAt: ts,
		UpdatedAt: ts,
	}
}

func ranges() *rec.Record {
	return &rec.Record{
		Content:    []string{"abstraction.job/base@1", "abstraction.download/ranges@1"},
		Critical:   []string{"abstraction.job/base@1"},
		Id:         "1787202430967-a752f9a9c2c77b123ffd",
		Kind:       "download",
		State:      "running",
		Spec:       `{"artifact":{"bytes":23068672}}`,
		Checkpoint: `{"verified_prefix":4194304,"verified":[[0,4194304],[8388608,12582912],[20971520,23068672]]}`,
		Progress: rec.Progress{
			Done:      10485760,
			Total:     23068672,
			UpdatedAt: "2026-08-20T05:07:14.951609Z",
		},
		Lease:     rec.Lease{Owner: "go-worker", Epoch: 2, ExpiresAt: "2026-08-20T05:08:14.635068Z"},
		CreatedAt: "2026-08-20T05:07:10.967343Z",
		UpdatedAt: "2026-08-20T05:07:15.134811Z",
	}
}

func terminal() *rec.Record {
	return &rec.Record{
		Content: []string{"abstraction.job/base@1", "abstraction.job/step@1",
			"abstraction.job/terminal@1", "abstraction.job/recall@1"},
		Critical: []string{"abstraction.job/base@1", "abstraction.job/terminal@1",
			"abstraction.job/recall@1"},
		Id:         "1787202430967-a752f9a9c2c77b123ffd",
		Kind:       "download",
		State:      "failed",
		Spec:       `{"artifact":{"bytes":64}}`,
		Checkpoint: `{"verified_prefix":8}`,
		Progress: rec.Progress{
			Done:      8,
			Total:     64,
			UpdatedAt: "2026-09-09T17:21:08.958178Z",
			Step:      &rec.Step{Name: "fetch", Ordinal: 1, Of: 2, Done: 8, Total: 64},
		},
		Lease: rec.Lease{
			Owner:     "alpha",
			Epoch:     3,
			ExpiresAt: "2026-09-09T17:21:09.958178Z",
			Recall: &rec.Recall{Reason: "yield", By: "broker",
				At: "2026-09-09T17:21:08.958178Z", Until: "2026-09-09T17:21:09.958178Z"},
		},
		Error:     "source closed the connection",
		CreatedAt: "2026-09-09T17:21:06.998457Z",
		UpdatedAt: "2026-09-09T17:21:08.967883Z",
	}
}

func write(dir, name string, body []byte) {
	if err := os.WriteFile(filepath.Join(dir, "go-"+name), body, 0o644); err != nil {
		panic(err)
	}
}

func verdict(in []byte) string {
	v, err := rec.Decode(in)
	if err == nil {
		return "ok\t" + string(v.Spec)
	}
	r := err.(*rec.Refusal)
	return r.Word + "\t" + strconv.Itoa(r.Offset)
}

func corpus(dir, out string) {
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(names) == 0 {
		panic("no corpus at " + dir)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		in, err := os.ReadFile(n)
		if err != nil {
			panic(err)
		}
		b.WriteString(strings.TrimSuffix(filepath.Base(n), ".json") + "\t" + verdict(in) + "\n")
	}
	write(out, "corpus.txt", []byte(b.String()))
}

func main() {
	dir, corpusDir := os.Args[1], os.Args[2]
	for name, r := range map[string]*rec.Record{"awkward": awkward(), "ranges": ranges(), "terminal": terminal()} {
		encoded := rec.Encode(r)
		write(dir, name+".json", encoded)
		back, err := rec.Decode(encoded)
		if err != nil {
			panic(name + ": " + err.Error())
		}
		write(dir, "rt-"+name+".json", rec.Encode(back))
	}
	corpus(corpusDir, dir)
}
