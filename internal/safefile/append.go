package safefile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// Append adds line, one whole line ending in '\n', to path, holding the lock
// path+".lock" so two writers never interleave. It waits at most wait for
// another writer and then gives up: for a log, where a dropped line costs
// little and waiting would hold up the agent.
//
// Like ReadRegular it refuses anything but a plain file at path, a symlink or
// junction included, and never writes through one. Unlike Replace it does not
// move such a thing aside: it leaves it and returns ErrNotRegular.
//
// When the line would take the file past max bytes, path is first renamed to
// rotated, replacing an older rotated file, so the log and its one older file
// stay about max each. The cap is soft: on Windows the rename fails while any
// process has the file open, and the line is then appended anyway, up to
// twice max; past that it is dropped with the rename's error.
//
// A file whose last byte is not a newline, cut short by a crash or by hand,
// gets one before line, so line is never glued to the torn one.
func Append(path, rotated string, line []byte, max int64, wait time.Duration) error {
	unlock, err := Lock(path+".lock", wait, 10*time.Second)
	if err != nil {
		return err
	}
	defer unlock()

	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s: %w", path, ErrNotRegular)
	case info.Size() > 0 && info.Size()+int64(len(line)) > max:
		if err := os.Rename(path, rotated); err != nil && info.Size()+int64(len(line)) > 2*max {
			return err
		}
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|noFollow, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	// Where opening cannot refuse a link (Windows), make sure what was opened
	// is the plain file now at path, not what a link swapped in points to.
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	now, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !now.Mode().IsRegular() || !os.SameFile(opened, now) {
		return fmt.Errorf("%s: %w", path, ErrNotRegular)
	}
	if n := opened.Size(); n > 0 {
		torn, err := lastByteNot(path, n, '\n')
		if err != nil {
			return err
		}
		if torn {
			line = append([]byte{'\n'}, line...)
		}
	}
	if _, err := f.Write(line); err != nil {
		return err
	}
	return f.Close()
}

// lastByteNot reports whether the byte at size-1 of path is not b. It reads
// through a handle of its own: the append handle is write-only.
func lastByteNot(path string, size int64, b byte) (bool, error) {
	r, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer r.Close()
	last := make([]byte, 1)
	if _, err := r.ReadAt(last, size-1); err != nil {
		return false, err
	}
	return last[0] != b, nil
}
