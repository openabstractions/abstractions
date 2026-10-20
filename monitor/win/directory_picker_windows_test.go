//go:build windows

package win

import (
	"context"
	"errors"
	"testing"
	"time"
	"unsafe"
)

func TestDirectoryPickerSDKSlots(t *testing.T) {
	word := unsafe.Sizeof(comProc(0))
	if unsafe.Offsetof(directoryDialogVtbl{}.Show) != 3*word ||
		unsafe.Offsetof(directoryDialogVtbl{}.GetOptions) != 10*word ||
		unsafe.Offsetof(directoryDialogVtbl{}.GetResult) != 20*word ||
		unsafe.Offsetof(directoryDialogVtbl{}.Close) != 23*word ||
		unsafe.Offsetof(directoryItemVtbl{}.GetDisplayName) != 5*word {
		t.Fatal("native directory picker does not match SDK interface slots")
	}
}

func directoryResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("picker did not end")
		return nil
	}
}

func TestDirectoryPickerCancelledBeforeDispatchDoesNotOpen(t *testing.T) {
	window, closeWindow := context.WithCancel(context.Background())
	defer closeWindow()
	queued := make(chan func(), 1)
	opened := false
	pick := windowDirectoryPicker(window, func(f func()) { queued <- f }, func(context.Context) (string, error) {
		opened = true
		return "late", nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := pick(ctx); result <- err }()
	f := <-queued
	cancel()
	if !errors.Is(directoryResult(t, result), context.Canceled) {
		t.Fatal("cancel was not retained")
	}
	f()
	if opened {
		t.Fatal("cancelled queued picker opened")
	}
	closeWindow()
	if _, err := pick(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDirectoryPickerWindowCloseSuppressesLateSelectionAndSecondPicker(t *testing.T) {
	window, closeWindow := context.WithCancel(context.Background())
	defer closeWindow()
	started, release, joined := make(chan struct{}), make(chan struct{}), make(chan struct{})
	pick := windowDirectoryPicker(window, func(f func()) { go func() { f(); close(joined) }() }, func(context.Context) (string, error) {
		close(started)
		<-release // deliberately late fake native reply
		return "must-not-publish", nil
	})
	result := make(chan error, 1)
	go func() {
		path, err := pick(context.Background())
		if path != "" {
			result <- errors.New("late path published")
			return
		}
		result <- err
	}()
	<-started
	if _, err := pick(context.Background()); err == nil {
		t.Fatal("second picker opened")
	}
	closeWindow()
	if !errors.Is(directoryResult(t, result), context.Canceled) {
		t.Fatal("window lifetime was lost")
	}
	close(release)
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("fake native callback not joined")
	}
}

func TestDirectoryPickerReturnsOnlyCurrentSelection(t *testing.T) {
	pick := windowDirectoryPicker(context.Background(), func(f func()) { f() }, func(context.Context) (string, error) { return "selected", nil })
	if path, err := pick(context.Background()); err != nil || path != "selected" {
		t.Fatal(path, err)
	}
}
