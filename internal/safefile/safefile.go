// Package safefile writes files so a crash never leaves one half-written,
// and provides the lock files, directory checks and bounded reads that go
// with that. Nothing under .stele/ is covered by the repository's
// .gitignore, so a cloned repository can commit anything there; these
// helpers never follow a symlink or trust a file stele did not write.
//
// Replace writes the new content to a temp file in the same directory, which
// is then renamed over the target. The target is never truncated. On Windows
// the rename is not atomic and is refused while another process holds the
// target open, so it is retried a few times (architecture §7).
package safefile

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Replace writes data to path. The file ends up 0644 whatever the umask did
// to the temp file, because stele's files are read by other users and CI.
func Replace(path string, data []byte) error {
	return ReplacePerm(path, data, 0o644)
}

// ReplacePerm is Replace leaving the file with mode perm, for a file stele
// rewrites but does not own, such as Claude Code's settings, which may hold
// secrets.
func ReplacePerm(path string, data []byte, perm os.FileMode) error {
	return replace(path, data, perm, transientWait)
}

// ReplaceNow is Replace without waiting for a reader to close the file: for
// a best-effort write on the hook's path, where waiting would hold up the
// agent and a skip costs nothing.
func ReplaceNow(path string, data []byte) error {
	return replace(path, data, 0o644, 0)
}

func replace(path string, data []byte, perm os.FileMode, wait time.Duration) error {
	// Anything but a plain file where the file belongs (a directory, a
	// symlink, a junction) is never stele's: .stele/ is not covered by the
	// repository's .gitignore, so a clone can commit one. Move it aside so
	// the rename puts a real file there. Callers only write files stele can
	// regenerate, or that it has just read as a plain file.
	if err := moveAsideNonFile(path); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	// CreateTemp makes the file 0600, and the rename would carry that over.
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// A reader with the target open makes the rename fail on Windows until
	// it closes the file, so retry that, and only that, for as long as
	// ReadRegular would.
	deadline := time.Now().Add(wait)
	backoff := time.Millisecond
	for {
		err = os.Rename(tmp.Name(), path)
		if err == nil {
			return nil
		}
		if !transient(err) {
			return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("replace %s: %w (an editor, antivirus or search indexer may hold it open)",
				filepath.Base(path), err)
		}
		time.Sleep(backoff)
		if backoff < 50*time.Millisecond {
			backoff *= 2
		}
	}
}

// moveAsideNonFile renames whatever is at path out of the way, unless it is
// a plain file or missing. A non-empty directory moved aside stays, renamed;
// nothing reads it again.
func moveAsideNonFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode().IsRegular() {
		return nil
	}
	token, err := newToken()
	if err != nil {
		return err
	}
	aside := path + "." + token + ".stale"
	if err := os.Rename(path, aside); err != nil {
		return fmt.Errorf("%s is not a file stele wrote, and moving it aside failed: delete it (%w)", path, err)
	}
	os.Remove(aside) // a link or an empty directory goes; anything else stays aside
	return nil
}
