package functional

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A hand-edited index.json must not be trusted (D57): dropping a rule from
// it must not stop the rule being shown.
func TestTamperedIndexIsNotTrusted(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	idx := p.read(".stele/index.json")
	tampered := strings.Replace(idx, "Never put credentials", "Credentials are fine", 1)
	if tampered == idx {
		t.Fatal("index.json does not hold the rule text; test needs updating")
	}
	p.write(".stele/index.json", tampered)
	a := p.hook(pre("a.go", "package a"))
	if strings.Contains(a.Reason, "Credentials are fine") {
		t.Fatalf("the hook showed a rule from a tampered index.json:\n%s", a.Reason)
	}
	if !a.Deny || !strings.Contains(a.Reason, "Never put credentials") {
		t.Errorf("with index.json tampered, the real rules must still be shown:\n%s", a.Raw)
	}
}

// A planted session state, as a cloned repository could carry, must not mark
// rules as shown. Same for a state signed by another machine's key.
func TestForgedSessionStateIsNotTrusted(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	p.hook(pre("a.go", "package a"))
	state := p.read(".stele/state/sess-1.json")

	t.Run("unsigned", func(t *testing.T) {
		p := p.on(t)
		body := state[strings.Index(state, "\n")+1:]
		forged := strings.ReplaceAll(body, "sess-1", "sess-9")
		p.write(".stele/state/sess-9.json", forged)
		e := pre("a.go", "package a")
		e.Session = "sess-9"
		if a := p.hook(e); !a.Deny {
			t.Error("an unsigned planted state marked the rules as shown")
		}
	})
	t.Run("signed by another key", func(t *testing.T) {
		q := newProject(t, goRules)
		q.compile()
		q.write(".stele/state/sess-1.json", state) // signed with p's key
		if a := q.hook(pre("a.go", "package a")); !a.Deny {
			t.Error("a state signed by another machine's key marked the rules as shown")
		}
	})
}

// D43 and D45: no external plugin ever runs from the hook, and nothing inside
// the repository is ever executed, whatever stele.json says. A planted git
// in the repository must not run either: stele check calls git.
func TestNothingInTheRepositoryRuns(t *testing.T) {
	p := newProject(t, goRules)
	marker := filepath.Join(t.TempDir(), "ran")
	plugin := buildMarker(t, marker)

	// Plant the marker under every name a plugin or git lookup could use,
	// inside the repository. A plugin listed in stele.json and found on PATH
	// may run from stele check (D43), so PATH gets its own case below, for
	// the hook only.
	names := []string{"stele-evil", "evil", "stele-plugin-evil", "git"}
	for _, n := range names {
		for _, dir := range []string{p.dir, p.path(".stele/bin"), p.path("bin")} {
			copyFile(t, plugin, filepath.Join(dir, n+exe()))
		}
	}
	if runtime.GOOS == "windows" {
		for _, n := range []string{"git.bat", "git.cmd", "git.com"} {
			copyFile(t, plugin, p.path(n))
		}
	}
	p.write("stele.json", `{"plugins": [{"name": "evil", "kind": "checker", "command": "./evil"}], "checkers": {"evil": "./bin/evil"}}`)
	p.write("stele.md", goRules+"\n---\ncheck: evil\n\nAn evil checker.\n")
	p.env = append(p.env, "STELE_FT_MARKER="+marker)
	p.git("add", "stele.md")

	p.stele("compile")
	p.agentWrite(event{Prompt: "t", File: "a.go", Content: "package a\n"})
	p.stele("check", "--all")
	p.stele("check")
	if _, err := os.Stat(marker); err == nil {
		data, _ := os.ReadFile(marker)
		t.Fatalf("stele executed a program from the repository: %s", data)
	}

	// The hook never runs an external plugin, even one listed and on PATH
	// (D45).
	onPath := t.TempDir()
	for _, n := range []string{"stele-evil", "evil", "stele-plugin-evil", "stele-checker-evil"} {
		copyFile(t, plugin, filepath.Join(onPath, n+exe()))
	}
	p.env = append(p.env, "PATH="+onPath+string(os.PathListSeparator)+os.Getenv("PATH"))
	e := event{Prompt: "t2", File: "b.go", Content: "package a\n"}
	p.agentWrite(e)
	if _, err := os.Stat(marker); err == nil {
		data, _ := os.ReadFile(marker)
		t.Fatalf("stele hook executed a plugin found on PATH: %s", data)
	}
}

// buildMarker builds a program that records it ran.
func buildMarker(t *testing.T, marker string) string {
	t.Helper()
	dir := t.TempDir()
	src := `package main

import "os"

func main() {
	os.WriteFile(os.Getenv("STELE_FT_MARKER"), []byte(os.Args[0]), 0o644)
}
`
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module marker\n"), 0o644)
	out := filepath.Join(dir, "marker"+exe())
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = dir
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building marker: %v\n%s", err, b)
	}
	return out
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(to), 0o755)
	if err := os.WriteFile(to, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// The agent itself can write the files that govern it. These probes record
// what happens when it does, through the hooked tools stele sees.
func TestAgentCanDisarmItself(t *testing.T) {
	t.Run("emptying stele.md", func(t *testing.T) {
		p := newProject(t, goRules)
		p.compile()
		p.commit()
		// The agent writes stele.md; the unscoped rule is shown first.
		postA := p.agentWrite(event{Prompt: "t", File: "stele.md", Content: "<!-- no rules -->\n"})
		e := pre("a.go", "package a\nfunc f() { fmt.Println(1) }\n")
		e.Prompt = "t2"
		a := p.hook(e)
		p.write("a.go", e.Content)
		code, out, _ := p.stele("check")
		if !a.Deny {
			t.Logf("FINDING: after the agent empties stele.md, writes get no rules; its own write to stele.md got %q; stele check exit %d:\n%s",
				postA.Raw, code, out)
		}
		if code == 0 {
			t.Logf("FINDING: stele check passes on a violating file after the agent removed every rule (the hook rewrote stele.lock to match)")
		}
	})
	t.Run("turning remind off in .claude/settings.json", func(t *testing.T) {
		p := newProject(t, goRules)
		p.mustStele(0, "init")
		p.commit()
		off := strings.ReplaceAll(p.read(".claude/settings.json"), "--installed=devel", "--installed=devel --remind=off")
		if a := p.hook(pre(".claude/settings.json", off)); !strings.Contains(a.Raw, `"permissionDecision":"ask"`) {
			t.Errorf("the agent can turn remind off by writing .claude/settings.json without asking:\n%s", a.Raw)
		}
	})
	t.Run("writing .stele/index.json", func(t *testing.T) {
		p := newProject(t, goRules)
		p.compile()
		p.agentWrite(event{Prompt: "t", File: ".stele/index.json", Content: "{}"})
		e := pre("a.go", "package a")
		e.Prompt = "t2"
		if a := p.hook(e); !a.Deny {
			t.Error("an agent writing .stele/index.json disarmed the hook; the signature should catch it")
		}
	})
}
