package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/wthomson/ctrlfreak/internal/protocol"
)

// DownloadDir is where files sent from a controller land on the host. It is
// created on first use. Kept deliberately simple and visible (not hidden) so
// transfers are easy to find.
var DownloadDir = defaultDownloadDir()

type incomingFile struct {
	meta protocol.FileMsg
	f    *os.File
	got  int64
}

// wireFiles handles the files channel: receiving uploads from the controller,
// and answering directory-listing requests.
func (s *hostSession) wireFiles() {
	if s.files == nil {
		return
	}
	s.incoming = make(map[string]*incomingFile)
	var pendingChunk *protocol.FileMsg
	var mu sync.Mutex

	s.files.OnMessage(func(m webrtc.DataChannelMessage) {
		if m.IsString {
			var fm protocol.FileMsg
			if json.Unmarshal(m.Data, &fm) != nil {
				return
			}
			switch fm.Type {
			case protocol.FileOfferTx: // controller -> host upload
				// Land the file in the folder the controller navigated to, if it
				// gave one and it exists; otherwise the default Downloads folder.
				dir := DownloadDir
				if fm.Dest != "" {
					if fi, err := os.Stat(fm.Dest); err == nil && fi.IsDir() {
						dir = fm.Dest
					}
				}
				if err := os.MkdirAll(dir, 0o755); err != nil {
					s.fileErr(fm.TransferID, "cannot create destination dir")
					return
				}
				safe := filepath.Join(dir, filepath.Base(fm.Name))
				f, err := os.Create(safe)
				if err != nil {
					s.fileErr(fm.TransferID, "cannot create file")
					return
				}
				mu.Lock()
				s.incoming[fm.TransferID] = &incomingFile{meta: fm, f: f}
				mu.Unlock()
				s.sendFileMsg(protocol.FileMsg{Type: protocol.FileAccept, TransferID: fm.TransferID})
			case protocol.FileChunkHdr:
				mu.Lock()
				pendingChunk = &fm
				mu.Unlock()
			case protocol.FileDone:
				mu.Lock()
				in := s.incoming[fm.TransferID]
				delete(s.incoming, fm.TransferID)
				mu.Unlock()
				if in != nil {
					_ = in.f.Close()
				}
			case protocol.FileList:
				s.sendFileMsg(listDir(fm.Path))
			case protocol.FileDrives:
				s.sendFileMsg(driveList())
			case protocol.FileGet:
				go s.sendFileToController(fm.Path)
			}
			return
		}
		// Binary chunk body for the last chunk header.
		mu.Lock()
		hdr := pendingChunk
		pendingChunk = nil
		var in *incomingFile
		if hdr != nil {
			in = s.incoming[hdr.TransferID]
		}
		mu.Unlock()
		if in == nil {
			return
		}
		if _, err := in.f.Write(m.Data); err != nil {
			s.fileErr(in.meta.TransferID, "write failed")
			return
		}
		in.got += int64(len(m.Data))
	})
}

func (s *hostSession) sendFileMsg(fm protocol.FileMsg) {
	b, _ := json.Marshal(fm)
	if s.files != nil {
		_ = s.files.SendText(string(b))
	}
}

// sendFileToController streams a file from disk down to the controller as a
// download: an offer, then chunk headers each followed by a binary chunk, then
// done. Used by the remote file browser (pull a file to your device).
func (s *hostSession) sendFileToController(path string) {
	if s.files == nil || path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.fileErr("", "cannot open "+filepath.Base(path))
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	tid := path // simple unique-ish id for this session
	s.sendFileMsg(protocol.FileMsg{
		Type: protocol.FileOfferTx, TransferID: tid, Name: filepath.Base(path),
		Size: sizeOf(info), Direction: "download",
	})
	buf := make([]byte, 60*1024)
	var offset int64
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			s.sendFileMsg(protocol.FileMsg{Type: protocol.FileChunkHdr, TransferID: tid, Offset: offset})
			if err := s.files.Send(buf[:n]); err != nil {
				return
			}
			offset += int64(n)
			// gentle backpressure
			for s.files.BufferedAmount() > 8*1024*1024 {
				time.Sleep(20 * time.Millisecond)
			}
		}
		if rerr != nil {
			break
		}
	}
	s.sendFileMsg(protocol.FileMsg{Type: protocol.FileDone, TransferID: tid})
}

func sizeOf(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	return info.Size()
}

func (s *hostSession) fileErr(tid, msg string) {
	s.sendFileMsg(protocol.FileMsg{Type: protocol.FileError, TransferID: tid, Message: msg})
}

// listDir returns a directory listing for the file manager. An empty path
// starts at the user's home folder. Parent is set so the controller can offer
// an "up" action, and is empty at a filesystem root.
func listDir(path string) protocol.FileMsg {
	if path == "" {
		path = startDir()
	}
	path = filepath.Clean(path)
	entries, err := os.ReadDir(path)
	if err != nil {
		return protocol.FileMsg{Type: protocol.FileError, Message: err.Error()}
	}
	out := protocol.FileMsg{Type: protocol.FileListResp, Path: path, Parent: parentDir(path)}
	for _, e := range entries {
		info, _ := e.Info()
		var size, mod int64
		if info != nil {
			size = info.Size()
			mod = info.ModTime().Unix()
		}
		out.Entries = append(out.Entries, protocol.FileEntry{
			Name: e.Name(), IsDir: e.IsDir(), Size: size, Mod: mod,
		})
	}
	return out
}

// parentDir returns the parent of path, or "" if path is already a root.
func parentDir(path string) string {
	parent := filepath.Dir(path)
	if parent == path {
		return ""
	}
	return parent
}

// startDir is the folder a fresh file-manager pane opens to.
func startDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return DownloadDir
}

// driveList returns the top-level places to start browsing: drive letters on
// Windows (C:\, D:\, ...) plus the user's home folder, or "/" elsewhere. The
// response has an empty Path, which the controller treats as the "drives" view.
func driveList() protocol.FileMsg {
	out := protocol.FileMsg{Type: protocol.FileListResp, Path: "", Parent: ""}
	if runtime.GOOS == "windows" {
		if home, err := os.UserHomeDir(); err == nil {
			out.Entries = append(out.Entries, protocol.FileEntry{Name: home, IsDir: true})
		}
		for c := 'C'; c <= 'Z'; c++ {
			root := string(c) + `:\`
			if _, err := os.Stat(root); err == nil {
				out.Entries = append(out.Entries, protocol.FileEntry{Name: root, IsDir: true})
			}
		}
	} else {
		out.Entries = append(out.Entries, protocol.FileEntry{Name: "/", IsDir: true})
		if home, err := os.UserHomeDir(); err == nil {
			out.Entries = append(out.Entries, protocol.FileEntry{Name: home, IsDir: true})
		}
	}
	return out
}

func defaultDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "ctrlfreak-downloads"
	}
	return filepath.Join(home, "CtrlFreak Downloads")
}
