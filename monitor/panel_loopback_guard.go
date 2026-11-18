package main

import (
	"context"
	"io/fs"

	"encoding/json"
	"errors"
	"github.com/openabstractions/abstraction-identity/fileid"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// The developer page (-native=false) serves this Panel on loopback HTTP and
// prints its keyed URL to whoever started it, so any program of the account
// can drive it. The runtime admits the installed Panel's image as the
// person's own window (rights RIGHTS-O17). The installed Panel's developer
// page therefore only reads, whichever runtime it is pointed at: every frame
// it sends must name a read method (loopbackReads). An isolated journey runs a
// copy of the Panel, which only the isolated runtime admits, through its
// --operator-program.

// loopbackReadPrefixes and loopbackReads are the methods the guarded page
// sends: reads, listings, observations, decisions, explanations and
// resolution. Every other method, and any frame that names none, is refused.
var (
	loopbackReadPrefixes = []string{"List", "Read", "Observe", "Decide", "Check", "Resolve"}
	loopbackReads        = []string{"Describe", "ExplainFor", "Find", "Gateway", "GetHistoryWindow", "Holders", "Hosts", "InboxesList",
		"Keys", "Lends", "Metadata", "Modalities", "Models", "ProgramBudgets", "Registrations", "Requests", "Resources", "Snapshot",
		"Sources", "Unheld", "Usage", "Declarations", "DeclarationsPinned", "Commands"}
)

var errLoopbackPersonOnly = errors.New("the installed Panel's developer page only reads; make changes and answer questions in the Panel window, or run a copy of the Panel against an isolated runtime")

// loopbackFrameGuard refuses every frame that names no read method.
func loopbackFrameGuard(frame []byte) error {
	var envelope struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(frame, &envelope) != nil || envelope.Method == "" {
		return errLoopbackPersonOnly
	}
	if loopbackRead(envelope.Method) {
		return nil
	}
	return errLoopbackPersonOnly
}

func loopbackRead(method string) bool {
	for _, prefix := range loopbackReadPrefixes {
		if strings.HasPrefix(method, prefix) {
			return true
		}
	}
	for _, read := range loopbackReads {
		if method == read {
			return true
		}
	}
	return false
}

// panelInstallFolders are the folders an installed Panel sits in: the
// per-machine and per-user install folders, and elsewhere the per-user
// ~/.local/bin layout. Whoever starts the Panel sets its environment, so
// every folder comes from what the operating system records: the
// ProgramFiles known folder, the account's profile and home folder, the
// LocalAppData known folder, where a person may have moved their local data,
// and on Windows every installation's recorded InstallLocation, which
// APPLICATIONFOLDER may put anywhere (product.InstalledToolFolders). A folder
// too many only guards more. An error means an install folder is unknown,
// which guards. Tests replace it.
var panelInstallFolders = func() ([]string, error) {
	if runtime.GOOS == "windows" {
		return product.InstalledToolFolders()
	}
	home, err := layout.AccountHome()
	if err != nil {
		return nil, err
	}
	return []string{filepath.Join(home, ".local", "bin")}, nil
}

// installedPanelFiles are the installed Panel's program files in folder.
func installedPanelFiles(folder string) []string {
	name := "Abstraction Panel"
	switch runtime.GOOS {
	case "windows":
		return []string{filepath.Join(folder, name+".exe")}
	case "darwin":
		return []string{filepath.Join(folder, name), filepath.Join(folder, name+".app", "Contents", "MacOS", name)}
	}
	return []string{filepath.Join(folder, name)}
}

// installedPanel reports whether program is the installed Panel by file
// identity, as the runtime admits the Panel (isPanelPeer): a hard link of it
// in another folder, or a start through a junction, is the installed Panel.
// It also reports true when program, an installed Panel file that exists, or
// the install folders cannot be read: an unknown answer guards.
func installedPanel(program string) bool {
	ctx := context.Background()
	self, err := fileid.Resolve(ctx, fileid.Self, program, fileid.Options{Links: fileid.FollowLocalLinks})
	if err != nil {
		return true
	}
	folders, err := panelInstallFolders()
	if err != nil {
		return true
	}
	for _, folder := range folders {
		if folder == "" {
			continue
		}
		for _, file := range installedPanelFiles(folder) {
			installed, err := fileid.Resolve(ctx, fileid.Self, file, fileid.Options{Links: fileid.FollowLocalLinks})
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil || installed.Equal(self) {
				return true
			}
		}
	}
	return false
}

// installedPanelSelf is installedPanel for this process's own program. Tests
// replace it.
var installedPanelSelf = installedPanel

// guardLoopbackPage installs loopbackFrameGuard for the installed Panel's
// developer page, whatever runtime it is pointed at. An unknown own program
// is guarded.
func guardLoopbackPage(native bool) {
	if native {
		return
	}
	self, err := os.Executable()
	if err != nil || installedPanelSelf(self) {
		listen.GuardOutgoingFrames(loopbackFrameGuard)
	}
}
