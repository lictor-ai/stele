package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/lictor-ai/stele/internal/extcheck"
)

// keys are the rule keys this checker reads, besides stele's check and
// blocking (ADR-0019).
var keys = []string{"scanners", "severity", "message"}

// scannerNames are the Trivy scanners a rule may name, in Trivy's words.
var scannerNames = []string{"vuln", "secret", "misconfig"}

// severities are Trivy's, lowest first.
var severities = []string{"UNKNOWN", "LOW", "MEDIUM", "HIGH", "CRITICAL"}

// env turns off what Trivy would do on its own beyond its database: the
// check for a newer version and telemetry. A variable a Trivy version
// doesn't read changes nothing.
var env = []string{"TRIVY_SKIP_VERSION_CHECK=true", "TRIVY_DISABLE_TELEMETRY=true", "TRIVY_NO_PROGRESS=true"}

type checker struct{}

// settings are one rule's keys, read.
type settings struct {
	scanners []string // sorted
	severity []string // sorted by rank; none means every severity
	message  string
}

// key is the Trivy run a rule needs: rules with the same one share it.
func (s settings) key() string {
	return strings.Join(s.scanners, ",") + "|" + strings.Join(s.severity, ",")
}

func read(r extcheck.Rule) (settings, []extcheck.Problem) {
	ps := extcheck.KnownKeys(r, keys...)
	bad := func(format string, args ...any) {
		ps = append(ps, extcheck.Problem{RuleID: r.ID, Message: fmt.Sprintf(format, args...)})
	}
	s := settings{message: r.Meta["message"]}
	v, ok := r.Meta["scanners"]
	if !ok {
		bad("scanners: is missing: name what Trivy looks for, such as scanners: [vuln, secret, misconfig]")
	} else {
		list, err := extcheck.ParseList(v)
		switch {
		case err != nil:
			bad("scanners: %v", err)
		case len(list) == 0:
			bad("scanners: is empty: name one or more of %s", strings.Join(scannerNames, ", "))
		}
		for _, sc := range list {
			if !contains(scannerNames, sc) {
				bad("scanners: %q is not one of %s", sc, strings.Join(scannerNames, ", "))
			} else if !contains(s.scanners, sc) {
				s.scanners = append(s.scanners, sc)
			}
		}
		sort.Strings(s.scanners)
	}
	if v, ok := r.Meta["severity"]; ok {
		list, err := extcheck.ParseList(v)
		switch {
		case err != nil:
			bad("severity: %v", err)
		case len(list) == 0:
			bad("severity: is empty: name one or more of %s, or leave severity out for all", strings.Join(severities, ", "))
		}
		for _, sv := range list {
			up := strings.ToUpper(sv)
			if !contains(severities, up) {
				bad("severity: %q is not one of %s", sv, strings.Join(severities, ", "))
			} else if !contains(s.severity, up) {
				s.severity = append(s.severity, up)
			}
		}
		sort.Slice(s.severity, func(i, j int) bool { return rank(s.severity[i]) < rank(s.severity[j]) })
	}
	return s, ps
}

func rank(sev string) int {
	for i, s := range severities {
		if s == sev {
			return i
		}
	}
	return -1
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
		_, p := read(r)
		ps = append(ps, p...)
	}
	return ps, nil
}

type member struct {
	id string
	s  settings
}

func (checker) Check(ctx context.Context, req extcheck.Request) ([]extcheck.Violation, []extcheck.Skip, error) {
	dir := req.Dir()
	var order []string
	groups := map[string][]member{}
	for _, r := range req.Rules {
		s, ps := read(r)
		if len(ps) > 0 {
			return nil, nil, fmt.Errorf("rule %s: %s", r.ID, ps[0].Message)
		}
		k := s.key()
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], member{id: r.ID, s: s})
	}
	tool, err := extcheck.FindTool("trivy", dir, env...)
	if err != nil {
		return nil, nil, fmt.Errorf("%v: install Trivy (https://trivy.dev) where PATH finds it", err)
	}
	files := extcheck.NewFiles(dir, req.Files)
	pos := positions{}
	var vs []extcheck.Violation
	for _, k := range order {
		ms := groups[k]
		s := ms[0].s
		args := []string{"fs", "--format", "json", "--quiet", "--exit-code", "0", "--offline-scan",
			"--scanners", strings.Join(s.scanners, ",")}
		if len(s.severity) > 0 {
			args = append(args, "--severity", strings.Join(s.severity, ","))
		}
		args = append(args, dir)
		out, code, stderr, err := tool.Run(ctx, args...)
		if err != nil {
			return nil, nil, err
		}
		if code != 0 {
			return nil, nil, tool.Failed(code, stderr)
		}
		rep, err := parse(out)
		if err != nil {
			return nil, nil, err
		}
		for _, f := range rep.findings() {
			// Trivy was asked for these scanners and severities; what else
			// it reports is not this rule's.
			if !contains(s.scanners, f.scanner) || len(s.severity) > 0 && !contains(s.severity, f.severity) {
				continue
			}
			file, ok := files.Lookup(f.target)
			if !ok {
				// Trivy scans the whole project; stele sent only the files
				// this check is about.
				continue
			}
			line, col := f.line, 1
			if f.pkg != "" {
				line, col = pos.of(files.Abs(file), f.pkg, f.version)
			}
			for _, m := range ms {
				msg := m.s.message
				if msg == "" {
					msg = f.message
				}
				vs = append(vs, extcheck.Violation{RuleID: m.id, File: file, Line: max(line, 1), Column: col, Message: msg})
			}
		}
	}
	return vs, nil, nil
}

// report is the part of trivy fs --format json's output this checker reads.
type report struct {
	SchemaVersion int `json:"SchemaVersion"`
	Results       []struct {
		Target          string `json:"Target"`
		Vulnerabilities []struct {
			VulnerabilityID  string `json:"VulnerabilityID"`
			PkgName          string `json:"PkgName"`
			InstalledVersion string `json:"InstalledVersion"`
			FixedVersion     string `json:"FixedVersion"`
			Severity         string `json:"Severity"`
			Title            string `json:"Title"`
		} `json:"Vulnerabilities"`
		Secrets []struct {
			RuleID    string `json:"RuleID"`
			Severity  string `json:"Severity"`
			Title     string `json:"Title"`
			StartLine int    `json:"StartLine"`
		} `json:"Secrets"`
		Misconfigurations []struct {
			ID            string `json:"ID"`
			Title         string `json:"Title"`
			Message       string `json:"Message"`
			Severity      string `json:"Severity"`
			Status        string `json:"Status"`
			CauseMetadata struct {
				StartLine int `json:"StartLine"`
			} `json:"CauseMetadata"`
		} `json:"Misconfigurations"`
	} `json:"Results"`
}

// finding is one thing Trivy found, in one target.
type finding struct {
	scanner      string // vuln, secret or misconfig
	severity     string
	target       string
	line         int    // 0 when Trivy gives none
	pkg, version string // a vulnerable package, placed by positions.of
	message      string
}

func (r report) findings() []finding {
	var fs []finding
	for _, res := range r.Results {
		for _, v := range res.Vulnerabilities {
			msg := fmt.Sprintf("%s (%s) in %s %s", v.VulnerabilityID, v.Severity, v.PkgName, v.InstalledVersion)
			if v.FixedVersion != "" {
				msg += ", fixed in " + v.FixedVersion
			}
			if v.Title != "" {
				msg += ": " + v.Title
			}
			fs = append(fs, finding{scanner: "vuln", severity: v.Severity, target: res.Target, pkg: v.PkgName, version: v.InstalledVersion, message: extcheck.OneLine(msg)})
		}
		for _, s := range res.Secrets {
			fs = append(fs, finding{scanner: "secret", severity: s.Severity, target: res.Target, line: s.StartLine,
				message: extcheck.OneLine(fmt.Sprintf("secret %s (%s): %s", s.RuleID, s.Severity, s.Title))})
		}
		for _, m := range res.Misconfigurations {
			if m.Status != "" && m.Status != "FAIL" {
				continue
			}
			msg := fmt.Sprintf("%s (%s): %s", m.ID, m.Severity, m.Title)
			if m.Message != "" {
				msg += ": " + m.Message
			}
			fs = append(fs, finding{scanner: "misconfig", severity: m.Severity, target: res.Target, line: m.CauseMetadata.StartLine, message: extcheck.OneLine(msg)})
		}
	}
	return fs
}

// parse reads Trivy's JSON report. One with no SchemaVersion is not one.
func parse(data []byte) (report, error) {
	var r report
	if len(bytes.TrimSpace(data)) == 0 {
		return r, fmt.Errorf("trivy wrote no JSON on stdout")
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return report{}, fmt.Errorf("trivy's output is not the JSON expected: %v (starts %q)", err, start(data))
	}
	if r.SchemaVersion == 0 {
		return report{}, fmt.Errorf("trivy's output is not a JSON report: no SchemaVersion (starts %q)", start(data))
	}
	return r, nil
}

func start(data []byte) string {
	s := strings.TrimSpace(string(data))
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return s
}

// positions holds the lines of the files read to place packages, by path.
type positions map[string][]string

// of places a vulnerable package, which Trivy gives no line for, in the
// file that pins it (ADR-0019): the first line naming both the package and
// its version, else the first naming the package, at the package's name;
// else line 1, column 1.
func (p positions) of(path, pkg, version string) (int, int) {
	lines, ok := p[path]
	if !ok {
		data, _ := os.ReadFile(path)
		lines = strings.Split(string(data), "\n")
		p[path] = lines
	}
	for _, both := range []bool{true, false} {
		for i, l := range lines {
			at := strings.Index(l, pkg)
			if at < 0 || both && (version == "" || !strings.Contains(l, version)) {
				continue
			}
			return i + 1, extcheck.Column(l, at)
		}
	}
	return 1, 1
}
