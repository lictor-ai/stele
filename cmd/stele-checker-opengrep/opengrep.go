package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/lictor-ai/stele/internal/extcheck"
)

// keys are the rule keys this checker reads, besides stele's check and
// blocking (ADR-0019).
var keys = []string{"rules", "ids", "message"}

// flags go before the rules file on every run. --disable-version-check
// keeps Opengrep from asking for a newer version: with a local rules file it
// then fetches nothing (it has no metrics). --disable-nosem makes a
// "# nosemgrep" comment in the code checked count for nothing: the code
// under review can't switch off its own rule.
var flags = []string{"scan", "--json", "--quiet", "--disable-version-check", "--disable-nosem"}

// maxRulesFile caps the rules file validate reads to find its ids.
const maxRulesFile = 4 << 20

type checker struct{}

// settings are one rule's keys, read.
type settings struct {
	rules   string   // the rules file, absolute
	ids     []string // the Opengrep rule ids that count; none means all
	fileIDs []string // the ids the rules file holds, as found
	message string   // what a violation says, instead of Opengrep's message
}

// read reads r's keys for the project at dir, and returns each problem.
func read(dir string, r extcheck.Rule) (settings, []extcheck.Problem) {
	ps := extcheck.KnownKeys(r, keys...)
	bad := func(format string, args ...any) {
		ps = append(ps, extcheck.Problem{RuleID: r.ID, Message: fmt.Sprintf(format, args...)})
	}
	var s settings
	s.message = r.Meta["message"]
	rel, ok := r.Meta["rules"]
	rel = extcheck.Unquote(rel)
	switch {
	case !ok || rel == "":
		bad("rules: is missing: name an Opengrep rules file in the project, such as rules: .opengrep/sql.yml")
	default:
		p, err := extcheck.ProjectFile(dir, rel)
		if err != nil {
			bad("rules: %v", err)
			break
		}
		s.rules = p
		ids, err := fileIDs(p)
		if err != nil {
			bad("rules: %q: %v", rel, err)
			break
		}
		if len(ids) == 0 {
			bad("rules: %q holds no Opengrep rule: no id: found", rel)
		}
		s.fileIDs = ids
	}
	if v, ok := r.Meta["ids"]; ok {
		ids, err := extcheck.ParseList(v)
		switch {
		case err != nil:
			bad("ids: %v", err)
		case len(ids) == 0:
			bad("ids: is empty: list Opengrep rule ids, such as ids: [python-sqli], or leave ids out for every rule in the file")
		}
		for _, id := range ids {
			if s.fileIDs != nil && !contains(s.fileIDs, id) {
				bad("ids: %q is not the id of a rule in %s", id, rel)
			}
		}
		s.ids = ids
	}
	return s, ps
}

// idLine is a line that gives a rule's id in YAML, "- id: x" or "  id: x",
// and idJSON one in JSON, "\"id\": \"x\"". It is a cheap look, not a YAML
// parser: an id nested deeper, in a rule's metadata, counts too.
var (
	idLine = regexp.MustCompile(`(?m)^[ \t-]*["']?id["']?[ \t]*:[ \t]*["']?([^"'\s,#}]+)`)
	idJSON = regexp.MustCompile(`"id"\s*:\s*"([^"]+)"`)
)

// fileIDs returns the rule ids the rules file at p gives.
func fileIDs(p string) ([]string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxRulesFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRulesFile {
		return nil, fmt.Errorf("is over %d MiB", maxRulesFile>>20)
	}
	ids := []string{}
	for _, re := range []*regexp.Regexp{idLine, idJSON} {
		for _, m := range re.FindAllSubmatch(data, -1) {
			if id := string(m[1]); !contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

func contains(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}

func (checker) Validate(_ context.Context, req extcheck.Request) ([]extcheck.Problem, error) {
	var ps []extcheck.Problem
	for _, r := range req.Rules {
		_, p := read(req.Dir(), r)
		ps = append(ps, p...)
	}
	return ps, nil
}

// group is the rules that name one rules file: Opengrep runs once for them.
type group struct {
	rules   string
	members []member
}

type member struct {
	id string
	s  settings
}

func (checker) Check(ctx context.Context, req extcheck.Request) ([]extcheck.Violation, []extcheck.Skip, error) {
	dir := req.Dir()
	var groups []*group
	byFile := map[string]*group{}
	for _, r := range req.Rules {
		s, ps := read(dir, r)
		if len(ps) > 0 {
			return nil, nil, fmt.Errorf("rule %s: %s", r.ID, ps[0].Message)
		}
		g, ok := byFile[s.rules]
		if !ok {
			g = &group{rules: s.rules}
			byFile[s.rules] = g
			groups = append(groups, g)
		}
		g.members = append(g.members, member{id: r.ID, s: s})
	}
	tool, err := extcheck.FindTool("opengrep", dir)
	if err != nil {
		return nil, nil, fmt.Errorf("%v: install Opengrep (https://github.com/opengrep/opengrep) where PATH finds it", err)
	}
	files := extcheck.NewFiles(dir, req.Files)
	var vs []extcheck.Violation
	var skips []extcheck.Skip
	skipped := map[string]bool{}
	text := texts{}
	for _, g := range groups {
		for _, chunk := range chunks(req.Files, files) {
			args := append(append(append([]string{}, flags...), "--config", g.rules), chunk...)
			out, code, stderr, err := tool.Run(ctx, args...)
			if err != nil {
				return nil, nil, err
			}
			// 0 is a scan, 1 a scan with findings when asked to fail on
			// them; anything else is Opengrep failing.
			res, err := parse(out)
			if code != 0 && code != 1 {
				return nil, nil, tool.Failed(code, stderr+"\n"+strings.Join(res.fatal(), "\n"))
			}
			if err != nil {
				return nil, nil, err
			}
			if f := res.fatal(); len(f) > 0 {
				return nil, nil, fmt.Errorf("opengrep: %s", f[0])
			}
			for _, e := range res.Errors {
				// A rule Opengrep didn't run at all (an IncompatibleRule, at
				// level info, exit 0, about the rules file) fails the check
				// when the rule counts, and so does an error about the rules
				// file that names no rule: reporting the files clean would
				// pass what was never checked. An error about a rule and one
				// file sent, such as a Timeout on a big file, skips that
				// file, below. An error about a rule no member counts is
				// left alone.
				sent := false
				if e.Path != "" {
					_, sent = files.Lookup(e.Path)
				}
				rulesFile := e.Path != "" && extcheck.SamePath(e.Path, g.rules)
				if e.RuleID != "" && g.counts(e.RuleID) && (!sent || rulesFile) || e.RuleID == "" && rulesFile {
					return nil, nil, fmt.Errorf("opengrep did not run the rules in %s: %s", relTo(dir, g.rules), extcheck.OneLine(e.text()))
				}
				if e.Path == "" {
					continue
				}
				f, ok := files.Lookup(e.Path)
				if !ok {
					if e.Level == "error" {
						return nil, nil, fmt.Errorf("opengrep: %s: %s", e.Path, e.text())
					}
					continue
				}
				if !skipped[f] {
					skipped[f] = true
					// Opengrep names the file by the absolute path it was
					// given; the report names it as stele does.
					reason := strings.ReplaceAll(e.text(), e.Path, f)
					skips = append(skips, extcheck.Skip{File: f, Reason: "opengrep: " + reason})
				}
			}
			for _, r := range *res.Results {
				f, ok := files.Lookup(r.Path)
				if !ok {
					return nil, nil, fmt.Errorf("opengrep reported %s, which is not one of the files sent", r.Path)
				}
				line, col := text.position(files.Abs(f), r.Start.Line, r.Start.Col, r.Start.Offset)
				id := g.resolve(r.CheckID)
				for _, m := range g.members {
					if !m.counts(r.CheckID, id) {
						continue
					}
					msg := m.s.message
					if msg == "" {
						shown := id
						if shown == "" {
							shown = r.CheckID
						}
						msg = shown + ": " + extcheck.OneLine(r.Extra.Message)
					}
					vs = append(vs, extcheck.Violation{RuleID: m.id, File: f, Line: line, Column: col, Message: msg})
				}
			}
		}
	}
	return vs, skips, nil
}

// relTo is p relative to dir, with /, for a message; p when it can't be.
func relTo(dir, p string) string {
	if rel, err := filepath.Rel(dir, p); err == nil {
		return filepath.ToSlash(rel)
	}
	return p
}

// resolve is the id in the rules file that checkID, as Opengrep reports
// it, names: the longest that matches, so with both "a.b" and "b" in the
// file, a finding of "a.b" is never one of "b". "" when none does.
func (g *group) resolve(checkID string) string {
	return matchID(checkID, g.members[0].s.fileIDs)
}

// counts reports whether a finding or an error of checkID, as Opengrep
// reports it, counts for some member of g.
func (g *group) counts(checkID string) bool {
	id := g.resolve(checkID)
	for _, m := range g.members {
		if m.counts(checkID, id) {
			return true
		}
	}
	return false
}

// counts reports whether a finding of checkID, resolved to the file's rule
// id, counts for m: every one does when m has no ids. When the look at the
// file found no id for it (a rule written as "- {id: x, …}", say), m's own
// ids are matched against checkID instead, so ids still filters.
func (m member) counts(checkID, id string) bool {
	switch {
	case len(m.s.ids) == 0:
		return true
	case id != "":
		return contains(m.s.ids, id)
	}
	return matchID(checkID, m.s.ids) != ""
}

// matchID returns the one of ids that checkID names, or "". Opengrep, as
// Semgrep, puts the rules file's folder in front of an id from a local file,
// joined with dots ("opengrep.python-sqli"), so the id is checkID or its end
// after a dot. The longest id wins.
func matchID(checkID string, ids []string) string {
	best := ""
	for _, id := range ids {
		if (checkID == id || strings.HasSuffix(checkID, "."+id)) && len(id) > len(best) {
			best = id
		}
	}
	return best
}

// maxArgs is how many bytes of file names go on one command line: Windows
// allows 32,767 characters in all.
func maxArgs() int {
	if runtime.GOOS == "windows" {
		return 24000
	}
	return 128 << 10
}

// chunks splits the files, as absolute paths, into command lines short
// enough to run. An empty list is one empty chunk: nothing to scan.
func chunks(rel []string, files *extcheck.Files) [][]string {
	var out [][]string
	var cur []string
	n := 0
	for _, f := range rel {
		p := files.Abs(f)
		if len(cur) > 0 && n+len(p)+1 > maxArgs() {
			out = append(out, cur)
			cur, n = nil, 0
		}
		cur = append(cur, p)
		n += len(p) + 1
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// output is the part of opengrep --json's output this checker reads.
type output struct {
	Results *[]struct {
		CheckID string `json:"check_id"`
		Path    string `json:"path"`
		Start   struct {
			Line   int `json:"line"`
			Col    int `json:"col"`
			Offset int `json:"offset"`
		} `json:"start"`
		Extra struct {
			Message string `json:"message"`
		} `json:"extra"`
	} `json:"results"`
	Errors []ogError `json:"errors"`
}

type ogError struct {
	Level   string          `json:"level"`
	Type    json.RawMessage `json:"type"`
	Message string          `json:"message"`
	Short   string          `json:"short_msg"` // a rule error has these instead of message
	Long    string          `json:"long_msg"`
	Path    string          `json:"path"`
	RuleID  string          `json:"rule_id"` // the rule it is about, as check_id
}

// text is the error's type and the first line of its message.
func (e ogError) text() string {
	m := e.Message
	if m == "" {
		m = strings.TrimSpace(e.Short + ": " + e.Long)
		m = strings.Trim(m, ": ")
	}
	msg, _, _ := strings.Cut(strings.TrimSpace(m), "\n")
	var t string
	if json.Unmarshal(e.Type, &t) != nil {
		var list []json.RawMessage
		if json.Unmarshal(e.Type, &list) == nil && len(list) > 0 {
			_ = json.Unmarshal(list[0], &t)
		}
	}
	if t == "" {
		return msg
	}
	return t + ": " + msg
}

// fatal is each error that names no file and stops the scan.
func (o output) fatal() []string {
	var out []string
	for _, e := range o.Errors {
		if e.Path == "" && e.Level == "error" {
			out = append(out, e.text())
		}
	}
	return out
}

// parse reads opengrep's JSON output.
func parse(data []byte) (output, error) {
	var o output
	if len(bytes.TrimSpace(data)) == 0 {
		return o, errors.New("opengrep wrote no JSON on stdout")
	}
	if err := json.Unmarshal(data, &o); err != nil {
		return output{}, fmt.Errorf("opengrep's output is not the JSON expected: %v (starts %q)", err, start(data))
	}
	if o.Results == nil {
		return o, errors.New("opengrep's output has no results")
	}
	return o, nil
}

func start(data []byte) string {
	s := strings.TrimSpace(string(data))
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return s
}

// texts holds the files read to place findings, by path.
type texts map[string][]byte

// position is where a finding starts: line from 1 and column counting
// characters from 1. Opengrep gives a byte offset too; the column comes
// from it when it falls in that line, else it is Opengrep's own.
func (t texts) position(path string, line, col, off int) (int, int) {
	if line < 1 {
		line = 1
	}
	if col < 1 {
		col = 1
	}
	data, ok := t[path]
	if !ok {
		data, _ = os.ReadFile(path)
		t[path] = data
	}
	startOf := 0
	for n := 1; n < line; n++ {
		i := bytes.IndexByte(data[startOf:], '\n')
		if i < 0 {
			return line, col
		}
		startOf += i + 1
	}
	end := len(data)
	if i := bytes.IndexByte(data[startOf:], '\n'); i >= 0 {
		end = startOf + i
	}
	if off < startOf || off > end {
		return line, col
	}
	return line, extcheck.Column(string(data[startOf:end]), off-startOf)
}
