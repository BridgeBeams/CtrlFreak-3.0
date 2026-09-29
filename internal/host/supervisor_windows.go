//go:build windows

package host

import (
	"log"
	"os/exec"
	"syscall"
	"time"
)

// helperTaskName is the logon scheduled task the installer registers. Windows
// runs it ELEVATED and inside the interactive desktop session, which is what
// lets the helper capture the screen and inject input. The supervisor's whole
// job is to keep this task's process alive.
const helperTaskName = "CtrlFreak Helper"

// SuperviseHelper runs the SYSTEM supervisor loop. It watches the helper's
// heartbeat and re-runs the logon task whenever the helper is missing or stuck
// (a crash, or the "Restart agent" command, which exits the helper). This is the
// piece that makes a machine self-heal instead of silently vanishing.
func SuperviseHelper(stop <-chan struct{}) {
	var lastRun time.Time
	runHelperTask()
	lastRun = time.Now()

	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		age, ok := HeartbeatAge()
		stale := !ok || age > 12*time.Second
		if stale && time.Since(lastRun) > 15*time.Second {
			if ok {
				log.Printf("helper heartbeat stale (%s); relaunching task", age.Round(time.Second))
			} else {
				log.Printf("no helper heartbeat yet; launching task")
			}
			runHelperTask()
			lastRun = time.Now()
		}
	}
}

// runHelperTask asks Task Scheduler to start the helper. Task Scheduler launches
// it elevated in the active session (per the task's definition). If no one is
// logged on, the run is a harmless no-op until someone is.
func runHelperTask() {
	cmd := exec.Command("schtasks", "/run", "/tn", helperTaskName)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("schtasks /run %q failed: %v: %s", helperTaskName, err, string(out))
	}
}
