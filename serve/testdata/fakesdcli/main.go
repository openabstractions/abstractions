// Command fakesdcli stands in for sd-cli -M convert in tests: it records its
// arguments beside its output and writes the input's bytes upper-cased, or,
// with FAKESDCLI_WRONG in its folder, one byte more.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	args := os.Args[1:]
	var in, out string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-m":
			in = args[i+1]
		case "-o":
			out = args[i+1]
		}
	}
	data, err := os.ReadFile(in)
	if err != nil || out == "" {
		os.Exit(3)
	}
	converted := bytes.ToUpper(data)
	self, _ := os.Executable()
	if _, err := os.Stat(filepath.Join(filepath.Dir(self), "FAKESDCLI_WRONG")); err == nil {
		converted = append(converted, '!')
	}
	if os.WriteFile(out, converted, 0o600) != nil {
		os.Exit(4)
	}
	cwd, _ := os.Getwd()
	record := strings.Join(args, "\n") + "\ncwd=" + cwd
	for _, item := range os.Environ() {
		record += "\nenv=" + strings.SplitN(item, "=", 2)[0]
	}
	_ = os.WriteFile(filepath.Join(filepath.Dir(out), "args.txt"), []byte(record), 0o600)
}
