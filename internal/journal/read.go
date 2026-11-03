package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/safefile"
	"github.com/lictor-ai/stele/internal/sign"
)

// maxRead caps a read of one log file: MaxSize, and room for the one line
// that may start a file past it.
const maxRead = MaxSize + 1<<20

// Read calls fn with each line of root's log that key signed, oldest first:
// the rotated file, then the current one. body is the line's JSON without
// its MAC; a kind Read does not know is passed on for fn to skip. A line
// that does not verify (torn by a crash, unsigned, signed on another
// machine, or edited) is skipped and counted in bad. An error from fn stops
// the read and is returned.
func Read(root string, key sign.Key, fn func(kind Kind, body []byte) error) (bad int, err error) {
	dir := filepath.Join(root, index.Dir)
	for _, name := range []string{Rotated, FileName} {
		data, err := safefile.ReadRegular(filepath.Join(dir, name), maxRead)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return bad, err
		}
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
