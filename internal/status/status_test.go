package status

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/index"
	"github.com/lictor-ai/stele/internal/plugin"
	"github.com/lictor-ai/stele/internal/sign"
	"github.com/lictor-ai/stele/internal/source"
)

// fakeAgent is an agent whose settings file holds "stele vX remind=off" when
// its hooks are installed, and whose rules file is owned when it starts with
// "stele".
type fakeAgent struct {
	adapter.Adapter
	name string
}

func (a fakeAgent) Name() string         { return a.name }
func (a fakeAgent) SettingsFile() string { return filepath.Join("."+a.name, "settings.json") }
func (a fakeAgent) RulesFile() string    { return a.name + "-rules.md" }
func (a fakeAgent) MaxRulesFile() int64  { return 1 << 20 }
func (a fakeAgent) OwnedRules(data []byte) bool {
	return strings.HasPrefix(string(data), "stele")
}
func (a fakeAgent) Installed(data []byte) (string, adapter.Switches, bool) {
	f := strings.Fields(string(data))
	if len(f) < 2 || f[0] != "stele" {
		return "", adapter.Switches{}, false
	}
	return f[1], adapter.Switches{RemindOff: strings.Contains(string(data), "remind=off")}, true
}

// fakeSource is a source with a fixed fingerprint.
type fakeSource struct {
	name, fp string
}

func (s fakeSource) Name() string                                        { return s.name }
func (s fakeSource) Fingerprint(context.Context, string) (string, error) { return s.fp, nil }
func (s fakeSource) Read(context.Context, string) ([]source.Rule, string, error) {
	return nil, s.fp, nil
}

// runner answers describe for any plugin, and counts calls.
type runner struct {
	calls   int
	missing map[string]bool
}

func (r *runner) Find(kind plugin.Kind, name string) (string, error) {
	if r.missing[name] {
		return "", plugin.ErrPluginNotFound
	}
	return "/bin/" + plugin.Exe(kind, name), nil
}

func (r *runner) Run(_ context.Context, bin string, req []byte, _ int, _ time.Duration) ([]byte, error) {
	r.calls++
	kind := strings.SplitN(filepath.Base(bin), "-", 3)[1]
	return []byte(`{"protocol":1,"kind":"` + kind + `","protocols":[1],"version":"2.1.0"}`), nil
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func key(t *testing.T) sign.Key {
	t.Helper()
	k, err := sign.LoadKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func saveIndex(t *testing.T, root string, k sign.Key, sources map[string]string, rules ...index.Entry) {
	t.Helper()
	if err := index.EnsureDir(root); err != nil {
		t.Fatal(err)
	}
	if err := index.Save(root, index.Index{Version: index.Version, Project: "p1", Sources: sources, Rules: rules}, k); err != nil {
		t.Fatal(err)
	}
}

func TestStatusJSONShape(t *testing.T) {
	root := t.TempDir()
	k := key(t)
	saveIndex(t, root, k, map[string]string{"stelemd": "fp1"}, index.Entry{ID: "r1", Source: "stelemd"})
	in := Input{
		Version: "v1.2.3",
		Items:   []plugin.Item{{Kind: plugin.KindSource, Name: "stelemd", Builtin: true, Source: fakeSource{"stelemd", "fp1"}}},
		Key:     k,
	}
	st, err := Collect(context.Background(), root, in)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["v"] != float64(1) || m["stele_version"] != "v1.2.3" {
		t.Errorf("header = %v", m)
	}
	for _, k := range []string{"agents", "plugins", "sources", "sinks", "notes"} {
		if _, ok := m[k].([]any); !ok {
			t.Errorf("%s is %v, want a list", k, m[k])
		}
	}
	if len(st.Sources) != 1 || st.Sources[0].Rules != 1 || st.Sources[0].Fresh == nil || !*st.Sources[0].Fresh {
		t.Errorf("sources = %+v", st.Sources)
	}
	if !st.Index.Present || !st.Index.Valid || st.Index.Fresh == nil || !*st.Index.Fresh {
		t.Errorf("index = %+v", st.Index)
	}
}

func TestStatusIndexStale(t *testing.T) {
	root := t.TempDir()
	k := key(t)
	saveIndex(t, root, k, map[string]string{"stelemd": "old"})
	st, err := Collect(context.Background(), root, Input{
		Items: []plugin.Item{{Kind: plugin.KindSource, Name: "stelemd", Builtin: true, Source: fakeSource{"stelemd", "new"}}},
		Key:   k,
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Sources[0].Fresh == nil || *st.Sources[0].Fresh || st.Index.Fresh == nil || *st.Index.Fresh {
		t.Errorf("a changed source is fresh: %+v %+v", st.Sources, st.Index)
	}
}

func TestStatusNoKeyNoIndex(t *testing.T) {
	st, err := Collect(context.Background(), t.TempDir(), Input{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Index.Present || st.Index.Valid || st.Index.Fresh != nil {
		t.Errorf("index = %+v", st.Index)
	}
}

func TestStatusPluginMissing(t *testing.T) {
	r := &runner{missing: map[string]bool{"lint": true}}
	st, err := Collect(context.Background(), t.TempDir(), Input{
		Run: r,
		Items: []plugin.Item{
			{Kind: plugin.KindChecker, Name: "regex", Builtin: true},
			{Kind: plugin.KindChecker, Name: "lint", Err: &plugin.Error{Kind: plugin.KindChecker, Name: "lint", Err: plugin.ErrPluginNotFound}},
			{Kind: plugin.KindChecker, Name: "fmt"},
		},
		Version: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Plugin{}
	for _, p := range st.Plugins {
		byName[p.Name] = p
	}
	if p := byName["regex"]; !p.Builtin || p.Version != "v1" || p.Found == nil || !*p.Found {
		t.Errorf("regex = %+v", p)
	}
	if p := byName["lint"]; p.Found == nil || *p.Found || p.Error == "" {
		t.Errorf("lint = %+v", p)
	}
	if p := byName["fmt"]; p.Found == nil || !*p.Found || p.Version != "2.1.0" || p.Error != "" {
		t.Errorf("fmt = %+v", p)
	}
}

func TestStatusOfflineStartsNothing(t *testing.T) {
	st, err := Collect(context.Background(), t.TempDir(), Input{
		Items: []plugin.Item{
			{Kind: plugin.KindChecker, Name: "fmt"},
			{Kind: plugin.KindSource, Name: "docs"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.Plugins {
		if p.Found != nil || p.Version != "" {
			t.Errorf("offline %s = %+v, want unknown", p.Name, p)
		}
	}
	if len(st.Sources) != 1 || st.Sources[0].Fresh != nil {
		t.Errorf("sources = %+v", st.Sources)
	}
}

func TestStatusAgentsInstallState(t *testing.T) {
	root := t.TempDir()
	write(t, root, filepath.Join(".a", "settings.json"), "stele v1.0.0 remind=off")
	write(t, root, "a-rules.md", "stele rules")
	write(t, root, "b-rules.md", "the user's own")
	st, err := Collect(context.Background(), root, Input{Agents: []adapter.Adapter{fakeAgent{name: "a"}, fakeAgent{name: "b"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Agents) != 2 {
		t.Fatalf("agents = %+v", st.Agents)
	}
	a, b := st.Agents[0], st.Agents[1]
	if !a.Hooks || a.Installed != "v1.0.0" || a.Remind || !a.Check || !a.RulesPresent {
		t.Errorf("a = %+v", a)
	}
	if b.Hooks || b.RulesPresent || filepath.ToSlash(b.SettingsFile) != ".b/settings.json" {
		t.Errorf("b = %+v", b)
	}
}
