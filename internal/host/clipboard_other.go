//go:build !windows

package host

// Clipboard sync is Windows-only for now; these stubs keep the agent building
// (and harmlessly inert) on other platforms.

func readClipboard() (string, bool) { return "", false }
func writeClipboard(string) bool    { return false }
