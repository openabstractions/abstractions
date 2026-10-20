//go:build windows

package win

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A permission prompt plays the sound Windows plays for its own consent
// prompt: the WindowsUAC event of the person's sound scheme ("Windows User
// Account Control" in Sound settings). PlaySound with SND_ALIAS reads that
// scheme, so a sound the person changed or set to none is followed;
// SND_SYSTEM plays it in the system-sounds audio session and volume;
// SND_NODEFAULT keeps silence when the event has no sound. The sound is
// skipped while Windows reports that notifications are held: quiet time,
// presentation mode or a full-screen application. MessageBeep with the
// warning sound is used only when PlaySound cannot be loaded.
var (
	winmm                 = windows.NewLazySystemDLL("winmm.dll")
	permissionPlaySound   = winmm.NewProc("PlaySoundW")
	permissionMessageBeep = user32.NewProc("MessageBeep")
	permissionNotifyState = shell32.NewProc("SHQueryUserNotificationState")
	// windowsUACAlias stays reachable for the asynchronous PlaySound call.
	windowsUACAlias = windows.StringToUTF16Ptr("WindowsUAC")
	// permissionChimeSound plays one prompt's sound; tests replace it.
	permissionChimeSound = playPermissionChime
	// pendingTaskDialogChime hands the chime of the TaskDialog being created
	// to its TDN_CREATED callback on the same UI thread.
	pendingTaskDialogChime atomic.Pointer[questionChime]
)

const (
	sndAsync                 = 0x0001
	sndNoDefault             = 0x0002
	sndAlias                 = 0x00010000
	sndSystem                = 0x00200000
	mbIconWarning            = 0x30
	qunsAcceptsNotifications = 5
	qunsApp                  = 7
)

// questionChime is one prompt's alert sound. It rings at most once, when the
// prompt first appears; relayout, DPI and theme changes never ring it.
type questionChime struct {
	once sync.Once
}

// newQuestionChime gives a permission question its sound. A result dialog,
// an ordinary application choice and an ordinary question get none.
func newQuestionChime(prompt QuestionPrompt) *questionChime {
	if !prompt.Permission || prompt.AppChoice {
		return nil
	}
	return &questionChime{}
}

func (c *questionChime) ring() {
	if c == nil {
		return
	}
	c.once.Do(func() { permissionChimeSound() })
}

func playPermissionChime() {
	if !permissionChimeAllowed(userNotificationState()) {
		return
	}
	if permissionPlaySound.Find() == nil {
		permissionPlaySound.Call(uintptr(unsafe.Pointer(windowsUACAlias)), 0, sndAlias|sndAsync|sndNoDefault|sndSystem)
		return
	}
	permissionMessageBeep.Call(mbIconWarning)
}

// userNotificationState is SHQueryUserNotificationState's QUNS value; known
// is false when Windows did not report one.
func userNotificationState() (state uint32, known bool) {
	if permissionNotifyState.Find() != nil {
		return 0, false
	}
	hr, _, _ := permissionNotifyState.Call(uintptr(unsafe.Pointer(&state)))
	return state, hr == 0
}

// permissionChimeAllowed plays the sound when Windows accepts notifications
// (or a Store app is in front), and when the state is unknown, which leaves
// the sound scheme as the only setting that applies.
func permissionChimeAllowed(state uint32, known bool) bool {
	return !known || state == qunsAcceptsNotifications || state == qunsApp
}
