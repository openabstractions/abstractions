package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Restart Manager, the source Windows Installer consults, names this test
// process as the holder of a file it keeps open. An ordinary program is one
// Restart Manager closes itself, and it never extends the removal's wait.
func TestRestartManagerNamesTheHolderOfAnInstalledFile(t *testing.T) {
	folder := t.TempDir()
	tools := filepath.Join(folder, "tools")
	if err := os.MkdirAll(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(tools, "held.exe")
	if err := os.WriteFile(file, []byte("held"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := installedFiles(folder)
	if !reflect.DeepEqual(files, []string{file}) {
		t.Fatalf("installed files = %q", files)
	}
	// A console program holds the file through its inherited standard input.
	held, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "PING.EXE"), "-n", "60", "127.0.0.1")
	child.Stdin = held
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	holders, err := restartManagerHolders(files)
	if err != nil {
		t.Fatal(err)
	}
	var named bool
	for _, holder := range holders {
		named = named || holder.pid == uint32(child.Process.Pid)
	}
	if !named {
		t.Fatalf("Restart Manager did not name the holding program %d among %+v", child.Process.Pid, holders)
	}
	if blockers := removalBlockers(holders, folder); len(blockers) != 0 {
		t.Fatalf("an ordinary program blocked the removal: %+v", blockers)
	}
	// Restart Manager reports its own caller as critical; the removal's own
	// process never blocks it.
	if blockers := removalBlockers([]removalHolder{{pid: uint32(os.Getpid()), appType: rmCritical}}, folder); len(blockers) != 0 {
		t.Fatalf("the waiting process blocked itself: %+v", blockers)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	//unchecked: the killed program's exit status is the kill itself
	_ = child.Wait()
	after, err := restartManagerHolders(files)
	if err != nil || len(after) != 0 {
		t.Fatalf("holders after the program ended = %+v, %v", after, err)
	}
	var out bytes.Buffer
	if err := serviceAwaitRelease(serviceRequest{command: "await-release", scope: "user", folder: folder, uiLevel: "2"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "no other program holds the installation's files" {
		t.Fatalf("output = %q", got)
	}
}

// A brief hold by a service or a critical process (antivirus, an indexer,
// provjobd.exe) ends the wait as soon as it is released; the waits double
// from 250 ms up to 4 s.
func TestRemovalWaitsWithBackoffWhileAProgramWindowsCannotCloseHoldsAFile(t *testing.T) {
	folder := `C:\Program Files\OpenAbstractions`
	calls := 0
	holders := func([]string) ([]removalHolder, error) {
		calls++
		if calls <= 6 {
			return []removalHolder{{pid: 7, name: "provjobd", appType: rmCritical, image: `C:\Windows\provjobd.exe`},
				{pid: 8, name: "Indexer", appType: rmService}}, nil
		}
		return []removalHolder{{pid: 9, name: "Notepad", appType: 1}}, nil
	}
	var waits []time.Duration
	sleep := func(_ context.Context, d time.Duration) bool { waits = append(waits, d); return true }
	held, err := awaitRemovalRelease(context.Background(), folder, []string{folder + `\tools\openabstractions.exe`}, holders, sleep)
	if err != nil || len(held) != 0 {
		t.Fatalf("held = %+v, %v", held, err)
	}
	want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second}
	if !reflect.DeepEqual(waits, want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
}

// A hold that outlasts the budget ends the wait with the holder named in one
// plain sentence: a restart finishes the removal.
func TestRemovalSaysARestartFinishesItWhenAHolderStays(t *testing.T) {
	folder := `C:\Program Files\OpenAbstractions`
	holders := func([]string) ([]removalHolder, error) {
		return []removalHolder{{pid: 7, name: "provjobd", appType: rmCritical}}, nil
	}
	var spent time.Duration
	sleep := func(_ context.Context, d time.Duration) bool {
		if spent+d > removalReleaseBudget {
			return false
		}
		spent += d
		return true
	}
	held, err := awaitRemovalRelease(context.Background(), folder, []string{folder + `\tools\x.exe`}, holders, sleep)
	if err != nil || len(held) != 1 {
		t.Fatalf("held = %+v, %v", held, err)
	}
	if got := removalRestartSentence(held); got != "provjobd is using OpenAbstractions files. Restart your computer to finish removing OpenAbstractions." {
		t.Fatalf("sentence = %q", got)
	}
	if got := removalRestartSentence([]removalHolder{{pid: 4}}); got != "Another program is using OpenAbstractions files. Restart your computer to finish removing OpenAbstractions." {
		t.Fatalf("unnamed sentence = %q", got)
	}
}

// OA's own supervisor service runs from the installation folder; the removal
// closes it, and it never makes the removal wait.
func TestOAsOwnServiceNeverExtendsTheRemovalWait(t *testing.T) {
	folder := `C:\Program Files\OpenAbstractions`
	holders := func([]string) ([]removalHolder, error) {
		return []removalHolder{{pid: 7, name: "OpenAbstractions", appType: rmService, image: folder + `\tools\openabstractions-system.exe`}}, nil
	}
	sleep := func(context.Context, time.Duration) bool { t.Fatal("waited for OA's own service"); return false }
	held, err := awaitRemovalRelease(context.Background(), folder, []string{folder + `\tools\x.exe`}, holders, sleep)
	if err != nil || len(held) != 0 {
		t.Fatalf("held = %+v, %v", held, err)
	}
	if withinFolder(`C:\Program Files\OpenAbstractionsOther\x.exe`, folder) {
		t.Fatal("a sibling folder counted as the installation")
	}
}

func TestAwaitReleaseArguments(t *testing.T) {
	request, err := serviceArguments([]string{"await-release", "--machine", `C:\Program Files\OpenAbstractions\.`, "5"})
	if err != nil || request.command != "await-release" || request.scope != "machine" || request.uiLevel != "5" ||
		request.folder != `C:\Program Files\OpenAbstractions\.` {
		t.Fatalf("request = %+v, %v", request, err)
	}
	if _, err := serviceArguments([]string{"await-release", "--user", `C:\x`}); err == nil {
		t.Fatal("await-release without UILevel was accepted")
	}
}
