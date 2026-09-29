//go:build windows

package host

import (
	"os/exec"
	"syscall"
)

// hidden keeps spawned console programs from flashing a window on the host's
// screen (the agent itself is a windowless GUI build).
var hidden = &syscall.SysProcAttr{HideWindow: true}

// doReboot triggers a forced reboot with a 10-second warning to any logged-in
// user. Because /t is greater than 0, Windows implies /f, so a stuck app cannot
// veto the reboot. That is exactly what we want when a machine is misbehaving.
func doReboot() error {
	cmd := exec.Command("shutdown", "/r", "/t", "10", "/c", "CtrlFreak remote reboot")
	cmd.SysProcAttr = hidden
	return cmd.Run()
}

// runShell runs a command through cmd.exe and returns combined stdout+stderr.
func runShell(command string) string {
	cmd := exec.Command("cmd", "/c", command)
	cmd.SysProcAttr = hidden
	out, err := cmd.CombinedOutput()
	s := string(out)
	if err != nil && s == "" {
		s = err.Error()
	}
	if s == "" {
		s = "(command finished, no output)"
	}
	return s
}
