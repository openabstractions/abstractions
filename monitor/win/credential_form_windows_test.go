//go:build windows

package win

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestNativeCredentialBudgetLabelPreservesConfiguredMicros(t *testing.T) {
	if credentialBudgetTitle("tokens") != "Daily token limit (optional)" || credentialBudgetTitle("money") != "Daily spending limit (optional)" {
		t.Fatal("limit title lost its unit or optional choice")
	}
	if credentialBudgetLabel(0, "tokens") != "No daily token limit" {
		t.Fatal("unlimited tokens described as monetary spending")
	}
	for amount, want := range map[int64]string{0: "No daily spending limit", 1: "0.000001 a day", 1000: "0.001 a day", 100000: "0.10 a day", 1000000: "1.00 a day", 123456: "0.123456 a day", 123456789012345: "123456789.012345 a day"} {
		if got := credentialBudgetLabel(amount, "money"); got != want {
			t.Fatal(amount, got, want)
		}
	}
	if got := credentialBudgetLabel(123456, "tokens"); got != "123456 tokens a day" {
		t.Fatal(got)
	}
}

func TestNativeCredentialFormMasksInputAndKeepsPendingAllowDisabled(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	instance, _, _ := getModuleHandle.Call(0)
	hwnd, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(utf16("#32770"))), uintptr(unsafe.Pointer(utf16("Hidden native credential fixture"))), 0x80000000|0x00c00000|0x00080000, 0, 0, 560, 380, 0, 0, instance, 0)
	if hwnd == 0 {
		t.Fatal(err)
	}
	defer destroyWindowProc.Call(hwnd)
	calls := 0
	var retained []byte
	form := &CredentialForm{Label: "Synthetic provider", BudgetUnit: "tokens", NeedsKey: true, PendingReason: "Budget owner pending", Allow: func(_ context.Context, key []byte, amount int64) error {
		calls++
		retained = key
		if string(key) != "synthetic-candidate" || amount != 0 {
			t.Error("wrong transient input")
		}
		return errors.New("synthetic-candidate must be redacted")
	}}
	d := &permissionDialog{hwnd: hwnd, dpi: 96, start: time.Now().Add(-time.Second), prompt: QuestionPrompt{CredentialForm: form, Choices: []QuestionChoice{{Label: "Allow", Value: "allow"}, {Label: "Not now", Value: "refuse"}}}}
	d.replaceFonts()
	defer d.freeResources()
	for i := range d.prompt.Choices {
		h, e := d.control("BUTTON", "inert", wsTabStop, firstQuestionButton+i)
		if e != nil {
			t.Fatal(e)
		}
		d.buttons = append(d.buttons, h)
	}
	if err := d.createCredentialForm(); err != nil {
		t.Fatal(err)
	}
	masked, _, _ := sendMessage.Call(d.credential.key, 0x00d2, 0, 0)
	if masked == 0 {
		t.Fatal("native edit lacks password masking")
	}
	setWindowText.Call(d.credential.key, uintptr(unsafe.Pointer(utf16("synthetic-candidate"))))
	d.updateCredentialAllow()
	enabled, _, _ := permissionIsEnabled.Call(d.buttons[0])
	if enabled != 0 || calls != 0 {
		t.Fatal("pending budget allowed a submission")
	}
	form.PendingReason = ""
	d.updateCredentialAllow()
	enabled, _, _ = permissionIsEnabled.Call(d.buttons[0])
	if enabled == 0 {
		t.Fatal("valid native input did not enable Allow")
	}
	if err := d.startCredential(firstQuestionButton); err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.credential.done:
	case <-time.After(2 * time.Second):
		t.Fatal("native validation worker did not join")
	}
	if done, err := d.finishCredential(); !done || err == nil || strings.Contains(err.Error(), "synthetic-candidate") {
		t.Fatal("native validation failure leaked input")
	}
	count, _, _ := credentialGetTextLength.Call(d.credential.key)
	if count != 0 || calls != 1 || !bytes.Equal(retained, make([]byte, len(retained))) {
		t.Fatal("native candidate retained after failure")
	}
	form.Allow = func(ctx context.Context, key []byte, _ int64) error {
		calls++
		retained = key
		<-ctx.Done()
		return ctx.Err()
	}
	setWindowText.Call(d.credential.key, uintptr(unsafe.Pointer(utf16("synthetic-candidate"))))
	if err := d.startCredential(firstQuestionButton); err != nil {
		t.Fatal(err)
	}
	d.cancelCredential()
	d.joinCredential()
	if d.credential.running || d.credential.pending != nil || calls != 2 || !bytes.Equal(retained, make([]byte, len(retained))) {
		t.Fatal("cancel did not join the native candidate worker")
	}
}
