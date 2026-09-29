package host

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// RunHeartbeat writes the current time to the heartbeat file every few seconds
// so the supervisor can tell the helper is alive. It stops when stop is closed.
// A stale or missing heartbeat is the supervisor's cue to relaunch the helper.
func RunHeartbeat(stop <-chan struct{}) {
	writeHeartbeat()
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			writeHeartbeat()
		}
	}
}

func writeHeartbeat() {
	if err := os.MkdirAll(DataDir(), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(heartbeatPath(), []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o644)
}

// HeartbeatAge returns how long since the helper last checked in, and whether a
// heartbeat file exists and could be read.
func HeartbeatAge() (time.Duration, bool) {
	b, err := os.ReadFile(heartbeatPath())
	if err != nil {
		return 0, false
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Since(time.Unix(sec, 0)), true
}
