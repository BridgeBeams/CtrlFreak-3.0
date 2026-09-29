//go:build !windows

package host

import (
	"log"

	"github.com/wthomson/ctrlfreak/internal/protocol"
)

// stubInjector is a no-op used on non-Windows dev builds. Real Linux/macOS
// input injection (uinput / CGEvent) is a follow-up behind this same interface.
type stubInjector struct{ warned bool }

func NewInjector() Injector { return &stubInjector{} }

func (s *stubInjector) warn() {
	if !s.warned {
		log.Println("input injection is not implemented on this platform (dev stub)")
		s.warned = true
	}
}

func (s *stubInjector) MouseMove(monitor int, mons []protocol.MonitorDim, x, y int) { s.warn() }
func (s *stubInjector) MouseButton(btn string, down bool)                           { s.warn() }
func (s *stubInjector) MouseScroll(dx, dy int)                                      { s.warn() }
func (s *stubInjector) Key(code string, down bool)                                  { s.warn() }
func (s *stubInjector) TypeText(text string)                                        { s.warn() }
