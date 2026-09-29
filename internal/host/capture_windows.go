//go:build windows

package host

import (
	"image"
	"image/color"
	"unsafe"

	"github.com/kbinani/screenshot"
	"github.com/wthomson/ctrlfreak/internal/protocol"
	"golang.org/x/sys/windows"
)

var (
	capUser32        = windows.NewLazySystemDLL("user32.dll")
	procGetCursorPos = capUser32.NewProc("GetCursorPos")
)

type winPoint struct{ X, Y int32 }

func getCursorPos() (int, int, bool) {
	var p winPoint
	r, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	if r == 0 {
		return 0, 0, false
	}
	return int(p.X), int(p.Y), true
}

// arrowMask is a small classic pointer: 'X' = black outline, '.' = white fill.
var arrowMask = []string{
	"X          ",
	"XX         ",
	"X.X        ",
	"X..X       ",
	"X...X      ",
	"X....X     ",
	"X.....X    ",
	"X......X   ",
	"X.......X  ",
	"X........X ",
	"X.....XXXXX",
	"X..X..X    ",
	"X.X X..X   ",
	"XX  X..X   ",
	"X    X..X  ",
	"     X..X  ",
	"      X..X ",
	"      XXXX ",
}

// drawCursor paints the pointer onto the captured frame at (x,y) local coords,
// since Windows screen capture does not include the hardware cursor overlay.
func drawCursor(img *image.RGBA, x, y int) {
	black := color.RGBA{0, 0, 0, 255}
	white := color.RGBA{255, 255, 255, 255}
	b := img.Bounds()
	for row, line := range arrowMask {
		for col := 0; col < len(line); col++ {
			ch := line[col]
			if ch == ' ' {
				continue
			}
			px, py := x+col, y+row
			if px < b.Min.X || px >= b.Max.X || py < b.Min.Y || py >= b.Max.Y {
				continue
			}
			if ch == 'X' {
				img.SetRGBA(px, py, black)
			} else {
				img.SetRGBA(px, py, white)
			}
		}
	}
}

// winCapturer captures the Windows desktop using GDI via kbinani/screenshot.
// This path uses only Windows syscalls (no cgo), so the host cross-compiles to
// a single static .exe.
type winCapturer struct{}

// NewCapturer returns the platform screen capturer.
func NewCapturer() Capturer { return &winCapturer{} }

func (c *winCapturer) Monitors() []protocol.MonitorDim {
	n := screenshot.NumActiveDisplays()
	out := make([]protocol.MonitorDim, 0, n)
	for i := 0; i < n; i++ {
		b := screenshot.GetDisplayBounds(i)
		out = append(out, protocol.MonitorDim{
			Index: i, X: b.Min.X, Y: b.Min.Y, Width: b.Dx(), Height: b.Dy(),
		})
	}
	if len(out) == 0 {
		out = append(out, protocol.MonitorDim{Index: 0, Width: 1920, Height: 1080})
	}
	return out
}

func (c *winCapturer) Capture(monitor int) (*image.RGBA, error) {
	if monitor < 0 || monitor >= screenshot.NumActiveDisplays() {
		monitor = 0
	}
	b := screenshot.GetDisplayBounds(monitor)
	img, err := screenshot.CaptureRect(b)
	if err != nil {
		return img, err
	}
	// Composite the cursor onto the frame at its monitor-local position.
	if cx, cy, ok := getCursorPos(); ok {
		lx, ly := cx-b.Min.X, cy-b.Min.Y
		if lx >= 0 && ly >= 0 && lx < b.Dx() && ly < b.Dy() {
			drawCursor(img, lx, ly)
		}
	}
	return img, nil
}
