package safefile

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Another process renaming over a file makes a read fail with a sharing
// violation until it is done. A handle that shares nothing does the same, and
// is closed only after the read has begun, so the read cannot pass unretried.
func TestReadsRetryASharingViolation(t *testing.T) {
	for name, read := range map[string]func(string, int64) ([]byte, error){
		"ReadRegular":   ReadRegular,
		"ReadFollowing": ReadFollowing,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "f.json")
			if err := os.WriteFile(path, []byte("12345"), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := syscall.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			h, err := syscall.CreateFile(p, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatal(err)
			}
			release := sync.OnceFunc(func() { syscall.CloseHandle(h) })
			defer release()
			time.AfterFunc(100*time.Millisecond, release)

			got, err := read(path, 5)
			if err != nil || string(got) != "12345" {
				t.Errorf("read = %q, %v; want the content once the handle closed", got, err)
			}
		})
	}
}
