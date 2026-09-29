package host

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

// InitLog points the standard logger at a file under the data dir, so the
// windowless helper and the SYSTEM supervisor leave a trail we can read when
// something goes wrong. role is "helper" or "service". A machine can no longer
// fail silently: the answer is in ProgramData\CtrlFreak\logs.
func InitLog(role string) {
	dir := logDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, role+".log")
	// Keep the log from growing without bound: start fresh once it passes ~2MB.
	if fi, err := os.Stat(path); err == nil && fi.Size() > 2*1024*1024 {
		_ = os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(f, os.Stderr))
	log.SetFlags(log.LstdFlags)
	log.Printf("=== CtrlFreak %s log opened %s ===", role, time.Now().Format(time.RFC3339))
}
