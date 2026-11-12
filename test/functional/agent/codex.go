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
	"runtime"
	"strings"
	"time"
)

// The Codex side of the eval (-agent codex): codex exec runs the task with a
// CODEX_HOME of the eval's own, which holds a copy of the user's auth.json
// and nothing else of theirs, so the arms stay comparable. The project and
// stele's hooks are trusted for one run with -c overrides; the user's
// ~/.codex is never changed.

// codexShellDeny is how stele's deny of a shell command starts under Codex
// (internal/install/assets/bash-message.tmpl, with Codex's words).
const codexShellDeny = "stele stopped this shell command"

// findCodex returns the command that starts Codex: on Windows, node and the
// npm package's codex.js, which sets up Codex's environment and takes its
// arguments as they are (the codex.cmd shim would pass them through cmd.exe).
func findCodex() []string {
	if runtime.GOOS == "windows" {
		js := filepath.Join(os.Getenv("APPDATA"), "npm", "node_modules", "@openai", "codex", "bin", "codex.js")
		if _, err := os.Stat(js); err == nil {
			return []string{"node", js}
		}
	}
	return []string{"codex"}
}

// codexCommand splits the -codex flag: a .js file runs with node.
func codexCommand(flag string) []string {
	if strings.HasSuffix(flag, ".js") {
		return []string{"node", flag}
	}
	return []string{flag}
}

// newCodexHome makes the eval's CODEX_HOME, with the user's auth.json copied
// in. The caller removes it at the end.
func newCodexHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	src := filepath.Join(home, ".codex", "auth.json")
	if v := os.Getenv("CODEX_HOME"); v != "" {
		src = filepath.Join(v, "auth.json")
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("codex login: %w", err)
	}
	// Not under the temp directory: there Codex refuses to set up its helper
	// binaries (the apply_patch alias in its shell).
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(cache, "stele-eval-codex-home-")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// tomlKey quotes s as a TOML literal key.
func tomlKey(s string) string { return "'" + s + "'" }

// codexTrust is the -c override that trusts the project at repo.
func codexTrust(repo string) []string {
	return []string{"-c", "projects={" + tomlKey(repo) + "={trust_level=\"trusted\"}}"}
}

// codexHookState asks Codex, through its app-server and without a model
// call, for the hooks it found in repo, and returns the -c override that
// trusts each as it is now.
func codexHookState(cfg config, repo string, env []string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := append(append([]string{}, cfg.codex[1:]...), "app-server")
	args = append(args, codexTrust(repo)...)
	cmd := exec.CommandContext(ctx, cfg.codex[0], args...)
	cmd.Dir = repo
	cmd.Env = env
	in, err := cmd.StdinPipe()
	if err != nil {
		return "", 0, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", 0, err
	}
	if err := cmd.Start(); err != nil {
		return "", 0, err
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	for _, m := range []string{
		`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"stele-eval","version":"0"}}}`,
		`{"method":"initialized"}`,
		fmt.Sprintf(`{"id":2,"method":"hooks/list","params":{"cwds":[%q]}}`, repo),
	} {
		if _, err := in.Write([]byte(m + "\n")); err != nil {
			return "", 0, err
		}
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var msg struct {
			ID     int `json:"id"`
			Result struct {
				Data []struct {
					Hooks []struct {
						Key         string `json:"key"`
						CurrentHash string `json:"currentHash"`
					} `json:"hooks"`
				} `json:"data"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.ID != 2 {
			continue
		}
		if len(msg.Error) > 0 {
			return "", 0, fmt.Errorf("hooks/list: %s", msg.Error)
		}
		var parts []string
		for _, d := range msg.Result.Data {
			for _, h := range d.Hooks {
				parts = append(parts, fmt.Sprintf("%s={trusted_hash=%q}", tomlKey(h.Key), h.CurrentHash))
			}
		}
		return "hooks.state={" + strings.Join(parts, ",") + "}", len(parts), nil
	}
	return "", 0, fmt.Errorf("hooks/list: no answer from codex app-server")
}

// runCodex runs codex exec on prompt in repo, saving its --json stream.
func runCodex(cfg config, repo, arm, prompt string, env []string, stream string) error {
	// stele counts AGENTS.md as loaded only when nothing in the project
	// changed in the 2 s before the session started (README, Limits,
	// Codex); setup has just written the tree. A user starts Codex later.
	time.Sleep(3 * time.Second)
	args := append([]string{}, cfg.codex[1:]...)
	args = append(args, "exec", "--json",
		"-m", cfg.model, "-c", fmt.Sprintf("model_reasoning_effort=%q", cfg.effort),
		"-s", "workspace-write", "-c", `windows.sandbox="unelevated"`,
		"--add-dir", cfg.goDir,
	)
	args = append(args, codexTrust(repo)...)
	if arm == "stele" || arm == "stele-tools" {
		state, n, err := codexHookState(cfg, repo, env)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("codex found no hooks in %s", repo)
		}
		args = append(args, "-c", state)
	}
	if arm == "stele-tools" {
		prompt += "\n\n" + codexToolsOnly
	}
	args = append(args, "-")

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	out, err := os.Create(stream)
	if err != nil {
		return err
	}
	defer out.Close()
	cmd := exec.CommandContext(ctx, cfg.codex[0], args...)
	cmd.Dir = repo
	cmd.Env = env
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 10 * time.Second
	err = cmd.Run()
	os.WriteFile(strings.TrimSuffix(stream, ".jsonl")+".stderr.txt", stderr.Bytes(), 0o644)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("timed out after %s", cfg.timeout)
		}
		return fmt.Errorf("codex: %v: %s", err, strings.TrimSpace(lastLine(stderr.String())))
	}
	return nil
}

// codexToolsOnly is the stele-tools arm's instruction, for Codex.
const codexToolsOnly = "Create and change files only with apply_patch, never through the shell (no Set-Content, Out-File, redirects or heredocs)."

// readCodexStream fills r from codex exec --json: tokens from turn.completed,
// writes from the file changes and shell commands. It returns the thread id.
func readCodexStream(path string, r *Result) (thread string) {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Message  string `json:"message"`
			Error    struct {
				Message string `json:"message"`
			} `json:"error"`
			Item struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Status  string `json:"status"`
			} `json:"item"`
			Usage struct {
				Input  int64 `json:"input_tokens"`
				Cached int64 `json:"cached_input_tokens"`
				Write  int64 `json:"cache_write_input_tokens"`
				Output int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "thread.started":
			thread = ev.ThreadID
		case "turn.completed":
			// input_tokens counts the cached ones too.
			r.InputTokens += ev.Usage.Input - ev.Usage.Cached - ev.Usage.Write
			r.CacheRead += ev.Usage.Cached
			r.CacheCreate += ev.Usage.Write
			r.OutputTokens += ev.Usage.Output
		case "turn.failed":
			r.IsError = true
			if r.Error == "" {
				r.Error = ev.Error.Message
			}
		case "error":
			if r.Error == "" {
				r.Error = ev.Message
			}
		case "item.completed":
			switch ev.Item.Type {
			case "file_change":
				r.Writes++
			case "command_execution":
				if writesFile(ev.Item.Command) {
					r.BashWrites++
				}
			}
		}
	}
	return thread
}

// readRollout reads the session's rollout in CODEX_HOME: the model requests
// (turns), and what stele said in the tool results: writes denied to show
// rules, shell commands stopped, and writes sent back for a violation. It
// returns the rollout, to be kept beside the stream.
func readRollout(cfg config, thread string, r *Result) []byte {
	if thread == "" {
		return nil
	}
	var path string
	filepath.WalkDir(filepath.Join(cfg.codexHome, "sessions"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), thread+".jsonl") {
			path = p
			return fs.SkipAll
		}
		return nil
	})
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		var ev struct {
			Type    string `json:"type"`
			Payload struct {
				Type   string          `json:"type"`
				Output json.RawMessage `json:"output"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Type == "token_usage_record" {
			r.NumTurns++
			continue
		}
		if ev.Type != "response_item" {
			continue
		}
		switch ev.Payload.Type {
		case "function_call_output", "custom_tool_call_output":
		default:
			continue
		}
		text := outputText(ev.Payload.Output)
		n := strings.Count(text, denyPhrase)
		s := strings.Count(text, codexShellDeny)
		b := strings.Count(text, blockPhrase)
		r.Denies += n + s
		r.BashDenies += s
		r.Blocks += b
		if n+s+b > 0 {
			r.HookBytes += len(text)
		}
	}
	return data
}

// outputText is a tool output's text: a string, an object with output, or
// a list of text items.
func outputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct{ Output string }
	if json.Unmarshal(raw, &obj) == nil && obj.Output != "" {
		return obj.Output
	}
	var parts []struct{ Text string }
	json.Unmarshal(raw, &parts)
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(p.Text)
	}
	return sb.String()
}
