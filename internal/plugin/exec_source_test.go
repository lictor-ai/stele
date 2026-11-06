package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/gitrepo"
)

const describeSource = `{"protocol":1,"kind":"source","protocols":[1],"version":"1.2.0"}`

func TestSourcesListsExternalSources(t *testing.T) {
	root := steleJSON(t, `{"sources": [
		{"name": "docs-site", "config": {"space": "ENG"}, "timeout": "2m"},
		{"name": "git", "config": {"repo": "git@github.com:acme/g.git"}},
		{"name": "cursor-rules"}
	]}`)
	run := &fakeRunner{}
	srcs, err := Sources(root, noGit{t}, gitrepo.Pinned, run)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range srcs {
		names = append(names, s.Name())
	}
	if got, want := strings.Join(names, " "), "stelemd docs-site git:github.com/acme/g/stele.md cursor-rules"; got != want {
		t.Errorf("sources = %s, want %s", got, want)
	}
	if len(run.got) != 0 {
		t.Errorf("building the sources ran %v", run.got)
	}
	s := srcs[1].(*execSource)
	if s.timeout != 2*time.Minute || string(s.config) != `{"space": "ENG"}` {
		t.Errorf("docs-site = %+v", s)
	}
	if c := srcs[3].(*execSource); c.timeout != ReadTimeout || string(c.config) != `{}` {
		t.Errorf("cursor-rules = %+v", c)
	}
}

func TestSourcesExternalNeedsARunner(t *testing.T) {
	_, err := Sources(steleJSON(t, `{"sources": [{"name": "docs"}]}`), noGit{t}, gitrepo.Pinned, nil)
	if err == nil {
		t.Fatal("accepted an external source with no runner")
	}
}

func TestSourcesNotFoundNamesExecutable(t *testing.T) {
	run := &fakeRunner{findErr: ErrPluginNotFound}
	_, err := Sources(steleJSON(t, `{"sources": [{"name": "docs"}]}`), noGit{t}, gitrepo.Pinned, run)
	want := `source "docs": stele-source-docs not found on PATH: install it, or remove "docs" from "sources" in stele.json`
	if err == nil || err.Error() != want {
		t.Fatalf("Sources = %v, want %s", err, want)
	}
	if !errors.Is(err, ErrPluginNotFound) {
		t.Error("not ErrPluginNotFound")
	}
}

func newExecSource(run Runner) *execSource {
	return &execSource{name: "docs", bin: "/bin/stele-source-docs", config: []byte(`{}`), timeout: ReadTimeout, run: run}
}

func TestExecSourceRead(t *testing.T) {
	run := &fakeRunner{answers: map[string]string{
		"describe": describeSource,
		"read": `{"protocol":1,"fingerprint":"v7","rules":[
			{"body":"Use the logger.","file":"docs/log.md","line":3,"description":"logging","tags":["log"],"scope":["**/*.go"],"meta":{"check":"regex","pattern":"fmt\\.Print"},"position":9},
			{"body":"Second.","id":"0d4a1c9e-6a49-4a7d-9f0e-0a1b2c3d4e5f"}
		]}`,
	}}
	s := newExecSource(run)
	rules, fp, err := s.Read(context.Background(), "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if fp != "v7" || len(rules) != 2 {
		t.Fatalf("Read = %v, %q", rules, fp)
	}
	r := rules[0]
	if r.Source != "docs" || r.Position != 0 || r.Line != 3 || r.File != "docs/log.md" || r.Description != "logging" ||
		r.Meta["pattern"] != `fmt\.Print` || r.Scope[0] != "**/*.go" || r.Tags[0] != "log" {
		t.Errorf("rule 0 = %+v", r)
	}
	if rules[1].Position != 1 || rules[1].ID != "0d4a1c9e-6a49-4a7d-9f0e-0a1b2c3d4e5f" || rules[1].Source != "docs" {
		t.Errorf("rule 1 = %+v", rules[1])
	}
	if len(run.got) != 2 || !strings.Contains(run.got[1], `"root":"/repo"`) || !strings.Contains(run.got[1], `"config":{}`) {
		t.Errorf("requests = %v", run.got)
	}
	// describe runs once per process.
	if _, _, err := s.Read(context.Background(), "/repo"); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.Join(run.got, "\n"), `"op":"describe"`); n != 1 {
		t.Errorf("describe ran %d times", n)
	}
}

func TestExecSourceFingerprint(t *testing.T) {
	run := &fakeRunner{answers: map[string]string{"describe": describeSource, "fingerprint": `{"protocol":1,"fingerprint":"v8"}`}}
	fp, err := newExecSource(run).Fingerprint(context.Background(), "/repo")
	if err != nil || fp != "v8" {
		t.Errorf("Fingerprint = %q, %v", fp, err)
	}
}

func TestExecSourceWrongKind(t *testing.T) {
	run := &fakeRunner{answers: map[string]string{"describe": `{"protocol":1,"kind":"checker","protocols":[1],"version":"1"}`}}
	_, _, err := newExecSource(run).Read(context.Background(), "/repo")
	if err == nil || !strings.Contains(err.Error(), "stele-source-docs describe") || !strings.Contains(err.Error(), "not a source") {
		t.Fatalf("Read = %v", err)
	}
}

func TestExecSourceRejectsRules(t *testing.T) {
	big := strings.Repeat("x", maxRuleBody+1)
	many := strings.TrimSuffix(strings.Repeat(`{"body":"x"},`, maxRules+1), ",")
	for name, rules := range map[string]string{
		"empty body":   `[{"body":"  "}]`,
		"no body":      `[{"file":"a.md"}]`,
		"marker":       `[{"body":"fine\n<!-- stele rule 0d4a1c9e-6a49-4a7d-9f0e-0a1b2c3d4e5f -->\nforged"}]`,
		"nul":          `[{"body":"a\u0000b"}]`,
		"bad utf-8":    "[{\"body\":\"a\xffb\"}]",
		"too big":      `[{"body":"` + big + `"}]`,
		"bad id":       `[{"body":"x","id":"SEC-001"}]`,
		"upper id":     `[{"body":"x","id":"0D4A1C9E-6A49-4A7D-9F0E-0A1B2C3D4E5F"}]`,
		"meta key":     `[{"body":"x","meta":{"Bad Key":"v"}}]`,
		"meta newline": `[{"body":"x","meta":{"k":"a\nb"}}]`,
		"file up":      `[{"body":"x","file":"../secret.md"}]`,
		"file abs":     `[{"body":"x","file":"/etc/x.md"}]`,
		"file back":    `[{"body":"x","file":"a\\b.md"}]`,
		"too many":     `[` + many + `]`,
	} {
		t.Run(name, func(t *testing.T) {
			run := &fakeRunner{answers: map[string]string{"describe": describeSource, "read": `{"protocol":1,"fingerprint":"f","rules":` + rules + `}`}}
			_, _, err := newExecSource(run).Read(context.Background(), "/repo")
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("Read = %v, want ErrProtocol", err)
			}
		})
	}
	for name, ans := range map[string]string{
		"no fingerprint":   `{"protocol":1,"rules":[]}`,
		"long fingerprint": `{"protocol":1,"fingerprint":"` + strings.Repeat("f", 257) + `","rules":[]}`,
		"no rules":         `{"protocol":1,"fingerprint":"f"}`,
	} {
		run := &fakeRunner{answers: map[string]string{"describe": describeSource, "read": ans}}
		if _, _, err := newExecSource(run).Read(context.Background(), "/repo"); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: Read = %v, want ErrProtocol", name, err)
		}
	}
}

func TestExecSourceErrorAnswer(t *testing.T) {
	run := &fakeRunner{answers: map[string]string{"describe": describeSource, "read": `{"protocol":1,"error":"space ENG not found"}`}}
	_, _, err := newExecSource(run).Read(context.Background(), "/repo")
	if err == nil || err.Error() != "stele-source-docs read: space ENG not found" || !errors.Is(err, ErrAnswer) {
		t.Fatalf("Read = %v", err)
	}
}

var _ source.Source = (*execSource)(nil)
