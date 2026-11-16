package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// rescrub scrubs again, in place and with today's scrubber, the saved runs
// of dir (a results/<out>/<time> folder) and the summaries beside it: each
// file as the run wrote it, streams and transcripts line by line, a stderr
// and a summary as text, diffs, setups and results lines as paths. It
// returns the files it changed. A file that still holds private data
// afterwards is named in the error, and left for the owner to look at.
func rescrub(dir string) ([]string, error) {
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("-rescrub %s: not a results folder", dir)
	}
	s := newScrubber("")
	type file struct {
		path string
		how  func([]byte) []byte
	}
	var files []file
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		how := s.paths
		switch {
		case strings.Contains(filepath.ToSlash(rel), ".setup/"):
		case strings.HasSuffix(p, ".jsonl"):
			how = s.jsonl
		case strings.HasSuffix(p, ".stderr.txt"):
			how = s.text
		}
		files = append(files, file{p, how})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ext := range []string{".md", ".regraded.md"} {
		files = append(files, file{dir + ext, s.text})
	}
	for _, ext := range []string{".jsonl", ".regraded.jsonl"} {
		files = append(files, file{dir + ext, s.paths})
	}
	var changed, leaks []string
	for _, f := range files {
		b, err := os.ReadFile(f.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return changed, err
		}
		out := f.how(b)
		// A stream read back drops its CRs: a checkout's line ends are no
		// change.
		if !bytes.Equal(out, b) && !bytes.Equal(out, bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))) {
			if err := os.WriteFile(f.path, out, 0o644); err != nil {
				return changed, err
			}
			changed = append(changed, f.path)
		}
		if what := s.leak(out); what != "" {
			leaks = append(leaks, f.path+" ("+what+")")
		}
	}
	slices.Sort(changed)
	if len(leaks) > 0 {
		return changed, fmt.Errorf("still private data after scrubbing, not published until fixed: %s", strings.Join(leaks, ", "))
	}
	return changed, nil
}
