package plugin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// fields decodes a request stele sent.
func fields(t *testing.T, req string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(req), &m); err != nil {
		t.Fatalf("request %s: %v", req, err)
	}
	return m
}

// wantProjectDir checks req carries project_dir, equal to root, and, if
// withRoot, root with the same value; without it, no root (ADR-0016).
func wantProjectDir(t *testing.T, req, root string, withRoot bool) {
	t.Helper()
	m := fields(t, req)
	if got, _ := m["project_dir"].(string); filepath.ToSlash(got) != filepath.ToSlash(root) {
		t.Errorf("project_dir = %v, want %s: %s", m["project_dir"], root, req)
	}
	r, ok := m["root"]
	switch {
	case withRoot && (!ok || filepath.ToSlash(r.(string)) != filepath.ToSlash(root)):
		t.Errorf("root = %v, want %s: %s", r, root, req)
	case !withRoot && ok:
		t.Errorf("root sent: %s", req)
	}
}

func TestSourceRequestsCarryProjectDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	run := &fakeRunner{answers: map[string]string{
		"describe":    describeSource,
		"read":        `{"protocol":1,"fingerprint":"v1","rules":[]}`,
		"fingerprint": `{"protocol":1,"fingerprint":"v1"}`,
	}}
	s := newExecSource(run)
	if _, _, err := s.Read(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Fingerprint(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if len(run.got) != 3 {
		t.Fatalf("requests = %v", run.got)
	}
	wantProjectDir(t, run.got[1], root, true)
	wantProjectDir(t, run.got[2], root, true)
	if _, ok := fields(t, run.got[0])["project_dir"]; ok {
		t.Errorf("describe carries project_dir: %s", run.got[0])
	}
}

func TestCheckerRequestsCarryProjectDir(t *testing.T) {
	root := steleJSON(t, `{"checkers": [{"name": "semgrep"}]}`)
	run := &fakeRunner{answers: map[string]string{
		"describe": describeChecker,
		"validate": `{"protocol":1,"problems":[]}`,
		"check":    `{"protocol":1,"violations":[]}`,
	}}
	cs, err := Checkers(root, run)
	if err != nil {
		t.Fatal(err)
	}
	c := cs["semgrep"]
	if _, err := c.Validate(context.Background(), checkRules); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Check(context.Background(), root, checkRules, []string{"a.js"}); err != nil {
		t.Fatal(err)
	}
	if len(run.got) != 3 {
		t.Fatalf("requests = %v", run.got)
	}
	wantProjectDir(t, run.got[1], root, false)
	wantProjectDir(t, run.got[2], root, true)
}

func TestSinkPushCarriesProjectDir(t *testing.T) {
	root := steleJSON(t, `{"sinks": [{"name": "a"}]}`)
	run := &fakeRunner{answers: map[string]string{
		"describe": `{"protocol":1,"kind":"sink","protocols":[1],"version":"1.0.0"}`,
		"push":     `{"protocol":1,"accepted":1}`,
	}}
	sinks, err := Sinks(root, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sinks[0].Push(context.Background(), "p1", records(1)); err != nil {
		t.Fatal(err)
	}
	wantProjectDir(t, run.got[len(run.got)-1], root, false)
}

// A plugin written before project_dir, as docs/plugin-protocol.md showed
// one, still reads every request: unknown fields are ignored.
func TestOldPluginIgnoresProjectDir(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	run := &fakeRunner{answers: map[string]string{
		"describe": describeSource,
		"read":     `{"protocol":1,"fingerprint":"v1","rules":[]}`,
	}}
	if _, _, err := newExecSource(run).Read(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	var old struct {
		Op     string          `json:"op"`
		Root   string          `json:"root"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal([]byte(run.got[1]), &old); err != nil {
		t.Fatal(err)
	}
	if old.Op != "read" || old.Root != root {
		t.Errorf("old plugin read %+v", old)
	}
}
