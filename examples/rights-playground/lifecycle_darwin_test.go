//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMacOwnedProcessCreationAndExecutable(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer cmd.Process.Kill() // exactly the child started by this test
	item, err := observeMacProcess(cmd.Process.Pid)
	if err != nil || item == nil || item.Created == "" {
		t.Fatal("native child ownership unavailable", err)
	}
	if got, err := matchingMacProcess(*item); err != nil || got == nil {
		t.Fatal("own child did not match", err)
	}
	wrong := *item
	wrong.Created = "wrong creation stamp"
	if _, err := matchingMacProcess(wrong); err == nil {
		t.Fatal("reused PID stamp must refuse cleanup")
	}
	wrong = *item
	wrong.Executable = "/unrelated/program"
	if _, err := matchingMacProcess(wrong); err == nil {
		t.Fatal("different executable must refuse cleanup")
	}
}

func TestMacLifecycleRefusesLinkedRunTree(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if err := assertPlainTreeIfPresent(root); err == nil {
		t.Fatal("linked run child must preserve state")
	}
}

func TestMacOwnedExitedChildIsAbsent(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill() // exactly the child started by this test
	item, err := observeMacProcess(cmd.Process.Pid)
	if err != nil || item == nil {
		t.Fatal("native child ownership unavailable", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if got, err := observeMacProcess(item.PID); err != nil || got != nil {
		t.Fatalf("exited child observation = %+v, %v; want absent", got, err)
	}
	if got, err := matchingMacProcess(*item); err != nil || got != nil {
		t.Fatalf("exited owned child = %+v, %v; want absent", got, err)
	}
}

func TestMacOwnedExecutableAliasUsesFileIdentity(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "owned-sleep")
	if err := os.WriteFile(program, data, 0o700); err != nil {
		t.Fatal(err)
	}
	private := program
	if strings.HasPrefix(private, "/var/") {
		private = "/private" + private
	}
	if !strings.HasPrefix(private, "/private/var/") {
		t.Skip("test temp is outside native /var alias")
	}
	alias := strings.TrimPrefix(private, "/private")
	ready := filepath.Join(filepath.Dir(program), "child-ready")
	cmd := exec.Command(alias, "-test.run=^TestMacLifecycleFixtureChild$")
	cmd.Env = append(os.Environ(), "OA_PLAYGROUND_LIFECYCLE_CHILD="+ready)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer cmd.Process.Kill() // exactly this fixture child
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native fixture did not finish startup")
		}
		time.Sleep(10 * time.Millisecond)
	}
	item, err := observeMacProcess(cmd.Process.Pid)
	if err != nil || item == nil {
		t.Fatal("native alias process unavailable", err)
	}
	item.Executable = private
	if got, err := matchingMacProcess(*item); err != nil || got == nil {
		t.Fatalf("same file alias refused: %+v %v", got, err)
	}
	other := filepath.Join(filepath.Dir(program), "different-sleep")
	if err := os.WriteFile(other, data, 0o700); err != nil {
		t.Fatal(err)
	}
	item.Executable = other
	if _, err := matchingMacProcess(*item); err == nil {
		t.Fatal("different physical file accepted")
	}
}

func TestMacLifecycleFixtureChild(t *testing.T) {
	ready := os.Getenv("OA_PLAYGROUND_LIFECYCLE_CHILD")
	if ready == "" {
		t.Skip("owned subprocess fixture only")
	}
	if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Minute)
}

func TestMacMenuRegistersItsExecutableAlias(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	alias := program
	switch {
	case strings.HasPrefix(program, "/private/var/"):
		alias = strings.TrimPrefix(program, "/private")
	case strings.HasPrefix(program, "/var/"):
		alias = "/private" + program
	default:
		t.Skip("test executable is outside native /var alias")
	}
	root := t.TempDir()
	c := connection{AppA: alias, AppB: "/unrelated/program", StateDir: filepath.Join(root, "state")}
	if err := registerMenu(c); err != nil {
		t.Fatalf("native executable alias role: %v", err)
	}
	var item macProcess
	if err := readLocalJSON(filepath.Join(root, "app-a.json"), &item); err != nil {
		t.Fatal(err)
	}
	if item.Role != "app-a" || item.PID != os.Getpid() || item.Created == "" {
		t.Fatalf("menu ownership record: %+v", item)
	}
}
