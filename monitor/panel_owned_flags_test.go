package main

import (
	"flag"
	"io"
	"testing"
)

func TestPanelOwnedLiteralRecipeFlags(t *testing.T) {
	for _, tc := range []struct {
		name, platform string
		args           []string
		want           bool
	}{
		{"recipe", "windows", []string{"-owned-host", "-native", "-quiet", "-page", "credentials"}, true},
		{"explicit runtime", "windows", []string{"-owned-host", "-native", "-quiet", "-page", "credentials", "-runtime-endpoint", "fixture", "-runtime-program", "fixture"}, true},
		{"HTTP", "windows", []string{"-owned-host", "-native=false", "-quiet", "-page", "credentials"}, false},
		{"stdout", "windows", []string{"-owned-host", "-native", "-page", "credentials"}, false},
		{"other page", "windows", []string{"-owned-host", "-native", "-quiet", "-page", "activity"}, false},
		{"background", "windows", []string{"-owned-host", "-native", "-quiet", "-page", "credentials", "-background"}, false},
		{"foreign argument", "windows", []string{"-owned-host", "-native", "-quiet", "-page", "credentials", "payload"}, false},
		{"Mac", "darwin", []string{"-owned-host", "-native", "-quiet", "-page", "credentials"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := flag.NewFlagSet("fixture", flag.ContinueOnError)
			flags.SetOutput(io.Discard)
			flags.Bool("owned-host", false, "")
			native := flags.Bool("native", false, "")
			quiet := flags.Bool("quiet", false, "")
			page := flags.String("page", "overview", "")
			flags.Bool("background", false, "")
			flags.String("runtime-endpoint", "", "")
			flags.String("runtime-program", "", "")
			if err := flags.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			err := validateOwnedPanelArguments(tc.platform, *native, *quiet, *page, flags)
			if (err == nil) != tc.want {
				t.Fatalf("admission %v want%v", err, tc.want)
			}
		})
	}
}
