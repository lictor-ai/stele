// Package safefile writes files so a crash never leaves one half-written,
// and provides the lock files and directory checks that go with that.
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
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	// CreateTemp makes the file 0600, and the rename would carry that over.
	if err := tmp.Chmod(0o644); err != nil {
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
	// it closes the file, so retry for as long as ReadRegular would.
	deadline := time.Now().Add(transientWait)
	backoff := time.Millisecond
	for {
		err = os.Rename(tmp.Name(), path)
		if err == nil {
			return nil
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
