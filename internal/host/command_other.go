//go:build !windows

package host

import "os/exec"

// doReboot on non-Windows hosts (e.g. a Linux home server). Tries systemctl,
// then falls back to shutdown. Requires the agent to run with privileges to
// reboot (a service running as root does).
func doReboot() error {
	return exec.Command("sh", "-c", "systemctl reboot || shutdown -r now").Run()
}

// runShell runs a command through the shell and returns combined output.
func runShell(command string) string {
	out, err := exec.Command("sh", "-c", command).CombinedOutput()
	s := string(out)
	if err != nil && s == "" {
		s = err.Error()
	}
	if s == "" {
		s = "(command finished, no output)"
	}
	return s
}
