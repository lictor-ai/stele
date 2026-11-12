package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// What a run leaves under results/ is meant to be published, so it is
// scrubbed first: the run's work directory, the temp directory and the
// home directory become <run>, <tmp> and ~, the user's account name
// becomes <user>, the account details Claude Code writes into its
// transcript are dropped, and emails become <email>.

// scrubEnv is what the scrubber hides. Tests set it; newScrubber reads it
// from the machine.
type scrubEnv struct {
	work, tmp, home string
	user            string   // the account name, without a domain
	account         []string // the account's emails and ids: .claude.json, git's user.email
}

// pathRule replaces one spelling of a local path or name.
type pathRule struct {
	re   *regexp.Regexp
	with string
	n    int // the spelling's length: longer ones go first
}

// scrubber scrubs one run's files.
type scrubber struct {
	rules   []pathRule
	home    []*regexp.Regexp // every spelling of the home directory, for leak
	user    *regexp.Regexp   // the account name, or nil
	account []string
	accRe   []pathRule // account, as case-insensitive rules
	// orgs are the organizationUuid values seen while scrubbing, which
	// leak looks for afterwards.
	orgs map[string]bool
}

// newScrubber scrubs the run whose work directory is work ("" for none),
// on this machine.
func newScrubber(work string) *scrubber {
	env := machineEnv()
	env.work = work
	return newScrubberFor(env)
}

var (
	machineOnce sync.Once
	machine     scrubEnv
)

// machineEnv reads, once, what this machine has to hide.
func machineEnv() scrubEnv {
	machineOnce.Do(func() {
		machine.tmp = os.TempDir()
		machine.home, _ = os.UserHomeDir()
		if u, err := user.Current(); err == nil {
			name := u.Username
			if i := strings.LastIndex(name, `\`); i >= 0 {
				name = name[i+1:]
			}
			machine.user = name
		}
		machine.account = claudeAccount(machine.home)
		if out, err := exec.Command("git", "config", "user.email").Output(); err == nil {
			if e := strings.TrimSpace(string(out)); e != "" {
				machine.account = append(machine.account, e)
			}
		}
	})
	return machine
}

// claudeAccount returns the account's organization and account ids and its
// email, from Claude Code's .claude.json: under CLAUDE_CONFIG_DIR, else in
// the home directory. They are never printed.
func claudeAccount(home string) []string {
	path := filepath.Join(home, ".claude.json")
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		path = filepath.Join(dir, ".claude.json")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		OAuthAccount struct {
			OrganizationUUID string `json:"organizationUuid"`
			AccountUUID      string `json:"accountUuid"`
			EmailAddress     string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return nil
	}
	var out []string
	for _, v := range []string{cfg.OAuthAccount.OrganizationUUID, cfg.OAuthAccount.AccountUUID, cfg.OAuthAccount.EmailAddress} {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func newScrubberFor(env scrubEnv) *scrubber {
	s := &scrubber{orgs: map[string]bool{}}
	for _, p := range []struct{ path, with string }{{env.work, "<run>"}, {env.tmp, "<tmp>"}, {env.home, "~"}} {
		for _, r := range pathRules(p.path, p.with) {
			s.rules = append(s.rules, r)
			if p.with == "~" {
				s.home = append(s.home, r.re)
			}
		}
	}
	slices.SortStableFunc(s.rules, func(a, b pathRule) int { return b.n - a.n })
	if len(env.user) >= 3 && !slices.Contains(commonNames, strings.ToLower(env.user)) {
		s.user = regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(env.user) + `\b`)
	}
	for _, a := range env.account {
		if len(a) < 3 {
			continue
		}
		with := "<account>"
		if strings.Contains(a, "@") {
			with = "<email>"
		}
		s.account = append(s.account, a)
		s.accRe = append(s.accRe, pathRule{re: regexp.MustCompile(`(?i)` + regexp.QuoteMeta(a)), with: with})
	}
	return s
}

// commonNames are account names too common to replace: they name no one,
// and replacing them would rewrite ordinary words.
var commonNames = []string{"root", "admin", "user", "runner", "dev", "test"}

// caseFold makes path matches case-insensitive where paths are.
var caseFold = func() string {
	if runtime.GOOS == "windows" {
		return "(?i)"
	}
	return ""
}()

var (
	drivePath = regexp.MustCompile(`^([A-Za-z]):[\\/](.*)$`)
	notAlnum  = regexp.MustCompile(`[^A-Za-z0-9]`)
	pathSep   = regexp.MustCompile(`[\\/]`)
)

// Around a path: what may come before it (not part of a longer path, or a
// JSON escape such as \n), and after it (a separator, the end, or a
// character no name goes on with).
const (
	before = `(^|[^A-Za-z0-9_.\-\\/]|\\[nrt"])`
	after  = `((?:\\+|/)|$|[^A-Za-z0-9_.\-]|\.(?:\s|$))`
	// A slug goes on with -.
	afterSlug = `(-|$|[^A-Za-z0-9_.])`
)

// pathRules are the ways path p can appear in a stream or transcript, each
// one regexp: its parts joined by any run of backslashes or a slash, which
// covers it as is, JSON-escaped once or twice, and with forward slashes;
// as MSYS writes it (/c/Users/x); and as Claude Code's project folder slug
// (C--Users-x). Each comes in its long and 8.3 short forms on Windows. A
// root, or "", has none.
func pathRules(p, with string) []pathRule {
	if p == "" || filepath.Dir(p) == p {
		return nil
	}
	forms := []string{p, shortPath(p), longPath(p)}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		forms = append(forms, r)
	}
	var spellings []string // each a regexp
	for _, f := range forms {
		if f == "" || filepath.Dir(f) == f {
			continue
		}
		spellings = append(spellings, before+sepFlexible(f)+after)
		if m := drivePath.FindStringSubmatch(f); m != nil {
			spellings = append(spellings, before+sepFlexible("/"+m[1]+"/"+m[2])+after)
		}
		// A slug of a one-part path (-tmp) would match ordinary words.
		if strings.Count(strings.Trim(filepath.ToSlash(f), "/"), "/") >= 1 {
			spellings = append(spellings, `(^|[^A-Za-z0-9_.\-])`+regexp.QuoteMeta(notAlnum.ReplaceAllString(f, "-"))+afterSlug)
		}
	}
	slices.Sort(spellings)
	var out []pathRule
	for _, sp := range slices.Compact(spellings) {
		out = append(out, pathRule{regexp.MustCompile(caseFold + sp), with, len(sp)})
	}
	return out
}

// sepFlexible is a regexp for path p with each separator matching any run
// of backslashes or a slash.
func sepFlexible(p string) string {
	parts := pathSep.Split(p, -1)
	for i, x := range parts {
		parts[i] = regexp.QuoteMeta(x)
	}
	return strings.Join(parts, `(?:\\+|/)`)
}

// paths replaces every spelling of the work, temp and home directories,
// the account name, and the account's emails and ids. It is all a diff or
// a setup file gets: the agent's code keeps its own example emails.
func (s *scrubber) paths(b []byte) []byte {
	for _, r := range s.rules {
		// A match takes the character around it, so two paths one
		// character apart need a second pass.
		for i := 0; i < 4; i++ {
			next := r.re.ReplaceAll(b, []byte("${1}"+r.with+"${2}"))
			if bytes.Equal(next, b) {
				break
			}
			b = next
		}
	}
	if s.user != nil {
		b = s.user.ReplaceAllLiteral(b, []byte("<user>"))
	}
	for _, r := range s.accRe {
		b = r.re.ReplaceAllLiteral(b, []byte(r.with))
	}
	return b
}

var email = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)

// keptEmail reports an address that is no one's: the harness's commit
// author, the address of a trailer the agent may write, a git host, or one
// at a domain reserved for examples and tests (RFC 2606, RFC 6761).
func keptEmail(m string) bool {
	m = strings.ToLower(m)
	if m == "noreply@anthropic.com" || strings.HasPrefix(m, "git@") {
		return true
	}
	domain := m[strings.LastIndex(m, "@")+1:]
	for _, d := range []string{"example.com", "example.org", "example.net"} {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	for _, tld := range []string{".test", ".example", ".invalid", ".localhost"} {
		if strings.HasSuffix(domain, tld) {
			return true
		}
	}
	return false
}

// text scrubs a stream or transcript's text: paths, then every email but
// the kept ones.
func (s *scrubber) text(b []byte) []byte {
	return email.ReplaceAllFunc(s.paths(b), func(m []byte) []byte {
		if keptEmail(string(m)) {
			return m
		}
		return []byte("<email>")
	})
}

// droppedKeys are account details, dropped at any depth.
var droppedKeys = []string{
	"organizationUuid", "accountUuid", "emailAddress", "oauthAccount", "userID", "memory_paths",
	"organization_uuid", "account_uuid", "email_address", "user_id",
}

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
	if ev["type"] == "api-request-blob" {
		// The whole API request, Claude Code's system prompt included.
		return []byte(`{"type":"api-request-blob","removed":"by the eval: the raw API request"}`)
	}
	if a, ok := ev["attachment"].(map[string]any); ok {
		if t, _ := a["type"].(string); slices.Contains(droppedAttachments, t) {
			ev["attachment"] = map[string]any{"type": t, "removed": "by the eval: account details"}
			// The same attachment, as Claude Code shows it.
			delete(ev, "rendered")
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
			if str, ok := x.(string); ok && (k == "organizationUuid" || k == "organization_uuid") && str != "" {
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
// account name, the account's emails and ids, or an organizationUuid seen
// while scrubbing. "" when none. It never says the value.
func (s *scrubber) leak(b []byte) string {
	for _, re := range s.home {
		if re.Match(b) {
			return "the home directory"
		}
	}
	if s.user != nil && s.user.Match(b) {
		return "the account name"
	}
	low := bytes.ToLower(b)
	for _, a := range s.account {
		if bytes.Contains(low, []byte(strings.ToLower(a))) {
			return "an account email or id"
		}
	}
	for o := range s.orgs {
		if bytes.Contains(low, []byte(strings.ToLower(o))) {
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
// base.setup folder). A file that still holds private data is replaced by
// a one-line marker, and named in the error.
func (s *scrubber) checkLeaks(base string) error {
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
	var found []string
	for _, f := range all {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if what := s.leak(b); what != "" {
			os.WriteFile(f, []byte("removed by the eval: it still held "+what+" after scrubbing\n"), 0o644)
			found = append(found, filepath.Base(f)+" ("+what+")")
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("removed after scrubbing, private data left: %s", strings.Join(found, ", "))
	}
	return nil
}
