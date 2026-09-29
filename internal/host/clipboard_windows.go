//go:build windows

package host

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows clipboard access via the Win32 API (no cgo). Used to sync text between
// the remote machine and the controller both ways.

var (
	clipUser32   = windows.NewLazySystemDLL("user32.dll")
	clipKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procOpenClipboard    = clipUser32.NewProc("OpenClipboard")
	procCloseClipboard   = clipUser32.NewProc("CloseClipboard")
	procEmptyClipboard   = clipUser32.NewProc("EmptyClipboard")
	procGetClipboardData = clipUser32.NewProc("GetClipboardData")
	procSetClipboardData = clipUser32.NewProc("SetClipboardData")

	procGlobalAlloc  = clipKernel32.NewProc("GlobalAlloc")
	procGlobalFree   = clipKernel32.NewProc("GlobalFree")
	procGlobalLock   = clipKernel32.NewProc("GlobalLock")
	procGlobalUnlock = clipKernel32.NewProc("GlobalUnlock")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// readClipboard returns the current clipboard text, if any.
func readClipboard() (string, bool) {
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return "", false
	}
	defer procCloseClipboard.Call()

	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", false
	}
	defer procGlobalUnlock.Call(h)

	return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(p))), true
}

// writeClipboard replaces the clipboard contents with text.
func writeClipboard(text string) bool {
	u16, err := windows.UTF16FromString(text)
	if err != nil {
		return false
	}
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return false
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()

	nBytes := len(u16) * 2
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(nBytes))
	if h == 0 {
		return false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return false
	}
	dst := unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(u16))
	copy(dst, u16)
	procGlobalUnlock.Call(h)

	// On success the system owns the memory block; on failure we free it.
	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h)
		return false
	}
	return true
}
