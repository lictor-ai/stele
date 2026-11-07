package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/sign"
)

// Pos is where a reader got to in the log: just past one signed line. A
// rotation renames the file without changing it, so a Pos names its file by
// what is in it, not by its name (D83).
type Pos struct {
	// File is the MAC of the first signed line of the file the line is in.
	File string `json:"file"`
	// Offset is the byte just past the line, its newline included.
	Offset int64 `json:"offset"`
	// Last is the line's own MAC.
	Last string `json:"last"`
}

// ReadAfter calls fn with each line of root's log that key signed and that
// comes after after, oldest first: line is the signed line as written,
// without its newline, body its JSON without the MAC, and at the Pos just
// past it. A nil after reads the whole log. When after's file is no longer
// there (the log rotated twice since, or was deleted), the whole log is read
// and gap is true. A line that does not verify is skipped and counted in bad.
//
// Both files are read under the writers' lock, which is released before fn
// is called: what fn does never holds up the hook.
func ReadAfter(root string, key sign.Key, after *Pos, fn func(kind Kind, line, body []byte, at Pos) error) (bad int, gap bool, err error) {
	dir := filepath.Join(root, index.Dir)
	if info, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) || (err == nil && !info.IsDir()) {
		return 0, after != nil, nil
	}
	files, bad, err := readFiles(dir)
	if err != nil {
		return bad, false, err
	}
	ids := make([]string, len(files))
	for i, f := range files {
		ids[i] = firstMAC(key, f.data)
	}

	startFile, startAt := 0, int64(0)
	if after != nil {
		found := -1
		for i, id := range ids {
			if id != "" && id == after.File {
				found = i
			}
		}
		if found < 0 {
			gap = true
		} else {
			startFile = found
			startAt, gap = resume(files[found].data, after)
		}
	}

	for i := startFile; i < len(files); i++ {
		data := files[i].data
		off := int64(0)
		if i == startFile {
			off = startAt
		}
		for off < int64(len(data)) {
			end := int64(len(data))
			if j := bytes.IndexByte(data[off:], '\n'); j >= 0 {
				end = off + int64(j) + 1
			}
			line := bytes.TrimRight(data[off:end], "\r\n")
			off = end
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
			at := Pos{File: ids[i], Offset: end, Last: macOf(line)}
			if err := fn(h.Kind, line, body, at); err != nil {
				return bad, gap, err
			}
		}
	}
	return bad, gap, nil
}

// resume is where to start reading data, the file after names, to read what
// comes after after's line. The line is looked for where after says; a file
// changed by hand since moves it, and then the last copy of the line is
// looked for. With neither, the whole file is read and gap is true.
func resume(data []byte, after *Pos) (start int64, gap bool) {
	if after.Offset > 0 && after.Offset <= int64(len(data)) {
		if lineEndingAt(data, after.Offset) == after.Last {
			return after.Offset, false
		}
	}
	var at int64 = -1
	var off int64
	for off < int64(len(data)) {
		end := int64(len(data))
		if j := bytes.IndexByte(data[off:], '\n'); j >= 0 {
			end = off + int64(j) + 1
		}
		if macOf(bytes.TrimRight(data[off:end], "\r\n")) == after.Last {
			at = end
		}
		off = end
	}
	if at < 0 {
		return 0, true
	}
	return at, false
}

// lineEndingAt is the MAC of the line data holds just before end.
func lineEndingAt(data []byte, end int64) string {
	line := bytes.TrimRight(data[:end], "\r\n")
	if i := bytes.LastIndexByte(line, '\n'); i >= 0 {
		line = line[i+1:]
	}
	return macOf(line)
}

// firstMAC is the MAC of the first line of data that key signed: what names
// the file wherever it is renamed to. "" when there is none.
func firstMAC(key sign.Key, data []byte) string {
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if _, ok := decode(key, line); ok {
			return macOf(line)
		}
	}
	return ""
}

// macOf is the MAC a line ends with, or "" when it has none.
func macOf(line []byte) string {
	n := len(line)
	if n < tailLen+2 || string(line[n-tailLen:n-tailLen+len(macField)]) != macField || string(line[n-2:]) != `"}` {
		return ""
	}
	return string(line[n-macLen-2 : n-2])
}
