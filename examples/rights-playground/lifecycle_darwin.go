//go:build darwin

package main

// Packaging lifecycle only. Credential and Rights operations remain the same
// public Go/CLI calls as Windows. Native process observations below authorize
// cleanup of this pack's own processes, never an application permission.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"golang.org/x/sys/unix"
)

type macProcess struct {
	Role       string `json:"role"`
	PID        int    `json:"pid"`
	Executable string `json:"executable"`
	Created    string `json:"created"`
}

type macRun struct {
	Name        string       `json:"isolated"`
	AgentLoaded bool         `json:"agent_loaded"`
	Processes   []macProcess `json:"processes"`
}

func packPaths() (pack, run, panel, launcher string, err error) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	pack = filepath.Dir(identity.CanonicalProgramPath(exe))
	run = filepath.Join(pack, "run")
	panel = filepath.Join(pack, "Abstraction Panel.app", "Contents", "MacOS", "panel")
	launcher = filepath.Join(pack, "Abstraction Panel.app", "Contents", "MacOS", "Abstraction Panel")
	for _, file := range []string{filepath.Join(pack, "openabstractions"), filepath.Join(pack, "Playground A"), filepath.Join(pack, "Playground B"), panel, launcher} {
		var info os.FileInfo
		info, err = os.Lstat(file)
		if err != nil {
			return
		}
		if !info.Mode().IsRegular() {
			err = errors.New("Mac pack requires regular executable files")
			return
		}
	}
	return
}

// Reuse the kernel reads used by abstraction-identity: exact p_starttime and
// kern.procargs2 executable path, with a second creation read across the path.
func observeMacProcess(pid int) (*macProcess, error) {
	kp, err := macProcessInfo(pid)
	if err == nil && kp == nil {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("native process observation unavailable; cleanup preserved")
	}
	args, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(args) < 5 {
		return nil, errors.New("native executable observation unavailable")
	}
	end := bytes.IndexByte(args[4:], 0)
	if end <= 0 {
		return nil, errors.New("native executable observation malformed")
	}
	second, err := macProcessInfo(pid)
	if err == nil && second == nil {
		return nil, nil
	}
	if err != nil || second.Proc.P_starttime != kp.Proc.P_starttime {
		return nil, errors.New("process changed during native observation")
	}
	created := time.Unix(kp.Proc.P_starttime.Sec, int64(kp.Proc.P_starttime.Usec)*1000).UTC().Format(time.RFC3339Nano)
	return &macProcess{PID: pid, Executable: identity.CanonicalProgramPath(string(args[4 : 4+end])), Created: created}, nil
}

func macProcessInfo(pid int) (*unix.KinfoProc, error) {
	items, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) != 1 || items[0].Proc.P_pid != int32(pid) {
		return nil, errors.New("native process observation malformed")
	}
	return &items[0], nil
}

func matchingMacProcess(record macProcess) (*macProcess, error) {
	current, err := observeMacProcess(record.PID)
	if err != nil || current == nil {
		return current, err
	}
	if current.Created != record.Created || !identity.SameSubjectProgram(current.Executable, record.Executable) {
		return nil, errors.New("recorded PID has different ownership; preserved")
	}
	return current, nil
}

func writeLocalJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".playground-write-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(data); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temp.Name(), path)
}

func readLocalJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 16<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing local record data")
	}
	return nil
}

func nativeConsoleReady() error {
	info, err := os.Stat("/dev/console")
	if err != nil {
		return err
	}
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(native.Uid) != os.Getuid() {
		return errors.New("run this Mac pack in its active native console account")
	}
	for _, field := range []struct{ key, expected string }{{"manageruid", strconv.Itoa(os.Getuid())}, {"managername", "Aqua"}} {
		data, err := exec.Command("/bin/launchctl", field.key).Output()
		if err != nil || strings.TrimSpace(string(data)) != field.expected {
			return errors.New("active native GUI launchd domain unavailable")
		}
	}
	return nil
}

func runtimeAgentProcess(name string) (int, error) {
	data, err := exec.Command("/bin/launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+agentLabel(name)).Output()
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "pid = "); ok {
			pid, err := strconv.Atoi(strings.TrimSpace(value))
			if err == nil && pid > 0 {
				return pid, nil
			}
		}
	}
	return 0, errors.New("own temporary agent has no running PID")
}

func runLifecycle(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || (len(args) == 2 && (args[1] == "--help" || args[1] == "-h")) {
		_, err := fmt.Fprintln(out, "lifecycle Start|Stop|Reset [--no-windows]; Stop retains dummy registrations; Reset uses public Revoke before deleting run state")
		return err
	}
	if len(args) > 2 || (len(args) == 2 && args[1] != "--no-windows") {
		return errors.New("use lifecycle --help")
	}
	if args[0] != "Start" && args[0] != "Stop" && args[0] != "Reset" {
		return errors.New("use lifecycle --help")
	}
	pack, run, panel, launcher, err := packPaths()
	if err != nil {
		return err
	}
	if err := assertPlainTreeIfPresent(run); err != nil {
		return err
	}
	recordPath := filepath.Join(run, "mac-processes.json")
	var record macRun
	if err := readLocalJSON(recordPath, &record); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if record.Name != "" && !validIsolatedName(record.Name) {
		return errors.New("invalid saved playground namespace")
	}
	paths := map[string]string{"runtime": filepath.Join(pack, "openabstractions"), "source": filepath.Join(pack, "Playground A"), "panel": launcher, "panel-child": panel, "app-a": filepath.Join(pack, "Playground A"), "app-b": filepath.Join(pack, "Playground B")}
	for _, role := range []string{"app-a", "app-b"} {
		var app macProcess
		if err := readLocalJSON(filepath.Join(run, role+".json"), &app); err == nil {
			record.Processes = append(record.Processes, app)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, item := range record.Processes {
		if item.PID < 1 || item.Created == "" || paths[item.Role] == "" || !identity.SameSubjectProgram(item.Executable, paths[item.Role]) {
			return errors.New("invalid owned process record")
		}
	}
	if args[0] == "Stop" || args[0] == "Reset" {
		if record.Name == "" {
			return errors.New("no owned run; Start the pack first")
		}
		if args[0] == "Reset" {
			c, err := loadConnection(filepath.Join(run, "connection.json"))
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if err := resetSecrets(ctx, c, out); err != nil {
				return err
			}
		}
		for _, role := range []string{"app-a", "app-b", "panel", "panel-child", "source", "runtime"} {
			for _, item := range record.Processes {
				if item.Role != role {
					continue
				}
				current, err := matchingMacProcess(item)
				if err != nil {
					return err
				}
				if current == nil {
					continue
				}
				if role == "runtime" {
					pid, err := runtimeAgentProcess(record.Name)
					if err != nil || pid != item.PID {
						return errors.New("temporary agent ownership changed; preserved")
					}
					if err := exec.Command("/bin/launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+agentLabel(record.Name)).Run(); err != nil {
						return err
					}
					record.AgentLoaded = false
					if err := writeLocalJSON(recordPath, record); err != nil {
						return err
					}
				} else if err := syscall.Kill(item.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
					return err
				}
				if err := waitMacExit(item); err != nil {
					return err
				}
				fmt.Fprintf(out, "Stopped owned %s PID %d.\n", role, item.PID)
			}
		}
		// An exited, non-KeepAlive runtime can leave its own inactive service
		// registration loaded. Removing that registration is separate from
		// signalling a process, and is allowed only after a successful query
		// proves there is no current PID under the saved unique label.
		if record.AgentLoaded {
			data, err := exec.Command("/bin/launchctl", "print", "gui/"+strconv.Itoa(os.Getuid())+"/"+agentLabel(record.Name)).Output()
			if err != nil {
				return errors.New("temporary agent state unavailable; registration preserved")
			}
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "pid = ") {
					return errors.New("unrecorded current agent PID; preserved")
				}
			}
			if err := exec.Command("/bin/launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+agentLabel(record.Name)).Run(); err != nil {
				return err
			}
			record.AgentLoaded = false
			if err := writeLocalJSON(recordPath, record); err != nil {
				return err
			}
		}
		if args[0] == "Reset" {
			if err := assertPlainTreeIfPresent(run); err != nil {
				return err
			}
			if !identity.SamePathName(run, filepath.Join(pack, "run")) {
				return errors.New("reset target changed")
			}
			if err := os.RemoveAll(run); err != nil {
				return err
			}
			fmt.Fprintln(out, "Reset complete: public dummy deletion succeeded and this pack run folder was removed.")
		} else {
			fmt.Fprintln(out, "Stopped. State and dummy values remain; Start resumes and Reset removes them publicly.")
		}
		return nil
	}
	if err := nativeConsoleReady(); err != nil {
		return err
	}
	for _, item := range record.Processes {
		current, err := matchingMacProcess(item)
		if err != nil {
			return err
		}
		if current != nil {
			return errors.New("this pack still owns running processes; Stop before Start")
		}
	}
	if err := os.MkdirAll(filepath.Join(run, "state"), 0700); err != nil {
		return err
	}
	if record.Name == "" {
		key, err := freshKey()
		if err != nil {
			return err
		}
		record.Name = key
	}
	record.Processes = nil
	if err := writeLocalJSON(recordPath, record); err != nil {
		return err
	}
	for _, role := range []string{"app-a", "app-b"} {
		if err := os.Remove(filepath.Join(run, role+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	add := func(role string, pid int) error {
		item, err := observeMacProcess(pid)
		if err != nil || item == nil {
			return errors.New("new owned process observation unavailable; state preserved")
		}
		if !identity.SameSubjectProgram(item.Executable, paths[role]) {
			return errors.New("new process executable differs; state preserved")
		}
		item.Role = role
		record.Processes = append(record.Processes, *item)
		return writeLocalJSON(recordPath, record)
	}
	start := func(role string, image string, arguments ...string) error {
		stdout, err := os.OpenFile(filepath.Join(run, role+".stdout.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer stdout.Close()
		stderr, err := os.OpenFile(filepath.Join(run, role+".stderr.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer stderr.Close()
		cmd := exec.Command(image, arguments...)
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		return add(role, cmd.Process.Pid)
	}
	agent, err := macAgent(record.Name, paths["runtime"], panel, filepath.Join(run, "state"), filepath.Join(run, "runtime.stdout.log"), filepath.Join(run, "runtime.stderr.log"))
	if err != nil {
		return err
	}
	agentFile := filepath.Join(run, "runtime-agent.plist")
	if err := os.WriteFile(agentFile, agent, 0600); err != nil {
		return err
	}
	if err := exec.Command("/bin/launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), agentFile).Run(); err != nil {
		return errors.New("temporary agent bootstrap failed; run state preserved")
	}
	record.AgentLoaded = true
	if err := writeLocalJSON(recordPath, record); err != nil {
		return err
	}
	if err := waitMac(func() error {
		pid, err := runtimeAgentProcess(record.Name)
		if err != nil {
			return err
		}
		return add("runtime", pid)
	}); err != nil {
		return err
	}
	endpoint, _ := playgroundEndpoint(record.Name, "darwin")
	if err := waitMac(func() error {
		data, err := os.ReadFile(filepath.Join(run, "runtime.stderr.log"))
		if err != nil || !bytes.Contains(data, []byte("ABSTRACTION_RUNTIME_ENDPOINT="+endpoint)) {
			return errors.New("runtime readiness pending")
		}
		return nil
	}); err != nil {
		return err
	}
	ready := filepath.Join(run, "source-ready.json")
	if err := os.Remove(ready); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := start("source", paths["source"], "source", "--ready-file", ready); err != nil {
		return err
	}
	var source struct {
		Origin string `json:"origin"`
	}
	if err := waitMac(func() error { return readLocalJSON(ready, &source) }); err != nil {
		return err
	}
	c := connection{Version: 1, Isolated: record.Name, Endpoint: endpoint, RuntimeProgram: paths["runtime"], StateDir: filepath.Join(run, "state"), AppA: paths["app-a"], AppB: paths["app-b"], Origin: source.Origin}
	connectionFile := filepath.Join(run, "connection.json")
	if err := writeLocalJSON(connectionFile, c); err != nil {
		return err
	}
	if _, err := loadConnection(connectionFile); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(run, "setup.complete")); errors.Is(err, os.ErrNotExist) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		err := setup(ctx, c, out)
		cancel()
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(run, "setup.complete"), []byte("Public dummy setup and narrow job prerequisite complete.\n"), 0600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if len(args) == 1 {
		if err := start("panel", launcher, "--runtime-endpoint", endpoint, "--runtime-program", c.RuntimeProgram, "--runtime-state-dir", c.StateDir); err != nil {
			return err
		}
		panelPID := record.Processes[len(record.Processes)-1].PID
		if err := waitMac(func() error {
			data, err := exec.Command("/usr/bin/pgrep", "-P", strconv.Itoa(panelPID)).Output()
			if err != nil {
				return err
			}
			fields := strings.Fields(string(data))
			if len(fields) != 1 {
				return errors.New("Panel child ownership pending")
			}
			pid, err := strconv.Atoi(fields[0])
			if err != nil {
				return err
			}
			return add("panel-child", pid)
		}); err != nil {
			return err
		}
		for _, name := range []string{"App-A.command", "App-B.command"} {
			if err := exec.Command("/usr/bin/open", "-a", "Terminal", filepath.Join(pack, name)).Run(); err != nil {
				return err
			}
		}
	}
	fmt.Fprintln(out, "Ready:", connectionFile)
	fmt.Fprintln(out, "A/B select names locally; the real OA permission presenter handles consent. Stop retains state; Reset publicly deletes both dummy values.")
	return nil
}

func waitMac(check func() error) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := check(); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("readiness unavailable; owned records retained for Stop/Reset")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitMacExit(item macProcess) error {
	return waitMac(func() error {
		current, err := matchingMacProcess(item)
		if err != nil {
			return err
		}
		if current != nil {
			return errors.New("owned process still exiting")
		}
		return nil
	})
}

func assertPlainTreeIfPresent(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("lifecycle refuses a linked run tree; preserved")
		}
		return nil
	})
}

func registerMenu(c connection) error {
	if c.AppA == "" {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	role := ""
	for key, path := range map[string]string{"app-a": c.AppA, "app-b": c.AppB} {
		if identity.SameSubjectProgram(exe, path) {
			role = key
		}
	}
	if role == "" {
		return errors.New("menu must run in its actual A/B executable")
	}
	path := filepath.Join(filepath.Dir(c.StateDir), role+".json")
	var old macProcess
	if err := readLocalJSON(path, &old); err == nil {
		current, err := matchingMacProcess(old)
		if err != nil {
			return err
		}
		if current != nil {
			return errors.New("this app already has an owned menu; use its window")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	item, err := observeMacProcess(os.Getpid())
	if err != nil || item == nil {
		return errors.New("menu native ownership unavailable")
	}
	item.Role = role
	return writeLocalJSON(path, item)
}
