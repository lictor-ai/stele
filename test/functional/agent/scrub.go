package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

// What a run leaves under results/ is meant to be published, so it is
// scrubbed first: the run's work directory, the temp directory and the
// home directory become <run>, <tmp> and ~, the account details Claude Code
// writes into its transcript are dropped, and emails become <email>.

// pathRule replaces one spelling of a local path.
type pathRule struct {
	re   *regexp.Regexp
	with string
	n    int // the spelling's length: longer ones go first
}

// scrubber scrubs one run's files.
type scrubber struct {
	rules []pathRule
	home  []*regexp.Regexp // every spelling of the home directory, for leak
	// orgs are the organizationUuid values seen while scrubbing, which
	// leak looks for afterwards.
	orgs map[string]bool
}

// newScrubber scrubs the run whose work directory is work ("" for none).
func newScrubber(work string) *scrubber {
	s := &scrubber{orgs: map[string]bool{}}
	home, _ := os.UserHomeDir()
	for _, p := range []struct{ path, with string }{{work, "<run>"}, {os.TempDir(), "<tmp>"}, {home, "~"}} {
		for _, sp := range spellings(p.path) {
			re := regexp.MustCompile(caseFold + regexp.QuoteMeta(sp))
			s.rules = append(s.rules, pathRule{re, p.with, len(sp)})
			if p.with == "~" {
				s.home = append(s.home, re)
			}
		}
	}
	slices.SortStableFunc(s.rules, func(a, b pathRule) int { return b.n - a.n })
	return s
}

// caseFold makes path matches case-insensitive where paths are.
var caseFold = func() string {
	if runtime.GOOS == "windows" {
		return "(?i)"
	}
	return ""
}()

var drivePath = regexp.MustCompile(`^([A-Za-z]):[\\/](.*)$`)
var notAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// spellings are the ways path p can appear in a stream or transcript: as
// is, JSON-escaped, with forward slashes, as MSYS writes it (/c/Users/x),
// as Claude Code's project folder slug (C--Users-x), and in its long and
// 8.3 short forms on Windows. A root, or "", has none.
func spellings(p string) []string {
	if p == "" || filepath.Dir(p) == p {
		return nil
	}
	forms := []string{p, shortPath(p), longPath(p)}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		forms = append(forms, r)
	}
	var out []string
	for _, f := range forms {
		if f == "" || filepath.Dir(f) == f {
			continue
		}
		fwd := strings.ReplaceAll(f, `\`, "/")
		out = append(out, f, strings.ReplaceAll(f, `\`, `\\`), fwd)
		if m := drivePath.FindStringSubmatch(f); m != nil {
			out = append(out, "/"+strings.ToLower(m[1])+"/"+strings.ReplaceAll(m[2], `\`, "/"))
		}
		// A slug of a one-part path (-tmp) would match ordinary words.
		if strings.Count(strings.Trim(fwd, "/"), "/") >= 1 {
			out = append(out, notAlnum.ReplaceAllString(f, "-"))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// paths replaces every spelling of the work, temp and home directories.
func (s *scrubber) paths(b []byte) []byte {
	for _, r := range s.rules {
		b = r.re.ReplaceAllLiteral(b, []byte(r.with))
	}
	return b
}

var email = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)

// keptEmails are not anyone's: the harness's commit author, and the
// address of a trailer the agent may write.
var keptEmails = []string{"eval@example.com", "noreply@anthropic.com"}

// text scrubs plain text: paths, then emails.
func (s *scrubber) text(b []byte) []byte {
	return email.ReplaceAllFunc(s.paths(b), func(m []byte) []byte {
		if slices.Contains(keptEmails, strings.ToLower(string(m))) {
			return m
		}
		return []byte("<email>")
	})
}

// droppedKeys are account details, dropped at any depth.
var droppedKeys = []string{"organizationUuid", "accountUuid", "emailAddress", "oauthAccount", "userID", "memory_paths"}

// droppedAttachments are attachments about the account or the user's other
// sessions; remote_session_change carries their commit trailers.
var droppedAttachments = []string{"credential_org", "remote_session_change"}

// transcriptLine scrubs one JSON line of a stream or transcript. A line
// that is not JSON is scrubbed as text.
func (s *scrubber) transcriptLine(line []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var ev map[string]any
	if len(bytes.TrimSpace(line)) == 0 || dec.Decode(&ev) != nil {
		return s.text(line)
	}
	s.collectOrgs(ev)
	if a, ok := ev["attachment"].(map[string]any); ok {
		if t, _ := a["type"].(string); slices.Contains(droppedAttachments, t) {
			ev["attachment"] = map[string]any{"type": t, "removed": "by the eval: account details"}
		} else if c, ok := a["context"].(map[string]any); ok {
			delete(c, "userEmail")
		}
	}
	if ev["type"] == "system" && ev["subtype"] == "init" {
		if ps, ok := ev["plugins"].([]any); ok {
			for _, p := range ps {
				if m, ok := p.(map[string]any); ok {
					delete(m, "path")
				}
			}
		}
	}
	dropKeys(ev)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ev); err != nil {
		return s.text(line)
	}
	return s.text(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

// jsonl scrubs a stream or transcript, line by line.
func (s *scrubber) jsonl(b []byte) []byte {
	lines := bytes.Split(b, []byte("\n"))
	for i, l := range lines {
		lines[i] = s.transcriptLine(bytes.TrimSuffix(l, []byte("\r")))
	}
	return bytes.Join(lines, []byte("\n"))
}

// collectOrgs records every organizationUuid value under v.
func (s *scrubber) collectOrgs(v any) {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if str, ok := x.(string); ok && k == "organizationUuid" && str != "" {
				s.orgs[str] = true
			}
			s.collectOrgs(x)
		}
	case []any:
		for _, x := range v {
			s.collectOrgs(x)
		}
	}
}

func dropKeys(v any) {
	switch v := v.(type) {
	case map[string]any:
		for _, k := range droppedKeys {
			delete(v, k)
		}
		for _, x := range v {
			dropKeys(x)
		}
	case []any:
		for _, x := range v {
			dropKeys(x)
		}
	}
}

// leak says what private data b still holds: the home directory, the
// user's git email, or an organizationUuid seen while scrubbing. "" when
// none.
func (s *scrubber) leak(b []byte, gitEmail string) string {
	for _, re := range s.home {
		if re.Match(b) {
			return "the home directory"
		}
	}
	if gitEmail != "" && bytes.Contains(bytes.ToLower(b), []byte(strings.ToLower(gitEmail))) {
		return "the git user.email"
	}
	for o := range s.orgs {
		if bytes.Contains(b, []byte(o)) {
			return "an organizationUuid"
		}
	}
	return ""
}

// writeScrubbed writes b, scrubbed by how, to path.
func writeScrubbed(path string, b []byte, how func([]byte) []byte) error {
	return os.WriteFile(path, how(b), 0o644)
}

// checkLeaks reads every file of one run under results (base.* and the
// base.setup folder) and reports the first that still holds private data.
func (s *scrubber) checkLeaks(base, gitEmail string) error {
	files, _ := filepath.Glob(base + ".*")
	var all []string
	for _, f := range files {
		filepath.WalkDir(f, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				all = append(all, p)
			}
			return nil
		})
	}
	for _, f := range all {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if what := s.leak(b, gitEmail); what != "" {
			return fmt.Errorf("%s still holds %s after scrubbing", filepath.Base(f), what)
		}
	}
	return nil
}
