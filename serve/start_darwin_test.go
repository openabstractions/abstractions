package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinActivationUsesRegisteredRuntime(t *testing.T) {
	for _, mode := range []string{"present", "absent", "wrong-user", "background", "query-failed", "malformed", "legacy", "missing"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			exe := filepath.Join(home, ".local/bin/openabstractions")
			program := "runtime"
			if mode == "legacy" {
				program = "once"
			}
			raw := []byte("<plist><dict><key>Label</key><string>" + runtimeAgent + "</string><key>ProgramArguments</key><array><string>" + exe + "</string><string>serve</string><string>" + program + "</string></array></dict></plist>")
			var calls []string
			read := func(string) ([]byte, error) {
				if mode == "missing" {
					return nil, os.ErrNotExist
				}
				return raw, nil
			}
			run := func(ctx context.Context, args ...string) (string, error) {
				calls = append(calls, strings.Join(args, " "))
				switch args[0] {
				case "manageruid":
					if mode == "wrong-user" {
						return "0", nil
					}
					return "1000", nil
				case "managername":
					if mode == "background" {
						return "Background", nil
					}
					return "Aqua", nil
				case "list":
					if mode == "query-failed" {
						return "", errors.New("query failed")
					}
					if mode == "malformed" {
						return "bad", nil
					}
					s := "PID Status Label\n- 0 unrelated label with spaces\n"
					if mode != "absent" {
						s += "123 0 " + runtimeAgent + "\n"
					}
					return s, nil
				}
				return "", nil
			}
			err := activateDarwin(context.Background(), home, 1000, read, run)
			if mode == "present" || mode == "absent" {
				if err != nil {
					t.Fatal(err)
				}
				last := calls[len(calls)-1]
				if mode == "present" && last != "kickstart gui/1000/"+runtimeAgent {
					t.Fatal(last)
				}
				if mode == "absent" && !strings.HasPrefix(last, "bootstrap gui/1000 ") {
					t.Fatal(last)
				}
			} else {
				if err == nil {
					t.Fatal("invalid activation accepted")
				}
				for _, call := range calls {
					if strings.HasPrefix(call, "bootstrap") || strings.HasPrefix(call, "kickstart") {
						t.Fatal(call)
					}
				}
			}
		})
	}
}

func TestRuntimeAgentRejectsAmbiguousDictionary(t *testing.T) {
	const exe = "/home/user/.local/bin/openabstractions"
	label := "<key>Label</key><string>" + runtimeAgent + "</string>"
	args := "<key>ProgramArguments</key><array><string>" + exe + "</string><string>serve</string><string>runtime</string></array>"
	good := label + args
	cases := map[string]string{
		"program override":    good + "<key>Program</key><string>/other</string>",
		"duplicate label":     label + good,
		"duplicate arguments": good + args,
		"duplicate program":   good + "<key>Program</key><string>" + exe + "</string><key>Program</key><string>" + exe + "</string>",
		"nested identity":     "<key>EnvironmentVariables</key><dict>" + good + "</dict>",
		"nested argument":     "<key>Label</key><string>" + runtimeAgent + "</string><key>ProgramArguments</key><array><string>" + exe + "</string><string>serve</string><string>runtime</string><dict><key>ignored</key><string>hidden</string></dict></array>",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if validateRuntimeAgent([]byte("<plist><dict>"+body+"</dict></plist>"), exe) == nil {
				t.Fatal("ambiguous launch configuration accepted")
			}
		})
	}
	for _, extra := range []string{"<key>Program</key><string>" + exe + "</string>", "<key>KeepAlive</key><true/><key>EnvironmentVariables</key><dict><key>Label</key><string>harmless nested value</string></dict>"} {
		if err := validateRuntimeAgent([]byte("<plist><dict>"+good+extra+"</dict></plist>"), exe); err != nil {
			t.Fatal(err)
		}
	}
}
