package safefile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Create writes data to path only if path does not exist, and returns an
// error satisfying errors.Is(err, fs.ErrExist) if it does. The file appears
// whole or not at all: the content goes to a temp file that is then hard
// linked into place, and a link fails rather than replace an existing file.
// perm is the file's mode, such as 0644, or 0600 for a secret.
func Create(path string, data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
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
	return os.Link(tmp.Name(), path)
}

// Dir makes sure path is a real directory, creating it if missing. It refuses
// a symlink, a Windows junction, or anything else that is not a directory:
// .stele/ is not covered by the repository's .gitignore, so a cloned
// repository could commit one pointing elsewhere, and stele would then write
// and delete files there.
func Dir(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.Mkdir(path, 0o755); err != nil && !os.IsExist(err) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a plain directory (a symlink?): refusing to write there; delete it and stele recreates it", path)
	}
	return nil
}
