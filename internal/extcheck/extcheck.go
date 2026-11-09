// Package extcheck is the checker side of plugin protocol 1 (D78), for the
// checker plugins that ship with stele: stele-checker-opengrep and
// stele-checker-trivy (ADR-0019). It reads one request, hands it to a
// Handler, and writes one answer. It also finds the scanner a plugin runs
// on PATH, outside the project (D43), and runs it with no shell (tool.go).
//
// Only the plugins under cmd/ import it; stele itself never does, so nothing
// here is on the hook's path (D45). It never prints and never exits: the
// answer goes to the writer it is given.
package extcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lictor-ai/stele/internal/plugin"
)

// Request is a request from stele, as docs/plugin-protocol.md describes it.
type Request struct {
	Protocol   int             `json:"protocol"`
	Op         string          `json:"op"`
	ProjectDir string          `json:"project_dir"`
	Root       string          `json:"root"` // project_dir's old name (ADR-0016)
	Config     json.RawMessage `json:"config"`
	Files      []string        `json:"files"`
	Rules      []Rule          `json:"rules"`
}

// Dir is the project's folder: project_dir, else root from an older stele.
func (r Request) Dir() string {
	if r.ProjectDir != "" {
		return r.ProjectDir
	}
	return r.Root
}

// Rule is a rule as stele sends it to a checker.
type Rule struct {
	ID    string            `json:"id"`
	Body  string            `json:"body"`
	Scope []string          `json:"scope"`
	Meta  map[string]string `json:"meta"`
}

// Problem is a rule whose settings a checker can't use.
type Problem struct {
	RuleID  string `json:"rule_id"`
	Message string `json:"message"`
}

// Violation is one finding: a file stele sent, a line and a column from 1.
type Violation struct {
	RuleID  string `json:"rule_id"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message,omitempty"`
}

// Skip is a file the checker could not check, which stele reports.
type Skip struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// Handler is one checker plugin.
type Handler interface {
	// Validate returns a problem for each rule whose settings are wrong.
	Validate(ctx context.Context, req Request) ([]Problem, error)
	// Check runs the scanner on req.Files under req.Dir(). An error is the
	// plugin's: stele fails the check, never takes it as no violations.
	Check(ctx context.Context, req Request) ([]Violation, []Skip, error)
}

// Serve answers the one request on in, for the checker version of h, on
// out. A failure is answered as {"error": …}, so stele says what went wrong.
// It returns the exit status: 0 once an answer is written.
func Serve(ctx context.Context, version string, h Handler, in io.Reader, out io.Writer) int {
	answer := map[string]any{"protocol": plugin.Protocol}
	if err := handle(ctx, version, h, in, answer); err != nil {
		answer = map[string]any{"protocol": plugin.Protocol, "error": err.Error()}
	}
	data, err := json.Marshal(answer)
	if err != nil {
		data, _ = json.Marshal(map[string]any{"protocol": plugin.Protocol, "error": err.Error()})
	}
	if _, err := out.Write(data); err != nil {
		return 1
	}
	return 0
}

func handle(ctx context.Context, version string, h Handler, in io.Reader, answer map[string]any) error {
	var req Request
	dec := json.NewDecoder(io.LimitReader(in, 64<<20))
	if err := dec.Decode(&req); err != nil {
		return fmt.Errorf("bad request: %v", err)
	}
	if req.Protocol != plugin.Protocol {
		return fmt.Errorf("unsupported protocol %d; speaks [%d]", req.Protocol, plugin.Protocol)
	}
	switch req.Op {
	case "describe":
		answer["kind"], answer["protocols"], answer["version"] = plugin.KindChecker, []int{plugin.Protocol}, version
		return nil
	case "validate", "check":
	default:
		return fmt.Errorf("unknown op %q: a checker answers describe, validate and check", req.Op)
	}
	if req.Dir() == "" || !filepath.IsAbs(req.Dir()) {
		return errors.New("the request has no absolute project_dir")
	}
	if req.Op == "validate" {
		ps, err := h.Validate(ctx, req)
		if err != nil {
			return err
		}
		answer["problems"] = nonNil(ps)
		return nil
	}
	vs, skips, err := h.Check(ctx, req)
	if err != nil {
		return err
	}
	answer["violations"], answer["skipped"] = nonNil(vs), nonNil(skips)
	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// KnownKeys returns a problem for each key of r.Meta not in known.
// check and blocking are stele's own and always allowed.
func KnownKeys(r Rule, known ...string) []Problem {
	var ps []Problem
	for k := range r.Meta {
		if k == "check" || k == "blocking" || contains(known, k) {
			continue
		}
		ps = append(ps, Problem{RuleID: r.ID, Message: fmt.Sprintf("%s: is not a key this checker knows; it knows %s", k, strings.Join(known, ", "))})
	}
	sortProblems(ps)
	return ps
}

func contains(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}

func sortProblems(ps []Problem) {
	sort.Slice(ps, func(i, j int) bool { return ps[i].Message < ps[j].Message })
}

// ParseList reads a rule's list value as stele.md writes one: "[a, b]", or
// one bare value as a list of one. Items may be quoted with " or '. An empty
// value is an empty list.
func ParseList(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") {
		if v := Unquote(s); v != "" {
			return []string{v}, nil
		}
		return nil, nil
	}
	if !strings.HasSuffix(s, "]") {
		return nil, errors.New("unclosed list")
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return nil, nil
	}
	var items []string
	var quote byte
	from := 0
	add := func(item string) error {
		v := Unquote(item)
		if v == "" {
			return errors.New("empty list item")
		}
		items = append(items, v)
		return nil
	}
	for j := 0; j < len(inner); j++ {
		c := inner[j]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case (c == '"' || c == '\'') && strings.TrimSpace(inner[from:j]) == "":
			quote = c
		case c == ',':
			if err := add(inner[from:j]); err != nil {
				return nil, err
			}
			from = j + 1
		}
	}
	if quote != 0 {
		return nil, errors.New("unclosed quote")
	}
	if err := add(inner[from:]); err != nil {
		return nil, err
	}
	return items, nil
}

// Unquote takes off one pair of matching quotes, " or ', around s.
func Unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// ErrOutside is a rules path that leaves the project.
var ErrOutside = errors.New("it must stay inside the project")

// ProjectFile returns the absolute path of rel, a file in the project at
// dir. rel is relative, with / or \, and holds no "..": a rules file is
// part of the project and reviewed like its code, never fetched or read
// from elsewhere. Where its links lead must be inside the project too, and
// it must be a regular file.
func ProjectFile(dir, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("is empty")
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) || filepath.VolumeName(rel) != "" {
		return "", fmt.Errorf("%q is absolute: give a path relative to the project's root", rel)
	}
	for _, part := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return "", fmt.Errorf("%q holds \"..\": %w", rel, ErrOutside)
		}
	}
	p := filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(rel, `\`, "/")))
	info, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%q: no such file in the project", rel)
		}
		return "", fmt.Errorf("%q: %v", rel, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%q is not a file", rel)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("%q: %v", rel, err)
	}
	top, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("project_dir: %v", err)
	}
	if !Within(real, top) {
		return "", fmt.Errorf("%q leads to %s: %w", rel, real, ErrOutside)
	}
	return p, nil
}

// Within reports whether p is root or below it. Paths compare without case
// on Windows.
func Within(p, root string) bool {
	if isWindows {
		p, root = strings.ToLower(p), strings.ToLower(root)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Files maps a path a scanner reports back to the file stele sent. stele
// sends paths relative to the project, with /; a scanner reports them
// absolute, or relative to the folder it scanned, with either slash.
type Files struct {
	dir  string
	sent map[string]string // key(path) -> the path as sent
}

// NewFiles is the files sent, under dir.
func NewFiles(dir string, files []string) *Files {
	fs := &Files{dir: dir, sent: make(map[string]string, len(files))}
	for _, f := range files {
		fs.sent[fs.key(f)] = f
	}
	return fs
}

func (fs *Files) key(rel string) string {
	k := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.ReplaceAll(rel, `\`, "/"))))
	if isWindows {
		k = strings.ToLower(k)
	}
	return k
}

// Abs is the absolute path of rel, a file stele sent.
func (fs *Files) Abs(rel string) string { return filepath.Join(fs.dir, filepath.FromSlash(rel)) }

// Lookup returns the file stele sent that p names, p being absolute or
// relative to the project, and whether it was sent.
func (fs *Files) Lookup(p string) (string, bool) {
	p = strings.ReplaceAll(p, `\`, "/")
	if filepath.IsAbs(filepath.FromSlash(p)) {
		rel, err := filepath.Rel(fs.dir, filepath.FromSlash(p))
		if err != nil {
			return "", false
		}
		if isWindows && strings.HasPrefix(rel, "..") {
			// filepath.Rel compares with case on Windows: try without.
			if r, err := filepath.Rel(strings.ToLower(fs.dir), strings.ToLower(filepath.FromSlash(p))); err == nil {
				rel = r
			}
		}
		p = filepath.ToSlash(rel)
	}
	f, ok := fs.sent[fs.key(p)]
	return f, ok
}

// Column is the column, counting characters from 1, of the byte offset off
// in line of text. An offset past the line's end is its end.
func Column(line string, off int) int {
	if off > len(line) {
		off = len(line)
	}
	if off < 0 {
		off = 0
	}
	return utf8.RuneCountInString(line[:off]) + 1
}

// OneLine is s with every run of white space, line breaks included, made
// one space.
func OneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
