//go:build !windows

package host

import (
	"image"
	"image/color"

	"github.com/wthomson/ctrlfreak/internal/protocol"
)

// stubCapturer lets the host build and run on non-Windows platforms (useful for
// development on Linux/macOS). It streams a simple placeholder frame instead of
// a real desktop. Real Linux/macOS capture is a follow-up: wire an X11/DXGI or
// ScreenCaptureKit backend here behind the same interface.
type stubCapturer struct{ w, h int }

func NewCapturer() Capturer { return &stubCapturer{w: 1280, h: 720} }

func (c *stubCapturer) Monitors() []protocol.MonitorDim {
	return []protocol.MonitorDim{{Index: 0, Width: c.w, Height: c.h}}
}

func (c *stubCapturer) Capture(monitor int) (*image.RGBA, error) {
	img := image.NewRGBA(image.Rect(0, 0, c.w, c.h))
	// A faint checkerboard so it is obviously the placeholder, not a black screen.
	for y := 0; y < c.h; y++ {
		for x := 0; x < c.w; x++ {
			v := uint8(30)
			if (x/40+y/40)%2 == 0 {
				v = 45
			}
			img.Set(x, y, color.RGBA{v, v, v + 10, 255})
		}
	}
	return img, nil
}
