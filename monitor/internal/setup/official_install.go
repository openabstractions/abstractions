package setup

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// OfficialInstallEvidence is what Windows recorded about an official desktop
// program's own installer. Every field falls back to its zero value when
// Windows does not let this account read it.
type OfficialInstallEvidence struct {
	// Registered: the vendor installer wrote its uninstall registration,
	// which it does only when it reports success. Version is that
	// registration's DisplayVersion and Written its last write time.
	Registered bool
	Version    string
	Written    time.Time
	// SmartAppControl is "on", "evaluation", "off", or empty when unread.
	SmartAppControl string
	// Blocks are the Code Integrity refusals (event 3077) near Written.
	Blocks []CodeIntegrityBlock
	// EventsUnread says why the Code Integrity log was not read.
	EventsUnread string
}

// CodeIntegrityBlock is one Code Integrity event 3077 and its matching 3089.
type CodeIntegrityBlock struct {
	At     time.Time
	File   string
	Policy string
	// Signatures is the 3089 TotalSignatureCount; empty without a 3089.
	Signatures string
}

// codeIntegrityWindow bounds how far from the installer's registration a
// Code Integrity block counts as the installer's.
const codeIntegrityWindow = 15 * time.Minute

// OfficialInstallBlocked reports an official installer that registered
// success while Windows Smart App Control kept its program off the disk. The
// caller has already found the program missing. reason is the Catalog row's
// plain sentence; detail goes under Technical details.
func OfficialInstallBlocked(title, program string, e OfficialInstallEvidence) (reason, detail string, blocked bool) {
	if !e.Registered || (e.SmartAppControl != "on" && len(e.Blocks) == 0) {
		return "", "", false
	}
	reason = fmt.Sprintf("Windows Smart App Control blocked %s's installer. Turn off Smart App Control in Windows Security, install %s again, then select Retry.", title, title)
	parts := []string{}
	if e.SmartAppControl != "" {
		parts = append(parts, "Smart App Control: "+e.SmartAppControl+".")
	}
	version := e.Version
	if version == "" {
		version = "unknown"
	}
	parts = append(parts, fmt.Sprintf("The %s installer registered version %s, and %s is missing.", title, version, path.Base(program)))
	for _, block := range e.Blocks {
		line := fmt.Sprintf("Code Integrity 3077 at %s blocked %s", block.At.UTC().Format(time.RFC3339), block.File)
		if block.Policy != "" {
			line += " under " + block.Policy
		}
		if block.Signatures != "" {
			line += " (3089 signatures: " + block.Signatures + ")"
		}
		parts = append(parts, line+".")
	}
	if e.EventsUnread != "" {
		parts = append(parts, "Code Integrity log: "+e.EventsUnread+".")
	}
	return reason, strings.Join(parts, " "), true
}
