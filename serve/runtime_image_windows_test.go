package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// copyTestImage copies this test binary to path.
func copyTestImage(t *testing.T, path string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// A running runtime whose image Windows Installer moves aside, and whose path
// then holds a new file, is the server every client refuses as "server
// program mismatch" (owner's PC, 2026-11-01). Its own image watch reports it,
// and it reports nothing while its image is still the file at its path.
func TestRuntimeImageWatchSeesItsImageMovedAside(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "openabstractions.exe")
	copyTestImage(t, image)
	cmd := exec.Command(image, "serve", "runtime", "--supervised", "image-watch-child", dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		if line != "READY 1\n" {
			done <- errors.New("child did not report READY: " + line)
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			cmd.Process.Kill()
			t.Fatal(err)
		}
	case <-time.After(hostTestStartupTimeout):
		cmd.Process.Kill()
		t.Fatal("child never became ready")
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		t.Fatalf("the watch ended an untouched runtime: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	// What Windows Installer does to an in-use image it removes or replaces.
	if err := os.Rename(image, filepath.Join(dir, "64ae4f.rbf")); err != nil {
		cmd.Process.Kill()
		t.Fatal(err)
	}
	copyTestImage(t, image)
	select {
	case err := <-exited:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 9 {
			t.Fatalf("child ended with %v, want the watch's exit 9", err)
		}
	case <-time.After(20 * time.Second):
		cmd.Process.Kill()
		t.Fatal("the watch never saw its image moved aside")
	}
}
