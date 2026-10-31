package safefile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// link is os.Link. Tests replace it.
var link = os.Link

// Create writes data to path only if path does not exist, and returns an
// error satisfying errors.Is(err, fs.ErrExist) if it does. The file appears
// whole or not at all: the content goes to a temp file that is then hard
// linked into place, and a link fails rather than replace an existing file.
// Where hard links fail, createExcl takes over, except for an owner-only file.
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
	err = link(tmp.Name(), path)
	if err == nil || errors.Is(err, fs.ErrExist) || perm&0o077 == 0 {
		return err
	}
	return createExcl(path, data, perm)
}

// createExcl writes data to a new file at path, as Create does where a disk
// has no hard links, FAT and exFAT among them. It still fails rather than
// replace a file, but the file is visible while it is written: a crash leaves
// it partial, and a reader can catch it so. Create keeps owner-only files, the
// signing key, from this: such a disk has no modes to protect them either.
func createExcl(path string, data []byte, perm fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
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
