//go:build windows && !panel

package win

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"syscall"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	comctl32             = windows.NewLazySystemDLL("comctl32.dll")
	taskDialogIndirect   = comctl32.NewProc("TaskDialogIndirect")
	activeTaskDialogHwnd atomic.Uintptr
	// pendingTaskDialogLevel is the header level the next TaskDialog's
	// frame takes at TDN_CREATED.
	pendingTaskDialogLevel atomic.Pointer[PromptLevel]
	taskDialogCallbackID   = syscall.NewCallback(taskDialogNotify)
	setWindowPos           = user32.NewProc("SetWindowPos")

	// questionButtons and questionArmed describe the one question dialog the
	// tray's UI thread can show at a time.
	questionButtons atomic.Int32
	questionArmed   atomic.Bool
	// questionPrompt and questionDefault are that dialog's prompt and its
	// default button, for the real-input check of an answer click.
	questionPrompt  atomic.Pointer[QuestionPrompt]
	questionDefault atomic.Int32
)

// hwndTopmost is HWND_TOPMOST, (HWND)-1.
const hwndTopmost = ^uintptr(0)

const (
	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpShowWindow = 0x0040
)

const (
	tdfAllowDialogCancellation = 0x0008
	tdfUseHIconMain            = 0x0002
	tdcbfCancelButton          = 0x0008
	tdcbfOKButton              = 0x0001
	tdcbfCloseButton           = 0x0020
	tdfUseCommandLinks         = 0x0010
	tdmClickButton             = 0x0466
	tdfCallbackTimer           = 0x0800
	tdmEnableButton            = 0x046F
	tdnCreated                 = 0
	tdnButtonClicked           = 2
	tdnTimer                   = 4
	tdnDestroyed               = 5
	idcCancel                  = 2
	idcClose                   = 8
	firstQuestionButton        = 100
	// questionArmDelay is how long a question's answer buttons stay
	// disabled after the dialog appears, so a key press or click meant for
	// the window underneath cannot answer it.
	questionArmDelay = 500
	// questionDialogRef marks a question dialog in the callback's reference
	// data; the result dialog passes zero.
	questionDialogRef      = 1
	lrSharedTaskDialogIcon = 0x00008000
	// Microsoft's TaskDialogStandardIcon shield bars: ShieldBlueBar uses
	// MAKEINTRESOURCEW(-5), ShieldWarningYellowBar -6 and ShieldGrayBar -9,
	// each passed as a low-word resource rather than HICON. TaskDialog offers
	// no orange bar; its red bar means a blocked program.
	// https://learn.microsoft.com/dotnet/api/system.windows.forms.taskdialogicon
	// https://raw.githubusercontent.com/dotnet/dotnet/b0f34d51fccc69fd334253924abd8d6853fad7aa/src/winforms/src/System.Windows.Forms/System/Windows/Forms/Dialogs/TaskDialog/TaskDialogStandardIcon.cs
	tdShieldBlueBar   = 65531
	tdShieldYellowBar = 65530
	tdShieldGrayBar   = 65527
	// TASKDIALOGCONFIG.cxWidth uses dialog units, retaining native DPI/font scaling.
	permissionQuestionWidth = 320
)

// These Go records retain the typed pointers used by a dialog. The Windows
// SDK packs TASKDIALOG_BUTTON and TASKDIALOGCONFIG at one-byte alignment;
// their native records are encoded separately by packed and call below.
type taskDialogButton struct {
	ID   int32
	Text *uint16
}

type taskDialogConfig struct {
	Size                 uint32
	Parent               syscall.Handle
	Instance             syscall.Handle
	Flags                uint32
	CommonButtons        uint32
	WindowTitle          *uint16
	MainIcon             uintptr
	MainInstruction      *uint16
	Content              *uint16
	ButtonCount          uint32
	Buttons              *taskDialogButton
	DefaultButton        int32
	RadioButtonCount     uint32
	RadioButtons         uintptr
	DefaultRadioButton   int32
	VerificationText     *uint16
	ExpandedInformation  *uint16
	ExpandedControlText  *uint16
	CollapsedControlText *uint16
	Footer               *uint16
	FooterIcon           uintptr
	Callback             uintptr
	CallbackData         uintptr
	Width                uint32
}

const (
	taskDialogPointerSize = unsafe.Sizeof(uintptr(0))
	taskDialogButtonSize  = 4 + taskDialogPointerSize
	taskDialogConfigSize  = 32 + 16*taskDialogPointerSize
)

type packedTaskDialogButton [taskDialogButtonSize]byte
type packedTaskDialogConfig [taskDialogConfigSize]byte

func putTaskDialogPointer(buffer []byte, value uintptr) {
	if taskDialogPointerSize == 8 {
		binary.LittleEndian.PutUint64(buffer, uint64(value))
	} else {
		binary.LittleEndian.PutUint32(buffer, uint32(value))
	}
}

// packed follows commctrl.h's pshpack1 declaration, including hFooterIcon
// before pszFooter. The packed byte records carry pointer values; the logical
// config and buttons keep those pointers visible to Go's garbage collector.
func (config *taskDialogConfig) packed() (packedTaskDialogConfig, []packedTaskDialogButton) {
	buttons := make([]packedTaskDialogButton, config.ButtonCount)
	for i, button := range unsafe.Slice(config.Buttons, config.ButtonCount) {
		binary.LittleEndian.PutUint32(buttons[i][:], uint32(button.ID))
		putTaskDialogPointer(buttons[i][4:], uintptr(unsafe.Pointer(button.Text)))
	}
	var buttonPointer uintptr
	if len(buttons) > 0 {
		buttonPointer = uintptr(unsafe.Pointer(&buttons[0]))
	}
	var native packedTaskDialogConfig
	offset := 0
	put32 := func(value uint32) {
		binary.LittleEndian.PutUint32(native[offset:], value)
		offset += 4
	}
	putPointer := func(value uintptr) {
		putTaskDialogPointer(native[offset:], value)
		offset += int(taskDialogPointerSize)
	}
	put32(config.Size)
	putPointer(uintptr(config.Parent))
	putPointer(uintptr(config.Instance))
	put32(config.Flags)
	put32(config.CommonButtons)
	putPointer(uintptr(unsafe.Pointer(config.WindowTitle)))
	putPointer(config.MainIcon)
	putPointer(uintptr(unsafe.Pointer(config.MainInstruction)))
	putPointer(uintptr(unsafe.Pointer(config.Content)))
	put32(config.ButtonCount)
	putPointer(buttonPointer)
	put32(uint32(config.DefaultButton))
	put32(config.RadioButtonCount)
	putPointer(config.RadioButtons)
	put32(uint32(config.DefaultRadioButton))
	putPointer(uintptr(unsafe.Pointer(config.VerificationText)))
	putPointer(uintptr(unsafe.Pointer(config.ExpandedInformation)))
	putPointer(uintptr(unsafe.Pointer(config.ExpandedControlText)))
	putPointer(uintptr(unsafe.Pointer(config.CollapsedControlText)))
	putPointer(config.FooterIcon)
	putPointer(uintptr(unsafe.Pointer(config.Footer)))
	putPointer(config.Callback)
	putPointer(config.CallbackData)
	put32(config.Width)
	return native, buttons
}

func (config *taskDialogConfig) call(proc uintptr, selected *int32) uintptr {
	native, buttons := config.packed()
	// The shared native-call wrapper marks uintptr pointer arguments as
	// escaping; a callback may collect or move the Go caller's stack.
	hr := comProc(proc).call(uintptr(unsafe.Pointer(&native)), uintptr(unsafe.Pointer(selected)), 0, 0)
	runtime.KeepAlive(config)
	runtime.KeepAlive(buttons)
	return hr
}

// QuestionChoice binds a safe display label to the exact option value read
// from the public Questions API.
type QuestionChoice struct {
	Label string
	Value string
}

// QuestionPrompt contains only presentation text from the Panel's own
// question projection. Both native renderers treat it as plain UTF-16 text.
type QuestionPrompt struct {
	Title       string
	Instruction string
	Content     string
	Details     string
	Footer      string
	Choices     []QuestionChoice
	// Default is the choice value Enter selects. Empty or unknown selects
	// Cancel, which answers nothing.
	Default string
	// Dismiss is the choice value Escape and the close box answer, such as
	// "not now". With it set no separate Cancel button is drawn, since that
	// choice already declines.
	Dismiss   string
	AppChoice bool
	// Permission comes from the Panel's typed or runtime-reserved consent display projection.
	Permission     bool
	PermissionView *PermissionView
	CredentialForm *CredentialForm
	// PairingPrompt leaves unanswered dismissal to X/Escape without an extra button. The Panel
	// sets it only from the reserved runtime key's private display origin.
	PairingPrompt bool
	// Level is the header level the Panel's one level choice made. The
	// renderers color by it and read nothing else for the header.
	Level PromptLevel
	// RealInputOnly marks an authority-granting prompt: an answer button
	// counts only when the person's own mouse or keyboard pressed it
	// (real_input_windows.go). Kind is its Book key, written to the log line
	// of an ignored answer.
	RealInputOnly bool
	Kind          string
	// OnShown runs on the UI thread once the question window is displayed.
	OnShown func()
	parent  uintptr
	// chime is this prompt's one alert sound, shared by both renderers so a
	// Windows 11 layout that falls back to TaskDialog plays it once.
	chime *questionChime
}

// PromptLevel is a native prompt's header level, after the Windows UAC
// banners. The Panel chooses it from the request's recorded evidence and
// risk; it selects colors only and no operating-system right.
type PromptLevel string

const (
	// PromptLevelBlue: OpenAbstractions itself asks.
	PromptLevelBlue PromptLevel = "blue"
	// PromptLevelGray: a verified publisher's ordinary request.
	PromptLevelGray PromptLevel = "gray"
	// PromptLevelYellow: an unverified or unsigned requester. A prompt with
	// no level or an unknown one renders as yellow.
	PromptLevelYellow PromptLevel = "yellow"
	// PromptLevelOrange: a high-risk request, whatever its publisher.
	PromptLevelOrange PromptLevel = "orange"
)

// known is the level the renderers draw: the level itself, or yellow for an
// empty or unknown one.
func (l PromptLevel) known() PromptLevel {
	switch l {
	case PromptLevelBlue, PromptLevelGray, PromptLevelOrange:
		return l
	}
	return PromptLevelYellow
}

// PermissionView is an app-private display projection, never a wire record.
// Identity evidence stays separate from the owning effect disclosures. No
// application wording, answerer tier or color selects operating-system rights.
type PermissionView struct {
	Question, Requester, Publisher, Scope, Effects string
	RequesterProgram                               string
	// Nil preserves Footer for an older projection; explicit empty omits its
	// dismissal hint. A nonempty warning remains visible in the modern dialog.
	Warning *string
}

// Occurrence is one retained Asks Book occurrence as the presenter shows it:
// the Book's id of the question, permission or choice, and the display the
// presenter projected from its retained content. The native question style
// renders only Book occurrences (abstraction-asks CONTRACT, "The native
// question style renders only Book occurrences"); OpenAbstractions' own
// confirmations, reports and failures go to the Panel window or to a
// notification.
type Occurrence struct {
	ID     string
	Prompt QuestionPrompt
}

// errNotOccurrence refuses a prompt that names no Book occurrence: free text
// never reaches the native question style.
var errNotOccurrence = errors.New("native question: only an Asks Book occurrence renders in this style")

// bookOccurrenceID is the shape of a Book id: 1 to 128 bytes of UTF-8 with
// no control character.
func bookOccurrenceID(id string) bool {
	if id == "" || len(id) > 128 || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (w *Window) ShowQuestion(occurrence Occurrence) (string, error) {
	occurrence.Prompt.parent = uintptr(w.h)
	return ShowQuestion(occurrence)
}

// Ordinary application choices have command links and an explicit safe Close
// default. The typed Go owner selects this form; labels remain plain app text.
func questionPresentationFlags(prompt QuestionPrompt) (flags, common uint32, defaultButton int32) {
	flags = tdfAllowDialogCancellation | tdfUseHIconMain | tdfCallbackTimer
	if prompt.AppChoice {
		return flags | tdfUseCommandLinks, tdcbfCloseButton, idcClose
	}
	if prompt.Permission {
		flags &^= tdfUseHIconMain
	}
	if prompt.PairingPrompt && prompt.Permission {
		return flags, 0, questionDefaultButton(prompt.Choices, prompt.Default)
	}
	if prompt.Dismiss != "" {
		// A choice already declines; Escape and the close box answer it.
		def := prompt.Default
		if def == "" {
			def = prompt.Dismiss
		}
		return flags, 0, questionDefaultButton(prompt.Choices, def)
	}
	return flags, tdcbfCancelButton, questionDefaultButton(prompt.Choices, prompt.Default)
}

// questionPresentationIcon is the permission prompt's shield bar for its
// level, or appIcon for a prompt without a header. Orange takes the yellow
// warning bar, since TaskDialog's red bar means a blocked program; the
// prompt's text carries the risk.
func questionPresentationIcon(prompt QuestionPrompt, appIcon uintptr) uintptr {
	if !prompt.Permission || prompt.AppChoice {
		return appIcon
	}
	switch prompt.Level.known() {
	case PromptLevelBlue:
		return tdShieldBlueBar
	case PromptLevelGray:
		return tdShieldGrayBar
	}
	return tdShieldYellowBar
}

func questionPresentationWidth(prompt QuestionPrompt) uint32 {
	if prompt.Permission && !prompt.AppChoice {
		return permissionQuestionWidth
	}
	return 0
}

// questionDefaultButton is the button Enter or Space activates: the choice
// whose value is def, or Cancel.
func questionDefaultButton(choices []QuestionChoice, def string) int32 {
	if def != "" {
		for i, choice := range choices {
			if choice.Value == def {
				return int32(firstQuestionButton + i)
			}
		}
	}
	return idcCancel
}

// questionClickAllowed decides a TDN_BUTTON_CLICKED in a question dialog.
// Cancel always closes; an answer button counts only once the dialog is armed.
func questionClickAllowed(armed bool, button uintptr) bool {
	return armed || button == idcCancel || button == idcClose
}

// questionArmedAfter reports whether a question dialog open for elapsed
// milliseconds accepts answers.
func questionArmedAfter(elapsed uintptr) bool { return elapsed >= questionArmDelay }

// ShowQuestion selects a native permission dialog for the current OS. It is
// owned by this process, so Windows leaves other desktop applications usable.
// An empty option is a deliberate close or cancel, not an answer. It renders
// only a Book occurrence and refuses a prompt that names none.
func ShowQuestion(occurrence Occurrence) (option string, err error) {
	if !bookOccurrenceID(occurrence.ID) {
		return "", errNotOccurrence
	}
	prompt := occurrence.Prompt
	if prompt.chime == nil {
		prompt.chime = newQuestionChime(prompt)
	}
	if prompt.RealInputOnly {
		stop, err := guardRealInput(prompt.Kind)
		if err != nil {
			return "", fmt.Errorf("native question: answers to a %s prompt need the real-input check: %w", prompt.Kind, err)
		}
		defer stop()
	}
	// A secure credential field has no legacy TaskDialog projection. Never
	// downgrade the form into a plain Allow question.
	if prompt.CredentialForm != nil {
		if !modernPermissionFor(prompt, windowsVersion()) {
			return "", errors.New("secure connection is unavailable on this native surface")
		}
		return showPermissionDialog(prompt, false)
	}
	if modernPermissionFor(prompt, windowsVersion()) {
		option, err := showPermissionDialog(prompt, false)
		if !errors.Is(err, errPermissionViewport) {
			return option, err
		}
	}
	return showTaskDialogQuestion(prompt)
}

func showTaskDialogQuestion(prompt QuestionPrompt) (option string, err error) {
	if len(prompt.Choices) == 0 {
		return "", fmt.Errorf("question requires at least one answer option")
	}
	if err := taskDialogIndirect.Find(); err != nil {
		return "", fmt.Errorf("load Windows TaskDialog: %w", err)
	}
	buttons := make([]taskDialogButton, len(prompt.Choices))
	labels := make([]*uint16, len(prompt.Choices))
	values := make(map[int32]string, len(prompt.Choices))
	for i, choice := range prompt.Choices {
		if choice.Value == "" || choice.Label == "" {
			return "", fmt.Errorf("question options require display labels and values")
		}
		label, conversionErr := syscall.UTF16PtrFromString(choice.Label)
		if conversionErr != nil {
			return "", fmt.Errorf("encode question option: %w", conversionErr)
		}
		labels[i] = label
		id := int32(firstQuestionButton + i)
		buttons[i] = taskDialogButton{ID: id, Text: label}
		values[id] = choice.Value
	}

	title, err := taskDialogText(prompt.Title)
	if err != nil {
		return "", err
	}
	instruction, err := taskDialogText(prompt.Instruction)
	if err != nil {
		return "", err
	}
	content, err := taskDialogText(prompt.Content)
	if err != nil {
		return "", err
	}
	details, err := taskDialogText(prompt.Details)
	if err != nil {
		return "", err
	}
	footer, err := taskDialogText(prompt.Footer)
	if err != nil {
		return "", err
	}
	var selected int32
	instance, _, _ := getModuleHandle.Call(0)
	questionButtons.Store(int32(len(buttons)))
	questionArmed.Store(false)
	flags, common, defaultButton := questionPresentationFlags(prompt)
	questionPrompt.Store(&prompt)
	questionDefault.Store(defaultButton)
	defer questionPrompt.Store(nil)
	config := taskDialogConfig{
		Size:                 uint32(taskDialogConfigSize),
		Parent:               syscall.Handle(prompt.parent),
		Flags:                flags,
		CommonButtons:        common,
		WindowTitle:          title,
		MainIcon:             questionPresentationIcon(prompt, loadSharedTaskDialogIcon(instance)),
		MainInstruction:      instruction,
		Content:              content,
		ButtonCount:          uint32(len(buttons)),
		Buttons:              &buttons[0],
		DefaultButton:        defaultButton,
		ExpandedInformation:  details,
		ExpandedControlText:  taskDialogTextPointer("Hide details"),
		CollapsedControlText: taskDialogTextPointer("Show details"),
		Footer:               footer,
		Callback:             taskDialogCallback(),
		CallbackData:         questionDialogRef,
		Width:                questionPresentationWidth(prompt),
	}
	pendingTaskDialogChime.Store(prompt.chime)
	pendingTaskDialogLevel.Store(&prompt.Level)
	hresult := config.call(taskDialogIndirect.Addr(), &selected)
	pendingTaskDialogChime.Store(nil)
	pendingTaskDialogLevel.Store(nil)
	runtime.KeepAlive(labels)
	runtime.KeepAlive(buttons)
	runtime.KeepAlive(prompt)
	if hresult != 0 {
		return "", fmt.Errorf("show Windows permission question: HRESULT %#x", hresult)
	}
	if selected == idcCancel {
		return prompt.Dismiss, nil
	}
	return values[selected], nil
}

func taskDialogCallback() uintptr {
	return taskDialogCallbackID
}

// sFalse is the callback result that keeps a TaskDialog open after a button
// click.
const sFalse = 1

func taskDialogNotify(hwnd, msg, wparam, _ uintptr, ref uintptr) uintptr {
	question := ref == questionDialogRef
	switch uint32(msg) {
	case tdnCreated:
		activeTaskDialogHwnd.Store(hwnd)
		if level := pendingTaskDialogLevel.Swap(nil); level != nil {
			popupFrameTheme(hwnd, *level)
		}
		// The tray process rarely owns the foreground, so Windows would
		// otherwise open the prompt behind the active application. Topmost
		// keeps it visible above the normal desktop without taking input
		// from another program.
		setWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow)
		if question {
			setQuestionButtons(hwnd, false)
			if prompt := questionPrompt.Load(); prompt != nil && prompt.OnShown != nil {
				prompt.OnShown()
			}
			pendingTaskDialogChime.Swap(nil).ring()
		}
	case tdnTimer:
		// wparam is the time since the dialog was created, in milliseconds.
		if question && !questionArmed.Load() && questionArmedAfter(wparam) {
			questionArmed.Store(true)
			armRealInput()
			setQuestionButtons(hwnd, true)
		}
	case tdnButtonClicked:
		if question && !questionClickAllowed(questionArmed.Load(), wparam) {
			return sFalse
		}
		if question && !taskDialogRealInput(hwnd, wparam) {
			return sFalse
		}
	case tdnDestroyed:
		activeTaskDialogHwnd.CompareAndSwap(hwnd, 0)
	}
	return 0
}

// taskDialogRealInput decides an answer click in the question TaskDialog by
// the real-input check. Cancel and Close answer nothing and always close.
func taskDialogRealInput(hwnd, button uintptr) bool {
	prompt := questionPrompt.Load()
	index := int(button) - firstQuestionButton
	if prompt == nil || index < 0 || index >= len(prompt.Choices) {
		return true
	}
	label := prompt.Choices[index].Label
	return realInputAnswer(*prompt, realInputTargetOn(hwnd, findTaskDialogButton(hwnd, label), questionDefault.Load() == int32(button), label))
}

var (
	taskDialogEnumChildren = user32.NewProc("EnumChildWindows")
	taskDialogClassName    = user32.NewProc("GetClassNameW")
	taskDialogFind         struct {
		label string
		found uintptr
	}
	taskDialogFindCallback = syscall.NewCallback(func(h, _ uintptr) uintptr {
		class := make([]uint16, 8)
		taskDialogClassName.Call(h, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
		if syscall.UTF16ToString(class) != "Button" {
			return 1
		}
		n, _, _ := getWindowTextLength.Call(h)
		text := make([]uint16, n+1)
		getWindowText.Call(h, uintptr(unsafe.Pointer(&text[0])), n+1)
		if syscall.UTF16ToString(text) == taskDialogFind.label {
			taskDialogFind.found = h
			return 0
		}
		return 1
	})
)

// findTaskDialogButton is the TaskDialog's button window labelled label. A
// TaskDialog's buttons carry no control IDs; their labels are the prompt's
// distinct choices. It runs on the dialog's UI thread.
func findTaskDialogButton(dialog uintptr, label string) uintptr {
	taskDialogFind.label, taskDialogFind.found = label, 0
	taskDialogEnumChildren.Call(dialog, taskDialogFindCallback, 0)
	return taskDialogFind.found
}

// setQuestionButtons enables or disables every answer button of the open
// question dialog. Cancel stays enabled.
func setQuestionButtons(hwnd uintptr, enabled bool) {
	on := uintptr(0)
	if enabled {
		on = 1
	}
	for i := int32(0); i < questionButtons.Load(); i++ {
		sendMessage.Call(hwnd, tdmEnableButton, uintptr(firstQuestionButton+i), on)
	}
}

func loadSharedTaskDialogIcon(instance uintptr) uintptr {
	icon, _, _ := loadImage.Call(instance, appIconResource, imageIcon, 32, 32, lrDefaultColor|lrSharedTaskDialogIcon)
	if icon == 0 {
		icon, _, _ = loadIcon.Call(0, idiApplication)
	}
	return icon
}

func init() { dismissOnEndSession = DismissActiveQuestion }

// DismissActiveQuestion asks an open question dialog to cancel. The Panel
// calls it from the tray window's WTS lock notification on the same UI thread.
func DismissActiveQuestion() {
	if hwnd := activePermissionDialog.Load(); hwnd != 0 {
		sendMessage.Call(hwnd, permissionWMClose, 0, 0)
		return
	}
	if hwnd := activeTaskDialogHwnd.Load(); hwnd != 0 {
		sendMessage.Call(hwnd, tdmClickButton, idcCancel, 0)
	}
}

func taskDialogText(value string) (*uint16, error) {
	if value == "" {
		return nil, nil
	}
	return syscall.UTF16PtrFromString(value)
}

func taskDialogTextPointer(value string) *uint16 {
	text, _ := syscall.UTF16PtrFromString(value)
	return text
}
