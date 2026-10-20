//go:build windows && !panel

package win

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	unicodeutf16 "unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

// CredentialForm is a private native callback. Candidate bytes are never a
// QuestionPrompt text member or an option/result returned to an application.
type CredentialForm struct {
	Label, BudgetUnit, PendingReason string
	NeedsKey                         bool
	NeedsKeyNow                      func() bool
	RetryOnly                        func() bool
	DefaultBudget                    int64
	GetKey                           func()
	Allow                            func(context.Context, []byte, int64) error
	Warning                          func() string
}

func CredentialFormSupported() bool {
	return modernPermissionFor(QuestionPrompt{Permission: true}, windowsVersion())
}

const (
	credentialKeyID      = 210
	credentialBudgetID   = 211
	credentialGetKeyID   = 212
	credentialStatusID   = 213
	credentialWMComplete = 0x8000 + 0x45
)

var credentialGetTextLength = user32.NewProc("GetWindowTextLengthW")
var credentialGetText = user32.NewProc("GetWindowTextW")

type credentialControls struct {
	key, label, budget, budgetLabel, getKey, status uintptr
	amounts                                         []int64
	statusText                                      string
	running                                         bool
	pending                                         chan error
	done                                            chan struct{}
	cancel                                          context.CancelFunc
	choice                                          uintptr
}

func (d *permissionDialog) createCredentialForm() error {
	f := d.prompt.CredentialForm
	if f == nil {
		return nil
	}
	var err error
	d.credential = &credentialControls{}
	c := d.credential
	if f.NeedsKey {
		c.label, err = d.control("STATIC", "Paste your "+f.Label+" key", 0x80, 214)
		if err != nil {
			return err
		}
		// ES_PASSWORD and EM_LIMITTEXT keep the candidate in this native edit.
		c.key, err = d.control("EDIT", "", wsTabStop|0x0080|0x0020|0x00800000, credentialKeyID)
		if err != nil {
			return err
		}
		sendMessage.Call(c.key, 0x00c5, 16384, 0)
		if f.GetKey != nil {
			c.getKey, err = d.control("BUTTON", "Get a key", wsTabStop, credentialGetKeyID)
			if err != nil {
				return err
			}
		}
	}
	c.budgetLabel, err = d.control("STATIC", credentialBudgetTitle(f.BudgetUnit), 0x80, 215)
	if err != nil {
		return err
	}
	c.budget, err = d.control("COMBOBOX", "", wsTabStop|0x0003|0x00200000, credentialBudgetID)
	if err != nil {
		return err
	}
	c.amounts = []int64{0, 100000, 1000000, 10000000}
	found := false
	for _, n := range c.amounts {
		if n == f.DefaultBudget {
			found = true
		}
	}
	if !found {
		c.amounts = append(c.amounts, f.DefaultBudget)
	}
	selected := 0
	for i, n := range c.amounts {
		label := credentialBudgetLabel(n, f.BudgetUnit)
		sendMessage.Call(c.budget, 0x0143, 0, uintptr(unsafe.Pointer(utf16String(label))))
		if n == f.DefaultBudget {
			selected = i
		}
	}
	sendMessage.Call(c.budget, 0x014e, uintptr(selected), 0)
	c.status, err = d.control("STATIC", f.PendingReason, 0x80, credentialStatusID)
	c.statusText = f.PendingReason
	return err
}

func credentialBudgetTitle(unit string) string {
	if unit == "tokens" {
		return "Daily token limit (optional)"
	}
	return "Daily spending limit (optional)"
}

func credentialBudgetLabel(amount int64, unit string) string {
	if amount <= 0 {
		if unit == "tokens" {
			return "No daily token limit"
		}
		return "No daily spending limit"
	}
	if unit == "tokens" {
		return strconv.FormatInt(amount, 10) + " tokens a day"
	}
	fraction := strconv.FormatInt(amount%1_000_000, 10)
	fraction = strings.Repeat("0", 6-len(fraction)) + fraction
	fraction = strings.TrimRight(fraction, "0")
	for len(fraction) < 2 {
		fraction += "0"
	}
	return strconv.FormatInt(amount/1_000_000, 10) + "." + fraction + " a day"
}

// Keep the UTF-16 helper local: secure values never pass through this text
// helper, only public labels do.
func utf16String(s string) *uint16 { return utf16(s) }

func (d *permissionDialog) credentialStatusHeight(width int) int {
	f := d.prompt.CredentialForm
	text := f.PendingReason
	if d.credential != nil && d.credential.statusText != "" {
		text = d.credential.statusText
	}
	return max(d.px(40), d.measure(text, d.fonts[0], max(1, width-2*d.px(20))))
}

func (d *permissionDialog) credentialHeight(width int) int {
	if d.credential == nil {
		return 0
	}
	if d.prompt.CredentialForm.NeedsKey {
		return d.px(144) + d.credentialStatusHeight(width)
	}
	return d.px(80) + d.credentialStatusHeight(width)
}
func (d *permissionDialog) layoutCredentialForm(y, width int) int {
	c := d.credential
	if c == nil {
		return y
	}
	pad := d.px(20)
	wide := width - 2*pad
	place := func(h uintptr, x, w, height int) {
		if h != 0 {
			sendMessage.Call(h, wmSetFont, d.fonts[0], 0)
			moveWindow.Call(h, uintptr(x), uintptr(y), uintptr(w), uintptr(d.px(height)), 1)
			showWindow.Call(h, 1)
		}
	}
	if c.key != 0 && d.prompt.CredentialForm.NeedsKey {
		place(c.label, pad, wide, 24)
		y += d.px(24)
		keyWidth := wide
		if c.getKey != 0 {
			keyWidth -= d.px(108)
		}
		place(c.key, pad, keyWidth, 28)
		place(c.getKey, pad+keyWidth+d.px(8), d.px(100), 28)
		y += d.px(40)
	} else {
		for _, h := range []uintptr{c.key, c.label, c.getKey} {
			if h != 0 {
				showWindow.Call(h, 0)
			}
		}
	}
	place(c.budgetLabel, pad, wide, 24)
	y += d.px(24)
	place(c.budget, pad, wide, 160)
	y += d.px(36)
	statusHeight := d.credentialStatusHeight(width)
	sendMessage.Call(c.status, wmSetFont, d.fonts[0], 0)
	moveWindow.Call(c.status, uintptr(pad), uintptr(y), uintptr(wide), uintptr(statusHeight), 1)
	showWindow.Call(c.status, 1)
	y += statusHeight + d.px(12)
	return y
}

func (d *permissionDialog) updateCredentialAllow() {
	f := d.prompt.CredentialForm
	if f == nil {
		return
	}
	enabled := f.PendingReason == "" && f.Allow != nil && !d.credential.running
	if f.NeedsKey {
		n, _, _ := credentialGetTextLength.Call(d.credential.key)
		enabled = enabled && n > 0 && n <= 16384
	}
	if d.start.IsZero() || !permissionAnswerAllowed(false, time.Since(d.start), 0, len(d.prompt.Choices)) {
		enabled = false
	}
	for i, choice := range d.prompt.Choices {
		if choice.Value == "allow" {
			permissionEnableWindow.Call(d.buttons[i], boolUint(enabled))
		}
	}
}

func boolUint(v bool) uintptr {
	if v {
		return 1
	}
	return 0
}

func (d *permissionDialog) credentialCommand(id int32, notification uintptr) bool {
	c := d.credential
	if c == nil {
		return false
	}
	f := d.prompt.CredentialForm
	if id == credentialKeyID && notification == 0x0300 {
		d.updateCredentialAllow()
		return true
	}
	if id == credentialGetKeyID && notification == 0 {
		if f.GetKey != nil {
			f.GetKey()
		}
		return true
	}
	return false
}

func (d *permissionDialog) captureCredential() ([]byte, int64, error) {
	c, f := d.credential, d.prompt.CredentialForm
	if c == nil || f == nil || f.Allow == nil || f.PendingReason != "" {
		return nil, 0, errors.New("Daily limits for this app are not available yet. Nothing was allowed.")
	}
	selected, _, _ := sendMessage.Call(c.budget, 0x0147, 0, 0)
	if selected >= uintptr(len(c.amounts)) {
		return nil, 0, errors.New("Choose a daily limit.")
	}
	var key []byte
	if f.NeedsKey {
		n, _, _ := credentialGetTextLength.Call(c.key)
		if n == 0 || n > 16384 {
			return nil, 0, errors.New("Paste the key first.")
		}
		buf := make([]uint16, n+1)
		defer clear(buf)
		count, _, _ := credentialGetText.Call(c.key, uintptr(unsafe.Pointer(&buf[0])), n+1)
		if count != n {
			return nil, 0, errors.New("The key could not be read.")
		}
		runes := unicodeutf16.Decode(buf[:n])
		defer clear(runes)
		key = make([]byte, 0, len(runes)*utf8.UTFMax)
		for _, r := range runes {
			key = utf8.AppendRune(key, r)
		}
	}
	d.clearCredentialField()
	return key, c.amounts[selected], nil
}

func (d *permissionDialog) startCredential(choice uintptr) error {
	c := d.credential
	if c == nil || c.running {
		return errors.New("This connection is already being checked.")
	}
	key, amount, err := d.captureCredential()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel, c.pending, c.done, c.running, c.choice = cancel, make(chan error, 1), make(chan struct{}), true, choice
	pending := c.pending
	done := c.done
	allow := d.prompt.CredentialForm.Allow
	for _, h := range []uintptr{c.key, c.budget, c.getKey} {
		if h != 0 {
			permissionEnableWindow.Call(h, 0)
		}
	}
	d.updateCredentialAllow()
	setWindowText.Call(c.status, uintptr(unsafe.Pointer(utf16("Connecting…"))))
	go func() {
		defer close(done)
		err := allow(ctx, key, amount)
		clear(key)
		pending <- err
		postMessage.Call(d.hwnd, credentialWMComplete, 0, 0)
	}()
	return nil
}

func (d *permissionDialog) finishCredential() (bool, error) {
	c := d.credential
	if c == nil || !c.running || c.pending == nil {
		return false, nil
	}
	select {
	case err := <-c.pending:
		<-c.done
		c.running = false
		c.cancel()
		c.cancel = nil
		c.pending = nil
		c.done = nil
		for _, h := range []uintptr{c.key, c.budget, c.getKey} {
			if h != 0 {
				permissionEnableWindow.Call(h, 1)
			}
		}
		return true, d.credentialResult(err)
	default:
		return false, nil
	}
}

func (d *permissionDialog) credentialResult(err error) error {
	c, f := d.credential, d.prompt.CredentialForm
	if f.NeedsKeyNow != nil {
		f.NeedsKey = f.NeedsKeyNow()
	}
	if f.RetryOnly != nil && f.RetryOnly() {
		permissionEnableWindow.Call(c.budget, 0)
		for i, choice := range d.prompt.Choices {
			label := "Close"
			if choice.Value == "allow" {
				label = "Check again"
			}
			setWindowText.Call(d.buttons[i], uintptr(unsafe.Pointer(utf16(label))))
		}
	}
	if err != nil {
		message := "That key didn't work. Nothing was allowed."
		if safe, ok := err.(interface{ NativeCredentialMessage() string }); ok {
			message = safe.NativeCredentialMessage()
		}
		if f.Warning != nil {
			if warning := f.Warning(); warning != "" {
				message = warning + "\n" + message
			}
		}
		c.statusText = message
		setWindowText.Call(c.status, uintptr(unsafe.Pointer(utf16(message))))
		d.layout(nil)
		d.updateCredentialAllow()
		return errors.New(message)
	}
	d.layout(nil)
	return nil
}

func (d *permissionDialog) cancelCredential() {
	if d.credential != nil && d.credential.cancel != nil {
		d.credential.cancel()
	}
	d.clearCredentialField()
}

func (d *permissionDialog) joinCredential() {
	c := d.credential
	if c == nil || !c.running {
		return
	}
	c.cancel()
	<-c.done
	c.running = false
	c.cancel = nil
	c.pending = nil
	c.done = nil
}

func (d *permissionDialog) clearCredentialField() {
	if d.credential != nil && d.credential.key != 0 {
		setWindowText.Call(d.credential.key, uintptr(unsafe.Pointer(utf16String(""))))
	}
}
