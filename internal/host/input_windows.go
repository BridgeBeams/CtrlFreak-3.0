//go:build windows

package host

import (
	"unsafe"

	"github.com/wthomson/ctrlfreak/internal/protocol"
	"golang.org/x/sys/windows"
)

// winInjector applies remote input via the Win32 SendInput API. It uses only
// syscalls (no cgo), keeping the host a single static .exe.
type winInjector struct {
	user32           *windows.LazyDLL
	procSendInput    *windows.LazyProc
	procGetSysMetric *windows.LazyProc
}

// NewInjector returns the platform input injector.
func NewInjector() Injector {
	u := windows.NewLazySystemDLL("user32.dll")
	return &winInjector{
		user32:           u,
		procSendInput:    u.NewProc("SendInput"),
		procGetSysMetric: u.NewProc("GetSystemMetrics"),
	}
}

// Win32 constants.
const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseeventfMove        = 0x0001
	mouseeventfLeftDown    = 0x0002
	mouseeventfLeftUp      = 0x0004
	mouseeventfRightDown   = 0x0008
	mouseeventfRightUp     = 0x0010
	mouseeventfMiddleDown  = 0x0020
	mouseeventfMiddleUp    = 0x0040
	mouseeventfWheel       = 0x0800
	mouseeventfHWheel      = 0x1000
	mouseeventfAbsolute    = 0x8000
	mouseeventfVirtualDesk = 0x4000

	keyeventfKeyUp    = 0x0002
	keyeventfUnicode  = 0x0004
	keyeventfScancode = 0x0008

	wheelDelta = 120

	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
)

// input mirrors the Win32 INPUT structure. On amd64 the union is 32 bytes and
// the whole struct is 40 bytes (4 type + 4 pad + 32 union), matching cbSize.
type input struct {
	Type  uint32
	_     uint32
	union [32]byte
}

type mouseInput struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

type keybdInput struct {
	WVk         uint16
	WScan       uint16
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

func (w *winInjector) send(in *input) {
	w.procSendInput.Call(1, uintptr(unsafe.Pointer(in)), unsafe.Sizeof(*in))
}

func (w *winInjector) metric(i int) int {
	r, _, _ := w.procGetSysMetric.Call(uintptr(i))
	return int(int32(r))
}

// MouseMove positions the cursor. The controller sends coordinates local to the
// streamed monitor; we add that monitor's virtual-desktop offset and normalize
// to the 0..65535 absolute space that SendInput expects.
func (w *winInjector) MouseMove(monitor int, mons []protocol.MonitorDim, x, y int) {
	offX, offY := 0, 0
	if monitor >= 0 && monitor < len(mons) {
		offX, offY = mons[monitor].X, mons[monitor].Y
	}
	absX, absY := offX+x, offY+y

	vLeft := w.metric(smXVirtualScreen)
	vTop := w.metric(smYVirtualScreen)
	vW := w.metric(smCXVirtualScreen)
	vH := w.metric(smCYVirtualScreen)
	if vW <= 1 {
		vW = 1920
	}
	if vH <= 1 {
		vH = 1080
	}
	nx := int32((int64(absX-vLeft) * 65535) / int64(vW-1))
	ny := int32((int64(absY-vTop) * 65535) / int64(vH-1))

	mi := mouseInput{Dx: nx, Dy: ny, DwFlags: mouseeventfMove | mouseeventfAbsolute | mouseeventfVirtualDesk}
	in := input{Type: inputMouse}
	*(*mouseInput)(unsafe.Pointer(&in.union)) = mi
	w.send(&in)
}

func (w *winInjector) MouseButton(btn string, down bool) {
	var flag uint32
	switch btn {
	case "right":
		flag = pick(down, mouseeventfRightDown, mouseeventfRightUp)
	case "middle":
		flag = pick(down, mouseeventfMiddleDown, mouseeventfMiddleUp)
	default:
		flag = pick(down, mouseeventfLeftDown, mouseeventfLeftUp)
	}
	mi := mouseInput{DwFlags: flag}
	in := input{Type: inputMouse}
	*(*mouseInput)(unsafe.Pointer(&in.union)) = mi
	w.send(&in)
}

func (w *winInjector) MouseScroll(dx, dy int) {
	if dy != 0 {
		mi := mouseInput{MouseData: uint32(int32(dy * wheelDelta)), DwFlags: mouseeventfWheel}
		in := input{Type: inputMouse}
		*(*mouseInput)(unsafe.Pointer(&in.union)) = mi
		w.send(&in)
	}
	if dx != 0 {
		mi := mouseInput{MouseData: uint32(int32(dx * wheelDelta)), DwFlags: mouseeventfHWheel}
		in := input{Type: inputMouse}
		*(*mouseInput)(unsafe.Pointer(&in.union)) = mi
		w.send(&in)
	}
}

func (w *winInjector) Key(code string, down bool) {
	vk, ok := vkFromCode(code)
	if !ok {
		return
	}
	ki := keybdInput{WVk: vk}
	if !down {
		ki.DwFlags = keyeventfKeyUp
	}
	in := input{Type: inputKeyboard}
	*(*keybdInput)(unsafe.Pointer(&in.union)) = ki
	w.send(&in)
}

// TypeText injects a string as Unicode, independent of keyboard layout, using
// KEYEVENTF_UNICODE. This is how the phone's soft keyboard types into the remote
// machine: whatever character it commits gets sent verbatim.
func (w *winInjector) TypeText(s string) {
	for _, r := range s {
		// Characters outside the BMP would need surrogate pairs; the common
		// case (ASCII, accented latin, symbols) is a single UTF-16 unit.
		if r > 0xFFFF {
			continue
		}
		// key down
		down := keybdInput{WScan: uint16(r), DwFlags: keyeventfUnicode}
		ind := input{Type: inputKeyboard}
		*(*keybdInput)(unsafe.Pointer(&ind.union)) = down
		w.send(&ind)
		// key up
		up := keybdInput{WScan: uint16(r), DwFlags: keyeventfUnicode | keyeventfKeyUp}
		inu := input{Type: inputKeyboard}
		*(*keybdInput)(unsafe.Pointer(&inu.union)) = up
		w.send(&inu)
	}
}

func pick(cond bool, a, b uint32) uint32 {
	if cond {
		return a
	}
	return b
}

// vkFromCode maps a browser KeyboardEvent.code to a Windows virtual-key code.
// Covers the common set; extend as needed.
func vkFromCode(code string) (uint16, bool) {
	// Letters
	if len(code) == 4 && code[:3] == "Key" {
		c := code[3]
		if c >= 'A' && c <= 'Z' {
			return uint16(c), true
		}
	}
	// Top-row digits
	if len(code) == 6 && code[:5] == "Digit" {
		c := code[5]
		if c >= '0' && c <= '9' {
			return uint16(c), true
		}
	}
	// Numpad digits
	if len(code) == 7 && code[:6] == "Numpad" {
		c := code[6]
		if c >= '0' && c <= '9' {
			return uint16(0x60 + (c - '0')), true // VK_NUMPAD0..9
		}
	}
	// Function keys F1..F12
	if len(code) >= 2 && code[0] == 'F' {
		switch code {
		case "F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9", "F10", "F11", "F12":
			n := 0
			for _, ch := range code[1:] {
				n = n*10 + int(ch-'0')
			}
			return uint16(0x70 + n - 1), true // VK_F1 = 0x70
		}
	}
	m := map[string]uint16{
		"Enter": 0x0D, "NumpadEnter": 0x0D, "Escape": 0x1B, "Backspace": 0x08,
		"Tab": 0x09, "Space": 0x20,
		"ArrowLeft": 0x25, "ArrowUp": 0x26, "ArrowRight": 0x27, "ArrowDown": 0x28,
		"ShiftLeft": 0xA0, "ShiftRight": 0xA1,
		"ControlLeft": 0xA2, "ControlRight": 0xA3,
		"AltLeft": 0xA4, "AltRight": 0xA5,
		"MetaLeft": 0x5B, "MetaRight": 0x5C,
		"CapsLock": 0x14,
		"Delete":   0x2E, "Insert": 0x2D, "Home": 0x24, "End": 0x23,
		"PageUp": 0x21, "PageDown": 0x22, "PrintScreen": 0x2C,
		"Minus": 0xBD, "Equal": 0xBB, "BracketLeft": 0xDB, "BracketRight": 0xDD,
		"Backslash": 0xDC, "Semicolon": 0xBA, "Quote": 0xDE, "Backquote": 0xC0,
		"Comma": 0xBC, "Period": 0xBE, "Slash": 0xBF,
		"NumpadAdd": 0x6B, "NumpadSubtract": 0x6D, "NumpadMultiply": 0x6A,
		"NumpadDivide": 0x6F, "NumpadDecimal": 0x6E,
	}
	if vk, ok := m[code]; ok {
		return vk, true
	}
	return 0, false
}
