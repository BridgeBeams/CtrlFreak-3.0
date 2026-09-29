//go:build !windows

package host

// SuperviseHelper is a no-op on non-Windows platforms. There the supervisor
// mode simply runs the agent inline (see cmd/host program.run), so this exists
// only to satisfy the cross-platform call site.
func SuperviseHelper(stop <-chan struct{}) { <-stop }
