//go:build unix

package enforce

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A named pipe nobody writes to would block the hook until its timeout.
func TestLoadFIFO(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, FileName), 0o644); err != nil {
		t.Skipf("no named pipes here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Load(root)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Load: no error on a named pipe")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Load blocked on a named pipe")
	}
}
