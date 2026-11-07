package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/sign"
)

// maxRead caps a read of one log file: the soft cap lets a log grow to twice
// MaxSize when its rotation fails (safefile.Append), and room for one line
// more. Tests lower it.
var maxRead int64 = 2*MaxSize + 1<<20

// readWait is how long Read waits for a writer: a command run by a person,
// not the hook, so it can wait longer than lockWait.
const readWait = 5 * time.Second

// Read calls fn with each line of root's log that key signed, oldest first:
// the rotated file, then the current one. body is the line's JSON without
// its MAC; a kind Read does not know is passed on for fn to skip. A line
// that does not verify (torn by a crash, unsigned, signed on another
// machine, or edited) is skipped and counted in bad, and so is a file too
// large to read, as one. An error from fn stops the read and is returned.
//
// Both files are read under the writers' lock, so a rotation is never seen
// half done, nor a line half written.
func Read(root string, key sign.Key, fn func(kind Kind, body []byte) error) (bad int, err error) {
	dir := filepath.Join(root, index.Dir)
	if info, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) || (err == nil && !info.IsDir()) {
		return 0, nil // stele never ran here: no log
	}
	files, bad, err := readFiles(dir)
	if err != nil {
		return bad, err
	}
	for _, f := range files {
		data := f.data
		for _, line := range bytes.Split(data, []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			body, ok := decode(key, line)
			var h struct {
				Kind Kind `json:"kind"`
			}
			if !ok || json.Unmarshal(body, &h) != nil {
				bad++
				continue
			}
			if err := fn(h.Kind, body); err != nil {
				return bad, err
			}
		}
	}
	return bad, nil
}

// logFile is one of the two log files, as read.
type logFile struct {
	name     string
	data     []byte
	tooLarge bool // left unread: data is nil
}

// readFiles reads the rotated log and the log under the lock, oldest first.
// A file too large is left unread, marked, and counted in bad.
func readFiles(dir string) (files []logFile, bad int, err error) {
	unlock, err := safefile.Lock(filepath.Join(dir, FileName+".lock"), readWait, 10*time.Second)
	if err != nil {
		return nil, 0, err
	}
	defer unlock()
	for _, name := range []string{Rotated, FileName} {
		data, err := safefile.ReadRegular(filepath.Join(dir, name), maxRead)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case errors.Is(err, safefile.ErrTooLarge):
			bad++
			files = append(files, logFile{name: name, tooLarge: true})
		case err != nil:
			return nil, bad, err
		default:
			files = append(files, logFile{name: name, data: data})
		}
	}
	return files, bad, nil
}
