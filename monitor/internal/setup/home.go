package setup

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
)

// Home is where Setup keeps what it saves for the runtime the Panel serves.
// Config holds the plan journal, the model journals and staged app
// downloads. Data is the base folder installed apps and ComfyUI's local data
// live under. Pictures is ComfyUI's output folder; empty selects the
// person's Pictures folder. Isolated marks a runtime started with its own
// state folder: the Lemonade provider keeps its state in this account's own
// folders, so Setup refuses every provider verb there.
type Home struct {
	Config   string
	Data     string
	Pictures string
	Isolated bool
}

// homeChoice is the Home this process selected, or the reason it has none.
type homeChoice struct {
	home Home
	err  error
}

// chosenHome is nil for the installed runtime: the person's own folders. A
// Panel serves one runtime per process (monitor/panel_runtime.go), so its
// Setup state is chosen once at start.
var chosenHome atomic.Pointer[homeChoice]

// UseRuntimeState keeps Setup's state under the served runtime's own state
// folder, the --state-dir that runtime was started with (live walkthrough
// 2026-10-02, D6): <state>/setup for the journals, <state>/setup/local for
// installed apps and their data and <state>/setup/pictures for ComfyUI's
// output. Nothing is written to the person's own Setup state.
func UseRuntimeState(stateDir string) error {
	if !filepath.IsAbs(stateDir) {
		return errors.New("the runtime's state folder must be an absolute path")
	}
	root := filepath.Join(filepath.Clean(stateDir), "setup")
	chosenHome.Store(&homeChoice{home: Home{Config: root, Data: filepath.Join(root, "local"),
		Pictures: filepath.Join(root, "pictures"), Isolated: true}})
	return nil
}

// UseNoState refuses every Setup read and write with reason: the Panel
// serves a runtime whose state folder it was not told.
func UseNoState(reason error) {
	if reason == nil {
		reason = errors.New("Setup has no state folder for this runtime")
	}
	chosenHome.Store(&homeChoice{err: reason})
}

// UseUserState returns Setup to the person's own folders. Tests use it to
// undo UseRuntimeState or UseNoState.
func UseUserState() { chosenHome.Store(nil) }

// CurrentHome is the Home this process selected: the runtime's state folder,
// a refusal, or the person's own folders (%APPDATA%\OpenAbstractions\setup
// and %LOCALAPPDATA% on Windows).
func CurrentHome() (Home, error) {
	if choice := chosenHome.Load(); choice != nil {
		return choice.home, choice.err
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return Home{}, err
	}
	data, err := os.UserCacheDir()
	if err != nil {
		return Home{}, err
	}
	return Home{Config: filepath.Join(config, "OpenAbstractions", "setup"), Data: data}, nil
}

// errIsolatedProvider refuses a Lemonade provider verb while the Panel serves
// a runtime with its own state folder.
var errIsolatedProvider = errors.New("the picture-model provider keeps its state in this account's own folders; Setup leaves it alone while the Panel serves a runtime with its own state folder")
