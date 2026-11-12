package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Result is one run: what it cost and how it was graded.
type Result struct {
	Scenario string `json:"scenario"`
	Arm      string `json:"arm"`
	Rep      int    `json:"rep"`
	Model    string `json:"model"`
	Agent    string `json:"agent,omitempty"` // empty for Claude Code, from before -agent
	// AgentVersion and APIKeySource are what Claude Code's init event says:
	// its version, and where its credentials came from ("none" is a
	// claude.ai login, which bills the subscription, not the API).
	AgentVersion string `json:"agent_version,omitempty"`
	APIKeySource string `json:"api_key_source,omitempty"`
	// ResolvedModel is the model Claude Code's init event names: the one
	// the session ran on, whatever -model said.
	ResolvedModel string `json:"resolved_model,omitempty"`

	WallMS        int64   `json:"wall_ms"`
	DurationMS    int64   `json:"duration_ms"`
	DurationAPIMS int64   `json:"duration_api_ms"`
	NumTurns      int     `json:"num_turns"`
	InputTokens   int64   `json:"input_tokens"`
	CacheCreate   int64   `json:"cache_creation_input_tokens"`
	CacheRead     int64   `json:"cache_read_input_tokens"`
	OutputTokens  int64   `json:"output_tokens"`
	CostUSD       float64 `json:"total_cost_usd"`
	IsError       bool    `json:"is_error"`
	Error         string  `json:"error,omitempty"`

	// What stele did, from the transcript: writes denied to show rules,
	// writes sent back for a violation, and the text it added.
	HookFired    bool `json:"hook_fired"`
	Denies       int  `json:"denies"`
	BashDenies   int  `json:"bash_denies"` // of Denies, Bash commands stopped
	Blocks       int  `json:"blocks"`
	HookBytes    int  `json:"hook_bytes"`
	Writes       int  `json:"writes"`        // Write, Edit and MultiEdit calls
	BashWrites   int  `json:"bash_writes"`   // Bash calls that look like they write a file
	RulesTouched bool `json:"rules_touched"` // the agent changed stele.md, CLAUDE.md or .claude/

	Grade
	Dir string `json:"-"` // the run's temporary tree: local, never saved
	// Baseline names the results file a run was loaded from, when it was
	// not run now (-baseline).
	Baseline string `json:"baseline,omitempty"`
	RTK      bool   `json:"rtk,omitempty"` // the run had the rtk hook (-rtk)
}

// runOne runs one scenario in one arm and grades it.
func runOne(cfg config, scenario, arm string, rep int) (r Result) {
	r = Result{Scenario: scenario, Arm: arm, Rep: rep, Model: cfg.model}
	if cfg.agent != "claude-code" {
		r.Agent = cfg.agent
	}
	work, err := os.MkdirTemp("", fmt.Sprintf("stele-eval-%s-%s-%d-", scenario, arm, rep))
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.Dir = work
	if !cfg.keep {
		// A run's tree with its build output is large; its diff is kept
		// beside the stream instead.
		defer os.RemoveAll(work)
	}
	// Everything written under results/ is scrubbed first (scrub.go). The
	// stream is written live in the work directory, and only its scrubbed
	// copy is kept.
	sc := newScrubber(work)
	base := filepath.Join(cfg.streamDir, fmt.Sprintf("%s-%s-%d", scenario, arm, rep))
	defer func() {
		r.Error = string(sc.text([]byte(r.Error)))
		for i := range r.Notes {
			r.Notes[i] = string(sc.text([]byte(r.Notes[i])))
		}
		for i := range r.Violations {
			r.Violations[i] = string(sc.text([]byte(r.Violations[i])))
		}
		if err := sc.checkLeaks(base, cfg.gitEmail); err != nil {
			r.Error = addError(r.Error, err.Error())
		}
	}()
	repo := filepath.Join(work, "repo")
	keys := filepath.Join(work, "config")
	fixture := filepath.Join(cfg.scenarioDir, scenario, "repo")
	env := runEnv(cfg, keys)

	r.RTK = cfg.rtk
	if err := setup(fixture, repo, arm, cfg.agent, cfg.stele, cfg.rtk, env); err != nil {
		r.Error = "setup: " + err.Error()
		return r
	}
	prompt, err := os.ReadFile(filepath.Join(cfg.scenarioDir, scenario, "prompt.txt"))
	if err != nil {
		r.Error = err.Error()
		return r
	}

	if err := saveSetup(repo, prompt, base+".setup", sc.text); err != nil {
		r.Error = "saving the setup: " + err.Error()
		return r
	}
	stream := filepath.Join(work, "stream.jsonl")
	start := time.Now()
	err = runAgent(cfg, repo, arm, strings.TrimSpace(string(prompt)), env, stream)
	r.WallMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Error = err.Error()
	}
	var transcript []byte
	if cfg.agent == "codex" {
		// codex exec reports no durations: the wall time stands in.
		r.DurationMS = r.WallMS
		transcript = readRollout(cfg, readCodexStream(stream, &r), &r)
		if b, err := os.ReadFile(strings.TrimSuffix(stream, ".jsonl") + ".stderr.txt"); err == nil {
			writeScrubbed(base+".stderr.txt", b, sc.text)
		}
	} else {
		sid := readStream(stream, &r)
		transcript = readTranscript(sid, &r)
	}
	if b, err := os.ReadFile(stream); err == nil {
		writeScrubbed(base+".jsonl", b, sc.jsonl)
	}
	if transcript != nil {
		writeScrubbed(base+".transcript.jsonl", transcript, sc.jsonl)
	}
	if fi, err := os.Stat(filepath.Join(repo, ".stele", "state")); err == nil && fi.IsDir() {
		entries, _ := os.ReadDir(filepath.Join(repo, ".stele", "state"))
		r.HookFired = len(entries) > 0
	}
	r.HookFired = r.HookFired || r.Denies > 0 || r.Blocks > 0
	r.RulesTouched = rulesTouched(repo, env)

	g, err := grade(cfg, scenario, fixture, repo, env)
	if err != nil {
		r.Error = addError(r.Error, "grade: "+err.Error())
	}
	r.Grade = g
	if _, err := run(repo, env, "git", "add", "-A"); err == nil {
		if diff, err := run(repo, env, "git", "diff", "--cached", "HEAD"); err == nil {
			writeScrubbed(base+".diff", []byte(diff), sc.text)
		}
		if names, err := run(repo, env, "git", "-c", "core.quotepath=off", "diff", "--cached", "--name-only", "HEAD"); err == nil {
			if ung := ungraded(strings.Split(strings.TrimSpace(names), "\n")); len(ung) > 0 {
				r.Notes = append(r.Notes, "not graded: changed by the agent: "+strings.Join(ung, ", "))
			}
		}
	}
	return r
}

// ungraded are the files of names (paths from the repository's root) that
// the grader leaves out of its copy (checkRules): rule files and agent
// setup, at any depth. stele's own compiled rules file is stele's, not the
// agent's.
func ungraded(names []string) []string {
	var out []string
	for _, n := range names {
		if n == ".claude/rules/stele-rules.md" {
			continue
		}
		for _, seg := range strings.Split(n, "/") {
			if slices.Contains(gradeSkips, seg) {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

// saveSetup copies what the agent was given into dir, through scrub: the
// prompt, and the arm's rules and their setup as committed before the run
// (stele.md, CLAUDE.md, AGENTS.md, stele.json, stele.lock, and every
// .claude/ and .codex/ folder). .stele/ is left out: it holds the run's
// state, not its setup.
func saveSetup(repo string, prompt []byte, dir string, scrub func([]byte) []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.txt"), scrub(prompt), 0o644); err != nil {
		return err
	}
	return filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, p)
		segs := strings.Split(filepath.ToSlash(rel), "/")
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".stele" {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case slices.Contains(segs, ".claude") || slices.Contains(segs, ".codex"):
		case d.Name() == "stele.md" || d.Name() == "CLAUDE.md" || d.Name() == "AGENTS.md" ||
			d.Name() == "stele.json" || d.Name() == "stele.lock":
		default:
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, scrub(b), 0o644)
	})
}

// runEnv is the environment for stele and the agent: stele first on PATH,
// and stele's key in a directory of this run's own.
func runEnv(cfg config, keys string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "PATH", "APPDATA", "XDG_CONFIG_HOME", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT":
			continue
		case "CODEX_HOME", "GOCACHE", "GOTMPDIR":
			if cfg.agent == "codex" {
				continue
			}
		}
		env = append(env, kv)
	}
	if cfg.agent == "codex" {
		// Codex's sandbox lets the agent write in the repository and in
		// goDir, so Go's cache and temp files go there.
		env = append(env,
			"CODEX_HOME="+cfg.codexHome,
			"GOCACHE="+filepath.Join(cfg.goDir, "cache"),
			"GOTMPDIR="+filepath.Join(cfg.goDir, "tmp"),
		)
	}
	return append(env,
		"PATH="+filepath.Dir(cfg.stele)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"APPDATA="+keys,
		"XDG_CONFIG_HOME="+keys,
	)
}

// setup copies the fixture to repo and sets up the arm: no rules at all,
// the rules pasted into CLAUDE.md or AGENTS.md, or stele installed for agent
// and compiled. The result is committed, so the agent's changes are the diff
// from HEAD.
func setup(fixture, repo, arm, agent, stele string, rtk bool, env []string) error {
	if err := copyTree(fixture, repo, nil); err != nil {
		return err
	}
	rules := ruleFiles(repo)
	switch arm {
	case "none":
		for _, f := range rules {
			os.Remove(f)
		}
	case "claudemd", "agentsmd":
		name := "CLAUDE.md"
		if arm == "agentsmd" {
			name = "AGENTS.md"
		}
		for _, f := range rules {
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			md := "# Project rules\n\nFollow these rules for every change. Rules are separated by `---`; a `scope:` line lists the files a rule applies to (none means every file).\n\n" + string(b)
			if err := os.WriteFile(filepath.Join(filepath.Dir(f), name), []byte(md), 0o644); err != nil {
				return err
			}
			os.Remove(f)
		}
	case "stele", "stele-tools":
		if _, err := run(repo, env, stele, "init", "--agent="+agent); err != nil {
			return err
		}
		for _, f := range rules {
			if _, err := run(filepath.Dir(f), env, stele, "compile"); err != nil {
				return err
			}
		}
	}
	if rtk {
		if err := addRTK(repo); err != nil {
			return err
		}
	}
	return commitAll(repo, env, "fixture")
}

// rtkHook is the one hook taken from the user's own settings with -rtk: rtk
// rewrites Bash commands to compress their output. Nothing else of the
// user's settings is used, so the arms stay comparable.
var rtkHook = map[string]any{
	"matcher": "Bash",
	"hooks":   []any{map[string]any{"type": "command", "command": "rtk hook claude"}},
}

// addRTK adds rtkHook to the PreToolUse hooks in repo's .claude/settings.json,
// keeping what is there (stele's hooks, in the stele arm).
func addRTK(repo string) error {
	path := filepath.Join(repo, ".claude", "settings.json")
	settings := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &settings); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	pre, _ := hooks["PreToolUse"].([]any)
	hooks["PreToolUse"] = append(pre, rtkHook)
	settings["hooks"] = hooks
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// runAgent runs headless Claude Code on prompt in repo, saving its stream.
func runAgent(cfg config, repo, arm, prompt string, env []string, stream string) error {
	if cfg.agent == "codex" {
		return runCodex(cfg, repo, arm, prompt, env, stream)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	out, err := os.Create(stream)
	if err != nil {
		return err
	}
	defer out.Close()
	cmd := exec.CommandContext(ctx, cfg.claude, "-p", prompt,
		"--output-format", "stream-json", "--verbose",
		"--model", cfg.model,
		"--permission-mode", "bypassPermissions",
		"--setting-sources", "project",
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
	)
	if arm == "stele-tools" {
		cmd.Args = append(cmd.Args, "--append-system-prompt", toolsOnly)
	}
	cmd.Dir = repo
	cmd.Env = env
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("timed out after %s", cfg.timeout)
		}
		return fmt.Errorf("claude: %v: %s", err, strings.TrimSpace(lastLine(stderr.String())))
	}
	return nil
}

// toolsOnly is the stele-tools arm's instruction.
const toolsOnly = "Create and change files only with the Write and Edit tools, never through the shell (no heredocs, redirects, sed -i or tee)."

// Phrases stele's messages start with (internal/install/assets).
const (
	denyPhrase     = "stele paused this write"
	bashDenyPhrase = "stele stopped this Bash command"
	blockPhrase    = "stele checked your write"
)

// readStream fills r from a stream-json transcript: usage from the result
// event, and the writes denied from the tool results. It returns the
// session id.
func readStream(path string, r *Result) (session string) {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var ev struct {
			Type         string `json:"type"`
			Subtype      string `json:"subtype"`
			SessionID    string `json:"session_id"`
			Version      string `json:"claude_code_version"`
			APIKeySource string `json:"apiKeySource"`
			Model        string `json:"model"`
			Message      struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			DurationMS    int64   `json:"duration_ms"`
			DurationAPIMS int64   `json:"duration_api_ms"`
			NumTurns      int     `json:"num_turns"`
			IsError       bool    `json:"is_error"`
			CostUSD       float64 `json:"total_cost_usd"`
			Result        string  `json:"result"`
			Usage         struct {
				Input       int64 `json:"input_tokens"`
				CacheCreate int64 `json:"cache_creation_input_tokens"`
				CacheRead   int64 `json:"cache_read_input_tokens"`
				Output      int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.SessionID != "" {
			session = ev.SessionID
		}
		switch ev.Type {
		case "system":
			if ev.Subtype == "init" {
				r.AgentVersion, r.APIKeySource, r.ResolvedModel = ev.Version, ev.APIKeySource, ev.Model
			}
		case "result":
			r.DurationMS, r.DurationAPIMS, r.NumTurns = ev.DurationMS, ev.DurationAPIMS, ev.NumTurns
			r.IsError, r.CostUSD = ev.IsError, ev.CostUSD
			r.InputTokens, r.CacheCreate = ev.Usage.Input, ev.Usage.CacheCreate
			r.CacheRead, r.OutputTokens = ev.Usage.CacheRead, ev.Usage.Output
			if ev.IsError && r.Error == "" {
				r.Error = ev.Result
			}
		case "assistant":
			for _, b := range blocks(ev.Message.Content) {
				if b.Type != "tool_use" {
					continue
				}
				switch b.Name {
				case "Write", "Edit", "MultiEdit":
					r.Writes++
				case "Bash", "PowerShell":
					var in struct{ Command string }
					json.Unmarshal(b.Input, &in)
					if writesFile(in.Command) {
						r.BashWrites++
					}
				}
			}
		case "user":
			for _, b := range blocks(ev.Message.Content) {
				if b.Type != "tool_result" {
					continue
				}
				text := b.text()
				if n := strings.Count(text, denyPhrase); n > 0 {
					r.Denies += n
					r.HookBytes += len(text)
				}
				if n := strings.Count(text, bashDenyPhrase); n > 0 {
					r.Denies += n
					r.BashDenies += n
					r.HookBytes += len(text)
				}
			}
		}
	}
	return session
}

// readTranscript counts the post-write blocks, which the stream leaves out:
// Claude Code records them in the session transcript as attachments. It
// returns the transcript, to be kept beside the stream. Claude Code keeps
// it under CLAUDE_CONFIG_DIR, else ~/.claude.
func readTranscript(session string, r *Result) []byte {
	if session == "" {
		r.Error = addError(r.Error, "no session id in the stream: post-write flags not counted")
		return nil
	}
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "projects", "*", session+".jsonl"))
	var data []byte
	err := fmt.Errorf("no %s.jsonl", session)
	if len(paths) > 0 {
		data, err = os.ReadFile(paths[0])
	}
	if err != nil {
		r.Error = addError(r.Error, "transcript not found: post-write flags not counted")
		return nil
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		var ev struct {
			Attachment struct {
				Type          string `json:"type"`
				HookEvent     string `json:"hookEvent"`
				BlockingError struct {
					BlockingError string `json:"blockingError"`
				} `json:"blockingError"`
				Content string `json:"content"`
			} `json:"attachment"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Attachment.HookEvent != "PostToolUse" {
			continue
		}
		text := ev.Attachment.BlockingError.BlockingError + ev.Attachment.Content
		if strings.Contains(text, "stele") {
			r.HookBytes += len(text)
		}
		if strings.Contains(text, blockPhrase) {
			r.Blocks++
		}
	}
	return data
}

// addError adds msg to a run's error, after what is there.
func addError(old, msg string) string {
	if old == "" {
		return msg
	}
	return old + "; " + msg
}

type block struct {
	Type    string          `json:"type"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

// text is a tool result's text, given as a string or as text blocks.
func (b block) text() string {
	var s string
	if json.Unmarshal(b.Content, &s) == nil {
		return s
	}
	var parts []struct{ Text string }
	json.Unmarshal(b.Content, &parts)
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(p.Text)
	}
	return sb.String()
}

func blocks(raw json.RawMessage) []block {
	var bs []block
	json.Unmarshal(raw, &bs)
	return bs
}

// writesFile guesses whether a shell command writes a file, which the hook
// would not see (README, Limits 2).
func writesFile(cmd string) bool {
	for _, quiet := range []string{"2>&1", "2>/dev/null", "2> /dev/null", ">/dev/null", "> /dev/null", "2>$null", "> $null"} {
		cmd = strings.ReplaceAll(cmd, quiet, "")
	}
	for _, s := range []string{">", "tee ", "sed -i", "Set-Content", "Out-File", "Add-Content", "cp ", "mv "} {
		if strings.Contains(cmd, s) {
			return true
		}
	}
	return false
}

// rulesTouched reports whether the agent changed the rules or their setup.
func rulesTouched(repo string, env []string) bool {
	out, _ := run(repo, env, "git", "status", "--porcelain", "--", "stele.md", "**/stele.md", "CLAUDE.md", "**/CLAUDE.md", ".claude", "AGENTS.md", "**/AGENTS.md", ".codex", "stele.json", ":(exclude).claude/rules/stele-rules.md")
	return strings.TrimSpace(out) != ""
}

// ruleFiles returns the stele.md files under dir.
func ruleFiles(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "stele.md" {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// copyTree copies src to dst, leaving out entries skip says to.
func copyTree(src, dst string, skip func(rel string, d fs.DirEntry) bool) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if skip != nil && rel != "." && skip(filepath.ToSlash(rel), d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

func commitAll(dir string, env []string, msg string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if _, err := run(dir, env, "git", "init", "-q", "-b", "main"); err != nil {
			return err
		}
	}
	if _, err := run(dir, env, "git", "add", "-A"); err != nil {
		return err
	}
	_, err := run(dir, env, "git", "-c", "user.name=eval", "-c", "user.email=eval@example.com", "commit", "-q", "--allow-empty", "-m", msg)
	return err
}

// run runs a command in dir and returns its combined output.
func run(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	b, err := cmd.CombinedOutput()
	if err != nil {
		return string(b), fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
