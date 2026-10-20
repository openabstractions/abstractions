package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strings"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
)

// This is the existing native runtime's service inventory. Naming these
// isolated services grants no authority; the XPC receiver proves its peer.
var macServices = []string{"runtime-v1", "logging-v1", "config-v1", "job-acceptance-v1", "router-v1", "model-v1", "storage-content-v1", "asks-application-v1", "rights-authorization-v1", "credentials-v1", "inference-v1", "inference-remote-v1", "registry-v1", "applications-v1", "resource-table-v1", "lend-v1"}

func agentLabel(name string) string { return "com.openabstractions.playground." + name }

func macAgent(name, executable, panel, state, stdout, stderr string) ([]byte, error) {
	if !validIsolatedName(name) {
		return nil, errors.New("invalid playground namespace")
	}
	var b strings.Builder
	escape := func(value string) string {
		var escaped strings.Builder
		_ = xml.EscapeText(&escaped, []byte(value))
		return escaped.String()
	}
	value := func(key, text string) { fmt.Fprintf(&b, "<key>%s</key><string>%s</string>", key, escape(text)) }
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>`)
	value("Label", agentLabel(name))
	b.WriteString("<key>ProgramArguments</key><array>")
	for _, arg := range []string{executable, "serve", "runtime", "--xpc", "--isolated", name, "--state-dir", state, "--operator-program", panel} {
		fmt.Fprintf(&b, "<string>%s</string>", escape(arg))
	}
	b.WriteString("</array><key>RunAtLoad</key><true/><key>KeepAlive</key><false/><key>ProcessType</key><string>Interactive</string><key>MachServices</key><dict>")
	for _, service := range macServices {
		endpoint, err := bootstrap.IsolatedXPCEndpoint(name, service)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "<key>%s</key><true/>", escape(strings.TrimPrefix(endpoint, "xpc:")))
	}
	b.WriteString("</dict>")
	value("StandardOutPath", stdout)
	value("StandardErrorPath", stderr)
	b.WriteString("</dict></plist>\n")
	return []byte(b.String()), nil
}
