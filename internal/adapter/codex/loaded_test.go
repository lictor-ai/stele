package codex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lictor-ai/stele/internal/adapter"
)

// repo is a git checkout holding root, a stele project with AGENTS.md
// holding stele's section; it returns root.
func repo(t *testing.T, sub string) string {
	t.Helper()
	t.Setenv("CODEX_HOME", t.TempDir())
	top := t.TempDir()
	if err := os.Mkdir(filepath.Join(top, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(top, filepath.FromSlash(sub))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, RulesFile), RenderRules(testRules), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// startEv is a session start whose id, a UUIDv7 as Codex's are, says it
// began now.
func startEv(source, cwd string) adapter.Event {
	return adapter.Event{Kind: adapter.SessionStart, SessionID: uuid.Must(uuid.NewV7()).String(), Cwd: cwd, Native: start{Source: source}}
}

// AGENTS.md changed after the session started is not what Codex read: Codex
// reads it when the session starts, the hook runs at the first turn.
func TestLoadedChangedSinceStart(t *testing.T) {
	root := repo(t, "")
	ev := startEv("startup", root)
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(root, RulesFile), RenderRules(testRules[:1]), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := Loaded(ev, root); l.Read || !l.Launch {
		t.Errorf("AGENTS.md changed after the start: %+v", l)
	}
	ev.SessionID = "not-a-uuid"
	if l := Loaded(ev, root); l.Read {
		t.Errorf("a session id with no time: %+v", l)
	}
	if got, ok := sessionStarted("01a1a883-dc24-7932-8abd-64e03740d9f7"); !ok || got.UTC().Format("2006-01-02") != "2026-11-04" {
		t.Errorf("recorded id's time = %v, %v", got, ok)
	}
}

// A .git between root and where Codex runs, a submodule, is where Codex's
// walk starts: root's AGENTS.md is not read.
func TestLoadedNestedGit(t *testing.T) {
	root := repo(t, "")
	sub := filepath.Join(root, "vendor", "lib")
	if err := os.MkdirAll(filepath.Join(sub, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if l := Loaded(startEv("startup", filepath.Join(sub, "x")), root); l.Read {
		t.Errorf("below a nested .git: %+v", l)
	}
}

func TestLoadedOpenCommentBefore(t *testing.T) {
	root := repo(t, "")
	data := append([]byte("<!-- a note never closed\n\n"), RenderRules(testRules)...)
	if err := os.WriteFile(filepath.Join(root, RulesFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if l := Loaded(startEv("startup", root), root); l.Read {
		t.Errorf("an open comment before the section: %+v", l)
	}
}

func TestLoadedStartupOnly(t *testing.T) {
	root := repo(t, "")
	if l := Loaded(startEv("startup", root), root); !l.Read || !l.Launch || l.Forget {
		t.Errorf("startup in root: %+v", l)
	}
	sub := filepath.Join(root, "pkg")
	if l := Loaded(startEv("startup", sub), root); !l.Read {
		t.Errorf("startup below root: %+v", l)
	}
	for _, src := range []string{"resume", "compact", "clear", "fork"} {
		if l := Loaded(startEv(src, root), root); l.Read || !l.Forget {
			t.Errorf("%s: %+v, want forget", src, l)
		}
	}
	ev := startEv("startup", root)
	ev.AgentID = "sub"
	if l := Loaded(ev, root); l != (adapter.Load{}) {
		t.Errorf("a subagent: %+v", l)
	}
	if l := Loaded(startEv("startup", filepath.Dir(root)), root); l.Read {
		t.Errorf("started above root: %+v", l)
	}
}

func TestLoadedOverride(t *testing.T) {
	root := repo(t, "")
	if err := os.WriteFile(filepath.Join(root, overrideFile), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := Loaded(startEv("startup", root), root); l.Read {
		t.Errorf("with an override: %+v", l)
	}
}

func TestLoadedNoGit(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	root := t.TempDir()
	if _, ok := gitTop(root); ok {
		t.Skip("the temporary directory is inside a git checkout")
	}
	if err := os.WriteFile(filepath.Join(root, RulesFile), RenderRules(testRules), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := Loaded(startEv("startup", root), root); l.Read {
		t.Errorf("no .git: %+v", l)
	}
}

// What Codex reads before root's file counts against its cap.
func TestLoadedOverCap(t *testing.T) {
	root := repo(t, "a/b")
	top := filepath.Dir(filepath.Dir(root))
	if err := os.WriteFile(filepath.Join(top, RulesFile), []byte(strings.Repeat("x", maxProjectDoc-100)), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := Loaded(startEv("startup", root), root); l.Read {
		t.Errorf("past the cap: %+v", l)
	}
	if err := os.WriteFile(filepath.Join(top, RulesFile), []byte("short"), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := Loaded(startEv("startup", root), root); !l.Read {
		t.Errorf("within the cap: %+v", l)
	}
}

func TestLoadedConfigCap(t *testing.T) {
	root := repo(t, "")
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, filepath.FromSlash(configFile))
	if err := os.WriteFile(cfg, []byte("project_doc_max_bytes = 10 # tiny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := Loaded(startEv("startup", root), root); l.Read {
		t.Errorf("a 10-byte cap: %+v", l)
	}
	if err := os.WriteFile(cfg, []byte("project_root_markers = [\".hg\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := Loaded(startEv("startup", filepath.Join(root, "x")), root); l.Read {
		t.Errorf("root markers moved, below root: %+v", l)
	}
	if l := Loaded(startEv("startup", root), root); !l.Read {
		t.Errorf("root markers moved, in root: %+v", l)
	}
}

func TestLoadedNoSection(t *testing.T) {
	root := repo(t, "")
	if err := os.WriteFile(filepath.Join(root, RulesFile), []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := Loaded(startEv("startup", root), root); l.Read {
		t.Errorf("no section: %+v", l)
	}
}

func TestVersionNotes(t *testing.T) {
	for _, tt := range []struct {
		out, goos string
		err       error
		want      string
	}{
		{"codex-cli 0.166.0\n", "windows", nil, ""},
		{"codex-cli 0.141.2\n", "linux", nil, ""},
		{"codex-cli 0.141.2\n", "windows", nil, "0.145 or later on Windows"},
		{"codex-cli 0.130.0\n", "linux", nil, "may not hold"},
		{"codex-cli 0.100.0\n", "linux", nil, "runs no hooks"},
		{"", "linux", errors.New("not found"), "was not found"},
		{"weird", "linux", nil, "did not say its version"},
	} {
		got := strings.Join(VersionNotes([]byte(tt.out), tt.err, tt.goos), "\n")
		if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
			t.Errorf("VersionNotes(%q, %v, %s) = %q, want %q", tt.out, tt.err, tt.goos, got, tt.want)
		}
	}
}
