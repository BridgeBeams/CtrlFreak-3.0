package host

import (
	"os"
	"path/filepath"
	"runtime"
)

// DataDir is where CtrlFreak keeps its logs and heartbeat file. On Windows this
// is C:\ProgramData\CtrlFreak, readable by an administrator and writable by both
// the SYSTEM supervisor and the elevated helper. Elsewhere it falls back to a
// folder next to the exe.
func DataDir() string {
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "CtrlFreak")
		}
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "ctrlfreak-data")
	}
	return "ctrlfreak-data"
}

func logDir() string        { return filepath.Join(DataDir(), "logs") }
func heartbeatPath() string { return filepath.Join(DataDir(), "helper.hb") }
