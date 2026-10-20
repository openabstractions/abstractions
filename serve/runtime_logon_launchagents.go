package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-identity/fileid"
)

// These two private templates are received against the package sources. The
// permanent runtime agent and its MachServices remain owned by bootstrap.
const runtimeLoginTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.openabstractions.runtime-login</string>
	<key>ProgramArguments</key>
	<array>
		<string>@BIN@/openabstractions</string>
		<string>start</string>
		<string>--timeout</string>
		<string>20s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>LimitLoadToSessionType</key>
	<array>
		<string>Aqua</string>
	</array>
</dict>
</plist>
`

const panelLoginTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.openabstractions.panel</string>
	<key>ProgramArguments</key>
	<array>
		<string>@BIN@/Abstraction Panel.app/Contents/MacOS/Abstraction Panel</string>
		<string>--background</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>LimitLoadToSessionType</key>
	<array>
		<string>Aqua</string>
	</array>
</dict>
</plist>
`

type macLogonOps struct {
	selection  func(context.Context) (bootstrap.Selection, error)
	uid, euid  func() int
	executable func() (string, error)
	open       func(context.Context, string) (fileid.Handle, error)
	owned      func(os.FileInfo, int) bool
	write      func(context.Context, string, []byte, []byte, func() error) error
	remove     func(context.Context, string, []byte, func() error) error
}

func renderMacLogin(template, bin string) []byte {
	escaped := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(bin)
	return []byte(strings.ReplaceAll(template, "@BIN@", escaped))
}

// Presence governs the next graphical login. There is deliberately no manager
// operation here: current runtime work and a visible Panel keep their lifetime.
func reconcileMacLogon(parent context.Context, enabled bool, ops macLogonOps) error {
	ctx, cancel := context.WithTimeout(parent, bootstrap.DefaultActivationBudget)
	defer cancel()
	uid := ops.euid()
	if uid <= 0 || ops.uid() != uid {
		return errors.New("runtime login requires the current unelevated account")
	}
	selected, err := ops.selection(ctx)
	if err != nil {
		return err
	}
	if selected.EndpointFromEnvironment || selected.Server.Process != nil || selected.Server.Principal.Kind != "posix" || selected.Server.Principal.UID != uid {
		return errors.New("runtime login refuses an override or another account's installation")
	}
	program, err := ops.executable()
	if err != nil {
		return err
	}
	self, err := ops.open(ctx, program)
	if err != nil {
		return err
	}
	defer self.Close()
	server, err := ops.open(ctx, selected.Server.Program)
	if err != nil {
		return err
	}
	defer server.Close()
	if !self.FileID().Equal(server.FileID()) {
		return errors.New("runtime login executable differs from selected installation")
	}
	bin := filepath.Dir(selected.Server.Program)
	if !utf8.ValidString(bin) || strings.ContainsFunc(bin, unicode.IsControl) {
		return errors.New("runtime login program cannot be rendered as single-line XML text")
	}
	home := filepath.Dir(filepath.Dir(bin))
	if selected.Server.Program != filepath.Join(home, ".local", "bin", "openabstractions") {
		return errors.New("runtime login installation layout differs")
	}
	share := filepath.Join(home, ".local", "share", "abstraction")
	agents := filepath.Join(home, "Library", "LaunchAgents")
	var held []fileid.Handle
	defer func() {
		for _, h := range held {
			h.Close()
		}
	}()
	check := func(path string, directory bool) (fileid.Handle, error) {
		h, err := ops.open(ctx, path)
		if err != nil {
			return fileid.Handle{}, err
		}
		info, err := h.File().Stat()
		if err != nil || info.IsDir() != directory || (!directory && (!info.Mode().IsRegular() || h.FileID().Links() != 1)) || !ops.owned(info, uid) {
			h.Close()
			return fileid.Handle{}, fmt.Errorf("runtime login refuses unowned or unsafe path %s", path)
		}
		held = append(held, h)
		return h, nil
	}
	for _, dir := range []string{home, filepath.Join(home, ".local"), bin, filepath.Join(home, ".local", "share"), share, filepath.Join(home, "Library"), agents} {
		if _, err := check(dir, true); err != nil {
			return err
		}
	}
	if _, err := check(selected.Server.Program, false); err != nil {
		return err
	}
	read := func(path string) ([]byte, error) {
		h, err := check(path, false)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(h.File(), 16385))
		if err != nil || len(raw) > 16384 {
			return nil, errors.New("runtime login file unavailable or oversized")
		}
		return raw, nil
	}
	readTrigger := func(path string) ([]byte, error) {
		n := len(held)
		raw, err := read(path)
		for _, h := range held[n:] {
			h.Close()
		}
		held = held[:n]
		return raw, err
	}
	checkLock := func(path string) error {
		n := len(held)
		h, err := check(path, false)
		defer func() {
			for _, h := range held[n:] {
				h.Close()
			}
			held = held[:n]
		}()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := h.File().Stat()
		if err != nil {
			return err
		}
		if info.Size() != 0 {
			return errors.New("runtime login refuses foreign lock bytes")
		}
		return nil
	}
	// Validate every template, destination and ledger entry before the first edit.
	ledger, err := read(filepath.Join(share, "MANIFEST"))
	if err != nil || ledger == nil {
		return errors.New("runtime login package ledger unavailable")
	}
	type edit struct {
		path          string
		before, after []byte
		wanted        bool
	}
	var edits []edit
	for i, template := range []string{runtimeLoginTemplate, panelLoginTemplate} {
		name := "com.openabstractions.runtime-login.plist"
		wanted := enabled
		if i == 1 {
			name = "com.openabstractions.panel.plist"
			panel := filepath.Join(bin, "Abstraction Panel.app", "Contents", "MacOS", "Abstraction Panel")
			if _, err := check(panel, false); errors.Is(err, os.ErrNotExist) {
				wanted = false
			} else if err != nil {
				return err
			} else {
				for _, dir := range []string{filepath.Dir(panel), filepath.Dir(filepath.Dir(panel)), filepath.Join(bin, "Abstraction Panel.app")} {
					if _, err := check(dir, true); err != nil {
						return err
					}
				}
			}
		}
		raw, err := read(filepath.Join(share, name))
		if err != nil {
			return err
		}
		if !bytes.Equal(raw, []byte(template)) {
			return fmt.Errorf("runtime login template differs: %s", name)
		}
		path := filepath.Join(agents, name)
		if !bytes.Contains(append([]byte{'\n'}, ledger...), []byte("\n"+path+"\n")) || !bytes.Contains(append([]byte{'\n'}, ledger...), []byte("\n"+path+".lock\n")) {
			return errors.New("runtime login conditional path missing from package ledger")
		}
		expected := renderMacLogin(template, bin)
		before, err := readTrigger(path)
		if err != nil {
			return err
		}
		if before != nil && !bytes.Equal(before, expected) {
			return fmt.Errorf("runtime login refuses foreign trigger %s", name)
		}
		if err := checkLock(path + ".lock"); err != nil {
			return err
		}
		edits = append(edits, edit{path, before, expected, wanted})
	}
	validate := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ops.uid() != uid || ops.euid() != uid {
			return errors.New("runtime login account changed")
		}
		now, err := ops.selection(ctx)
		if err != nil {
			return err
		}
		if now.EndpointFromEnvironment || now.Server.Process != nil || now.Server.Principal.Kind != "posix" || now.Server.Principal.UID != uid || now.Server.Program != selected.Server.Program {
			return errors.New("runtime login selection changed")
		}
		for _, h := range held {
			current, err := ops.open(ctx, h.FileID().Path())
			if err != nil {
				return err
			}
			info, statErr := current.File().Stat()
			same := current.FileID().Equal(h.FileID())
			current.Close()
			if statErr != nil || !same || !ops.owned(info, uid) {
				return errors.New("runtime login path changed")
			}
		}
		for i, template := range []string{runtimeLoginTemplate, panelLoginTemplate} {
			name := "com.openabstractions.runtime-login.plist"
			if i == 1 {
				name = "com.openabstractions.panel.plist"
			}
			raw, err := read(filepath.Join(share, name))
			if err != nil || !bytes.Equal(raw, []byte(template)) {
				return errors.New("runtime login template changed before update")
			}
		}
		for _, e := range edits {
			if err := checkLock(e.path + ".lock"); err != nil {
				return err
			}
		}
		return nil
	}
	for _, e := range edits {
		if err := validate(); err != nil {
			return err
		}
		current, err := readTrigger(e.path)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, e.before) || (current == nil) != (e.before == nil) {
			return errors.New("runtime login trigger changed before update")
		}
		if err := checkLock(e.path + ".lock"); err != nil {
			return err
		}
		if e.wanted && e.before == nil {
			if err := ops.write(ctx, e.path, nil, e.after, validate); err != nil {
				return err
			}
		} else if !e.wanted && e.before != nil {
			if err := ops.remove(ctx, e.path, e.before, validate); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}
