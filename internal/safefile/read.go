package safefile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"syscall"
	"time"
)

var (
	// ErrNotRegular is a file that is a symlink, a device or anything but a
	// plain file. A cloned repository can commit a symlink to /dev/zero.
	ErrNotRegular = errors.New("not a regular file")
	// ErrTooLarge is a file over the limit ReadRegular was given.
	ErrTooLarge = errors.New("file too large")
)

// transientWait is how long a read or a replace keeps retrying while another
// process has the file open on Windows.
const transientWait = 2 * time.Second

// ReadRegular reads path if it is a regular file of at most limit bytes. It
// never follows a symlink and never blocks on a device, so a file a cloned
// repository planted cannot hang stele or exhaust its memory.
//
// On Windows a read that meets another process's rename fails with a sharing
// violation; it is retried for a while, as Replace retries its rename.
func ReadRegular(path string, limit int64) ([]byte, error) {
	return readRetrying(path, limit, os.Lstat)
}

// ReadFollowing is ReadRegular for a file a user may link on purpose: a
// symlink is followed, but it must end at a regular file.
func ReadFollowing(path string, limit int64) ([]byte, error) {
	return readRetrying(path, limit, os.Stat)
}

func readRetrying(path string, limit int64, stat func(string) (fs.FileInfo, error)) ([]byte, error) {
	deadline := time.Now().Add(transientWait)
	backoff := time.Millisecond
	for {
		data, err := readRegular(path, limit, stat)
		if err == nil || !transient(err) || time.Now().After(deadline) {
			return data, err
		}
		time.Sleep(backoff)
		if backoff < 50*time.Millisecond {
			backoff *= 2
		}
	}
}

// readRegular reads path, which stat must find to be a regular file.
func readRegular(path string, limit int64, stat func(string) (fs.FileInfo, error)) ([]byte, error) {
	info, err := stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if runtime.GOOS != "windows" && errors.Is(err, syscall.ENOTDIR) {
		// A parent is a file, not a directory: a clone committed a file
		// named .stele, say. Report it like any other non-file, so callers
		// regenerate or refuse it with their recovery step. (Windows calls
		// that "path not found", which is already ErrNotExist.)
		return nil, fmt.Errorf("%s: a parent is not a directory: %w", path, ErrNotRegular)
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", path, ErrNotRegular)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: %w (over %d bytes)", path, ErrTooLarge, limit)
	}
	return data, nil
}

// transient reports whether err is Windows refusing access because another
// process has the file open or is renaming over it: access denied, or a
// sharing violation. Elsewhere nothing is transient.
func transient(err error) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	const (
		errorAccessDenied     = 5
		errorSharingViolation = 32
	)
	return errno == errorAccessDenied || errno == errorSharingViolation
}
