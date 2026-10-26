package functional

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The basic exchange: the first write of a turn is denied with the rules,
// the retry goes through, and a new turn shows them again.
func TestPreShowsRulesOncePerTurn(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()

	a := p.hook(pre("cmd/main.go", "package main"))
	if !a.Deny {
		t.Fatalf("first write of the turn was not denied:\n%s", a.Raw)
	}
	for _, want := range []string{"Never print with fmt.Print", "Never put credentials"} {
		if !strings.Contains(a.Reason, want) {
			t.Errorf("deny reason lacks rule %q:\n%s", want, a.Reason)
		}
	}
	if strings.Contains(a.Reason, "migration") {
		t.Errorf("deny reason shows the migrations/ rule for a .go file outside migrations/:\n%s", a.Reason)
	}

	time.Sleep(3100 * time.Millisecond) // past the batch window
	if b := p.hook(pre("cmd/main.go", "package main")); b.Deny {
		t.Fatalf("retry after reading the rules was denied again:\n%s", b.Reason)
	}
	// Another Go file this turn: nothing new to show.
	if b := p.hook(pre("internal/x/x.go", "package x")); b.Deny {
		t.Errorf("rules already shown this turn were shown again:\n%s", b.Reason)
	}
	// A migration brings one new rule, and only that one.
	b := p.hook(pre("migrations/002.sql", "alter table"))
	if !b.Deny || !strings.Contains(b.Reason, "merged migration") || strings.Contains(b.Reason, "fmt.Print") {
		t.Errorf("write under migrations/ should show only the migration rule:\n%s", b.Raw)
	}

	// New turn.
	e := pre("cmd/main.go", "package main")
	e.Prompt = "turn-2"
	if c := p.hook(e); !c.Deny {
		t.Error("a new prompt_id did not show the rules again")
	}
}

// A file with no rules is never denied: README.md only gets unscoped rules,
// and a project with only scoped rules leaves it alone entirely.
func TestPreNoRulesNoDeny(t *testing.T) {
	p := newProject(t, "scope: [\"**/*.go\"]\n\nGo rule.\n")
	p.compile()
	if a := p.hook(pre("README.md", "# hi")); !a.silent() {
		t.Errorf("write with no applicable rule got an answer:\n%s", a.Raw)
	}
}

// Subagents have their own context: rules shown to the main agent do not
// count for them, nor the other way round, nor between two subagents.
func TestPrePerAgent(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	for _, agent := range []string{"", "agent-a", "agent-b"} {
		e := pre("a.go", "package a")
		e.Agent = agent
		if a := p.hook(e); !a.Deny {
			t.Errorf("agent %q was not shown the rules", agent)
		}
	}
	time.Sleep(3100 * time.Millisecond)
	for _, agent := range []string{"", "agent-a", "agent-b"} {
		e := pre("a.go", "package a")
		e.Agent = agent
		if a := p.hook(e); a.Deny {
			t.Errorf("agent %q was shown the rules twice in one turn", agent)
		}
	}
}

// Two sessions on one repository never see each other's state.
func TestPrePerSession(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	p.hook(pre("a.go", "package a"))
	e := pre("a.go", "package a")
	e.Session = "sess-2"
	if a := p.hook(e); !a.Deny {
		t.Error("a second session inherited the first session's shown rules")
	}
}

// The agent sends a batch of writes in one message, and Claude Code runs
// their hooks at once. Every write in the batch must be denied (the agent
// has read nothing yet), the session state must survive the race, and the
// retry of the whole batch must go through.
func TestPreParallelBatch(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	const n = 16
	var wg sync.WaitGroup
	answers := make([]answer, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i] = p.hook(pre(fmt.Sprintf("pkg%d/f.go", i), "package x"))
		}()
	}
	wg.Wait()
	denied := 0
	for _, a := range answers {
		if a.Deny {
			denied++
		}
		if a.Warning != "" {
			t.Errorf("parallel hook warned (lock contention?): %s", a.Warning)
		}
	}
	if denied != n {
		t.Errorf("%d of %d parallel writes denied; the whole batch must be, or some go through unread", denied, n)
	}

	time.Sleep(3100 * time.Millisecond)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i] = p.hook(pre(fmt.Sprintf("pkg%d/f.go", i), "package x"))
		}()
	}
	wg.Wait()
	for i, a := range answers {
		if a.Deny {
			t.Errorf("retry %d of the batch was denied again", i)
		}
	}
}

// A fast agent can retry inside the batch window. Window is 3 s, "shorter
// than any model round trip". A retry 1.5 s after the deny is denied again;
// this test records how many extra round trips a fast retry costs, and fails
// if the agent can be held in a loop past the window.
func TestPreFastRetryCostsAtMostOneExtraDeny(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	p.hook(pre("a.go", "package a"))
	denies := 1
	start := time.Now()
	for time.Since(start) < 10*time.Second {
		time.Sleep(1500 * time.Millisecond)
		if a := p.hook(pre("a.go", "package a")); !a.Deny {
			break
		}
		denies++
	}
	measure("fast_retry_1500ms_denies", denies)
	if denies > 2 {
		t.Errorf("a retry every 1.5 s was denied %d times; the window must not hold an agent in a loop", denies)
	}
	if denies == 2 {
		t.Logf("FINDING: a retry 1.5 s after the deny is denied again (window 3 s): one wasted round trip for a fast model")
	}
}

// The root is found from the file, not from cwd: a subproject with its own
// stele.md gets only its own rules, and a nested git repository (worktree,
// submodule) gets none from the outer stele.md.
func TestPreSubprojectsAndNestedGit(t *testing.T) {
	p := newProject(t, "Outer rule, everywhere.\n")
	p.compile()
	p.write("services/api/stele.md", "Inner rule for the api.\n")
	p.write("vendor/lib/.git", "gitdir: ../../.git/modules/lib\n")

	a := p.hook(pre("services/api/h.go", "package api"))
	if !a.Deny || !strings.Contains(a.Reason, "Inner rule") || strings.Contains(a.Reason, "Outer rule") {
		t.Errorf("file in a subproject should get its rules only:\n%s", a.Raw)
	}
	if !p.exists("services/api/.stele/index.json") {
		t.Error("the subproject's index was not built by the hook (EnsureFresh)")
	}
	if a := p.hook(pre("vendor/lib/x.go", "package lib")); a.Deny {
		t.Errorf("file in a nested git checkout got the outer project's rules:\n%s", a.Reason)
	}
	// cwd in the subproject, file in the outer project.
	e := pre("main.go", "package main")
	e.Cwd = p.path("services/api")
	if a := p.hook(e); !a.Deny || !strings.Contains(a.Reason, "Outer rule") {
		t.Errorf("root must come from the file, not cwd:\n%s", a.Raw)
	}
}

// A file outside any stele project gets nothing, and the hook must not
// create .stele/ or stele.lock in a repository that does not use stele, even
// though the hook is registered for every write.
func TestPreOutsideProjectTouchesNothing(t *testing.T) {
	p := newProject(t, "")
	if a := p.hook(pre("main.go", "package main")); !a.silent() {
		t.Errorf("repository without stele got an answer:\n%s", a.Raw)
	}
	if p.exists(".stele") || p.exists("stele.lock") {
		t.Error("hook created stele files in a repository that does not use stele")
	}
	other := t.TempDir()
	if a := p.hook(pre(filepath.Join(other, "x.go"), "package x")); !a.silent() {
		t.Errorf("file outside every project got an answer:\n%s", a.Raw)
	}
}

// Path tricks must not dodge a scope: .., ./, mixed separators, and on
// Windows and macOS, letter case.
func TestPreScopeCannotBeDodgedByPathSpelling(t *testing.T) {
	p := newProject(t, "scope: [\"secret/**\"]\n\nSecret area rule.\n")
	p.compile()
	spellings := []string{
		p.path("other/../secret/a.txt"),
		p.dir + "/./secret/b.txt",
		strings.ReplaceAll(p.path("secret/c.txt"), string(filepath.Separator), "/"),
		"rel:secret/d.txt", // relative to cwd
	}
	if runtime.GOOS != "linux" {
		spellings = append(spellings, p.path("SECRET/e.txt"), p.path("Secret/f.txt"))
	}
	for i, f := range spellings {
		e := pre(f, "x")
		e.Prompt = fmt.Sprintf("turn-%d", i)
		if a := p.hook(e); !a.Deny {
			t.Errorf("path %q dodged scope secret/**", f)
		}
	}
	// And a near miss must not match.
	if a := p.hook(pre("secretive/a.txt", "x")); a.Deny {
		t.Error("secret/** matched secretive/a.txt")
	}
}

// Scope globs from the README, against the files they should and should
// not match.
func TestPreScopeGlobs(t *testing.T) {
	cases := []struct {
		scope   string
		match   []string
		nomatch []string
	}{
		{`["internal/**/*.go", "!**/*_test.go"]`, []string{"internal/a.go", "internal/x/y/b.go"}, []string{"internal/a_test.go", "cmd/a.go", "internal/a.gox"}},
		{`*.go`, []string{"a.go", "x/y/z.go"}, []string{"a.go.txt", "go"}},
		{`/*.go`, []string{"a.go"}, []string{"x/a.go"}},
		{`migrations/`, []string{"migrations/1.sql", "migrations/x/2.sql"}, []string{"migrations.sql", "db/migrations/1.sql"}},
		{`src/**/*.{go,ts}`, []string{"src/a.go", "src/x/b.ts"}, []string{"src/a.js", "lib/a.go"}},
		{`["!**/*.md"]`, []string{"a.go", "x/y.txt"}, []string{"README.md", "docs/a.md"}},
		{`\!bang.txt`, []string{"!bang.txt"}, []string{"bang.txt"}},
	}
	for _, c := range cases {
		t.Run(c.scope, func(t *testing.T) {
			p := newProject(t, "scope: "+c.scope+"\n\nThe rule.\n")
			p.compile()
			for i, f := range append(c.match, c.nomatch...) {
				e := pre(f, "x")
				e.Prompt = fmt.Sprintf("turn-%d", i)
				want := i < len(c.match)
				if got := p.hook(e).Deny; got != want {
					t.Errorf("scope %s on %s: shown=%v, want %v", c.scope, f, got, want)
				}
			}
		})
	}
}

// An edit to stele.md takes effect on the next write, with no stele compile.
func TestPreRecompilesWhenRulesChange(t *testing.T) {
	p := newProject(t, "First rule.\n")
	p.compile()
	p.hook(pre("a.txt", "x"))
	p.write("stele.md", "First rule.\n\n---\n\nSecond rule, added later.\n")
	e := pre("a.txt", "x")
	a := p.hook(e)
	if !a.Deny || !strings.Contains(a.Reason, "Second rule") {
		t.Fatalf("a rule added to stele.md mid-turn was not shown on the next write:\n%s", a.Raw)
	}
	if strings.Contains(a.Reason, "First rule") && !strings.Contains(a.Reason, "Second") {
		t.Error("the already-shown rule was shown again instead of the new one")
	}
}

// stele.md broken after a good compile: the last good rules still apply, and
// the agent is told where the error is.
func TestPreBrokenRulesFileKeepsLastGoodRules(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	p.write("stele.md", goRules+"\n---\nscope: [\"unclosed\n\nBroken rule.\n")
	a := p.hook(pre("a.go", "package a"))
	if !a.Deny || !strings.Contains(a.Reason, "Never print with fmt.Print") {
		t.Fatalf("with stele.md broken, the last good rules must still be shown:\n%s", a.Raw)
	}
	if !strings.Contains(a.Reason, "stele.md") {
		t.Errorf("the agent was not told stele.md is broken:\n%s", a.Reason)
	}
}

// stele.md broken before it ever parsed: block_on_error decides.
func TestPreBrokenBeforeFirstParse(t *testing.T) {
	broken := "scope: [\"unclosed\n\nBroken rule.\n"
	t.Run("default lets the write through with a warning", func(t *testing.T) {
		p := newProject(t, broken)
		a := p.hook(pre("a.go", "package a"))
		if a.Deny || a.Warning == "" {
			t.Errorf("want write through + warning, got:\n%s", a.Raw)
		}
	})
	t.Run("block_on_error blocks", func(t *testing.T) {
		p := newProject(t, broken)
		p.write("stele.json", `{"block_on_error": true}`)
		if a := p.hook(pre("a.go", "package a")); !a.Deny {
			t.Errorf("block_on_error on, but the write went through:\n%s", a.Raw)
		}
	})
	t.Run("remind off never blocks", func(t *testing.T) {
		p := newProject(t, broken)
		p.write("stele.json", `{"block_on_error": true}`)
		p.hookArgs = []string{"--installed=devel", "--remind=off"}
		if a := p.hook(pre("a.go", "package a")); a.Deny {
			t.Errorf("remind off must never block:\n%s", a.Raw)
		}
	})
}

// init --remind=off: no rules before writes at all, but a write to stele's
// own files still asks.
func TestPreRemindOff(t *testing.T) {
	p := newProject(t, goRules)
	p.mustStele(0, "init", "--remind=off")
	p.useInstalledHook()
	p.compile()
	if a := p.hook(pre("a.go", "package a")); a.Deny {
		t.Errorf("remind off denied a write:\n%s", a.Raw)
	}
	if a := p.hook(pre("stele.md", goRules+"\n---\n\nAnother rule.\n")); !strings.Contains(a.Raw, `"permissionDecision":"ask"`) {
		t.Errorf("remind off: a write to stele.md did not ask:\n%s", a.Raw)
	}
}

// The hook must survive any payload: it exits 0 and prints nothing or one
// JSON object, so Claude Code never sees a crash as a block.
func TestHookSurvivesGarbage(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	big := `{"hook_event_name":"PreToolUse","tool_name":"Write","session_id":"s","tool_input":{"file_path":"` +
		filepath.ToSlash(p.path("a.go")) + `","content":"` + strings.Repeat("A", 70<<20) + `"}}`
	payloads := map[string]string{
		"empty":          "",
		"not json":       "hello",
		"bom":            "\xef\xbb\xbf{}",
		"array":          "[1,2]",
		"no event":       `{"tool_name":"Write"}`,
		"null input":     `{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":null}`,
		"wrong types":    `{"hook_event_name":"PreToolUse","tool_name":5,"tool_input":{"file_path":7}}`,
		"unknown event":  `{"hook_event_name":"Stop","session_id":"s"}`,
		"bash tool":      `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}`,
		"over 64 MiB":    big,
		"bad session":    `{"hook_event_name":"PreToolUse","tool_name":"Write","session_id":"../../etc","tool_input":{"file_path":"` + filepath.ToSlash(p.path("a.go")) + `"}}`,
		"device session": `{"hook_event_name":"PreToolUse","tool_name":"Write","session_id":"CON","tool_input":{"file_path":"` + filepath.ToSlash(p.path("a.go")) + `"}}`,
		"nul in path":    `{"hook_event_name":"PreToolUse","tool_name":"Write","session_id":"s","tool_input":{"file_path":"` + filepath.ToSlash(p.path("a\u0000.go")) + `"}}`,
	}
	for name, pl := range payloads {
		t.Run(name, func(t *testing.T) {
			p.hookRaw([]byte(pl)) // fails the test on a non-zero exit or bad JSON
		})
	}
	// Nothing escaped the state directory.
	if _, err := os.Stat(filepath.Join(p.dir, "..", "etc.json")); err == nil {
		t.Error("a session id with .. wrote outside .stele/state")
	}
}

// A session id that cannot name a file cannot be recorded. The write must not
// be denied forever (save before deny); with the default policy it goes
// through with a warning. This is a way to write with no rules shown, so the
// test records it.
func TestPreUnrecordableSessionIsNotALoop(t *testing.T) {
	p := newProject(t, goRules)
	p.compile()
	for _, id := range []string{"", "../x", "CON"} {
		e := pre("a.go", "package a")
		e.Session = id
		if id == "" {
			e.Session = "\x00" // payload() fills an empty one
		}
		a := p.hook(e)
		a2 := p.hook(e)
		if a.Deny && a2.Deny {
			t.Errorf("session %q: denied twice in a row with nothing recorded: a loop", id)
		}
		if !a.Deny && a.Warning == "" {
			t.Errorf("session %q: write went through with no rules and no warning", id)
		}
	}
}
