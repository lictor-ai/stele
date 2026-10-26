// Package functional tests the stele binary from the outside: it builds
// cmd/stele once, then drives init, compile, check and hook in throwaway
// repositories with the payloads Claude Code sends. Nothing here imports
// stele's packages, so these tests hold whatever the code inside looks like.
//
// Measurements (hook latency, bytes and estimated tokens the hook adds to
// the conversation) are printed with -v, and saved as JSON to the file named
// by STELE_FT_MEASURE when it is set. The agent A/B eval, which measures real
// tokens and time with a model, is in agent/.
package functional

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// bin is the stele binary under test, built by TestMain.
var bin string

// runTimeout bounds one stele call.
const runTimeout = 2 * time.Minute

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "stele-functional-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	bin = filepath.Join(dir, "stele"+exe())
	build := exec.Command("go", "build", "-o", bin, "../../cmd/stele")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building stele:", err)
		os.Exit(2)
	}
	code := m.Run()
	writeMeasurements()
	os.RemoveAll(dir)
	os.Exit(code)
}

func exe() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// project is a throwaway git repository with its own stele key.
type project struct {
	t   *testing.T
	dir string
	env []string
	// hookArgs are stele hook's arguments; nil is --installed=devel.
	hookArgs []string
}

// newProject makes a git repository holding stele.md (unless md is empty),
// with a signing key no other test shares.
func newProject(t *testing.T, md string) *project {
	t.Helper()
	dir := t.TempDir()
	// Resolve 8.3 names and symlinks (macOS /var): the hook compares paths.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := t.TempDir()
	p := &project{t: t, dir: dir, env: keyEnv(key)}
	p.git("init", "-q")
	p.git("config", "user.email", "t@example.com")
	p.git("config", "user.name", "t")
	p.git("config", "core.autocrlf", "false")
	if md != "" {
		p.write("stele.md", md)
	}
	return p
}

// keyEnv points os.UserConfigDir, where stele keeps its key, at dir.
func keyEnv(dir string) []string {
	env := os.Environ()
	return append(env, "APPDATA="+dir, "XDG_CONFIG_HOME="+dir, "HOME="+dir)
}

// on returns p reporting to t, for a subtest sharing p.
func (p *project) on(t *testing.T) *project { q := *p; q.t = t; return &q }

func (p *project) path(rel string) string { return filepath.Join(p.dir, filepath.FromSlash(rel)) }

func (p *project) write(rel, content string) {
	p.t.Helper()
	f := p.path(rel)
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

func (p *project) read(rel string) string {
	p.t.Helper()
	data, err := os.ReadFile(p.path(rel))
	if err != nil {
		p.t.Fatal(err)
	}
	return string(data)
}

func (p *project) exists(rel string) bool {
	_, err := os.Lstat(p.path(rel))
	return err == nil
}

func (p *project) git(args ...string) string {
	p.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = p.dir
	// The developer's git config (signing, hooks, templates) stays out.
	cmd.Env = append(p.env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		p.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (p *project) commit() {
	p.t.Helper()
	p.git("add", "-A")
	p.git("commit", "-q", "-m", "c", "--allow-empty")
}

// stele runs the binary in dir (relative to the project) and returns its
// exit code and output.
func (p *project) steleIn(dir string, stdin []byte, args ...string) (code int, stdout, stderr string) {
	p.t.Helper()
	// A stele call takes milliseconds. One that hangs, as a process starved of
	// memory can on Windows, fails its test instead of the whole package.
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = p.path(dir)
	cmd.Env = p.env
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if ctx.Err() != nil {
		p.t.Fatalf("stele %v did not finish within %v", args, runTimeout)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), out.String(), errOut.String()
	} else if err != nil {
		p.t.Fatalf("running stele %v: %v", args, err)
	}
	return 0, out.String(), errOut.String()
}

func (p *project) stele(args ...string) (int, string, string) {
	p.t.Helper()
	return p.steleIn(".", nil, args...)
}

// mustStele runs stele and fails the test unless it exits with want.
func (p *project) mustStele(want int, args ...string) string {
	p.t.Helper()
	code, out, errOut := p.stele(args...)
	if code != want {
		p.t.Fatalf("stele %v: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", args, code, want, out, errOut)
	}
	return out + errOut
}

// compile runs stele compile and fails on error.
func (p *project) compile() { p.t.Helper(); p.mustStele(0, "compile") }

// event is one hook payload, as Claude Code sends it.
type event struct {
	Session, Prompt, Agent string
	Name                   string // PreToolUse or PostToolUse
	Tool                   string // Write, Edit, MultiEdit
	File                   string // relative to the project, or absolute
	Content                string // Write
	Old, New               string // Edit
	ReplaceAll             bool
	Edits                  []string // MultiEdit new_strings
	Patch                  []hunk   // tool_response.structuredPatch, nil for none
	Cwd                    string
}

type hunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

func (p *project) payload(e event) []byte {
	if e.Session == "" {
		e.Session = "sess-1"
	}
	if e.Tool == "" {
		e.Tool = "Write"
	}
	file := e.File
	if !filepath.IsAbs(file) && !strings.HasPrefix(file, "rel:") {
		file = p.path(file)
	}
	file = strings.TrimPrefix(file, "rel:")

	// Start from a payload Claude Code really sent (cmd/stele/testdata/hook)
	// and change only the values, so the tests never invent its shape.
	m := p.recorded(e)
	in, _ := m["tool_input"].(map[string]any)
	in["file_path"] = file
	switch e.Tool {
	case "Write":
		in["content"] = e.Content
	case "Edit":
		in["old_string"], in["new_string"], in["replace_all"] = e.Old, e.New, e.ReplaceAll
	case "MultiEdit":
		var eds []map[string]any
		for _, n := range e.Edits {
			eds = append(eds, map[string]any{"old_string": "x", "new_string": n})
		}
		in["edits"] = eds
	}
	cwd := e.Cwd
	if cwd == "" {
		cwd = p.dir
	}
	m["session_id"], m["cwd"], m["hook_event_name"] = e.Session, cwd, e.Name
	if e.Prompt != "" {
		m["prompt_id"] = e.Prompt
	}
	if e.Agent != "" {
		m["agent_id"] = e.Agent
	}
	if e.Name == "PostToolUse" {
		resp, _ := m["tool_response"].(map[string]any)
		resp["filePath"] = file
		if _, ok := resp["newString"]; ok {
			resp["oldString"], resp["newString"] = e.Old, e.New
		}
		// The recorded file before the edit is the recording's, not this
		// test's; stele reads the file from disk.
		delete(resp, "originalFile")
		delete(resp, "structuredPatch")
		if e.Patch != nil {
			resp["structuredPatch"] = e.Patch
		}
	} else {
		// A PreToolUse payload is the same without the tool's result.
		delete(m, "tool_response")
		delete(m, "duration_ms")
	}
	data, err := json.Marshal(m)
	if err != nil {
		p.t.Fatal(err)
	}
	return data
}

// recorded returns the payload recorded in cmd/stele/testdata/hook for e's
// tool, with a patch or without, decoded.
func (p *project) recorded(e event) map[string]any {
	p.t.Helper()
	name := map[string]string{"Write": "post-write", "Edit": "post-edit", "MultiEdit": "post-multiedit"}[e.Tool]
	if name == "" {
		p.t.Fatalf("no recorded payload for tool %q", e.Tool)
	}
	if e.Patch != nil && e.Tool != "Write" {
		name += "-patch"
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "cmd", "stele", "testdata", "hook", name+".json"))
	if err != nil {
		p.t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		p.t.Fatalf("%s.json: %v", name, err)
	}
	return m
}

// answer is the hook's reply, decoded.
type answer struct {
	Raw      string
	Deny     bool   // PreToolUse permissionDecision deny
	Reason   string // deny reason
	Block    bool   // PostToolUse decision block
	Context  string // additionalContext
	Warning  string // systemMessage
	Duration time.Duration
}

func (a answer) silent() bool { return strings.TrimSpace(a.Raw) == "" }

// hook sends one payload to stele hook. The hook must always exit 0 and print
// nothing or one JSON object: anything else would break Claude Code.
func (p *project) hook(e event) answer {
	p.t.Helper()
	return p.hookRaw(p.payload(e))
}

func (p *project) hookRaw(payload []byte) answer {
	p.t.Helper()
	args := p.hookArgs
	if args == nil {
		args = []string{"--installed=devel"}
	}
	start := time.Now()
	code, out, errOut := p.steleIn(".", payload, append([]string{"hook"}, args...)...)
	d := time.Since(start)
	if code != 0 {
		p.t.Fatalf("stele hook exited %d (would block the write whatever it says)\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	a := answer{Raw: out, Duration: d}
	if strings.TrimSpace(out) == "" {
		return a
	}
	var r struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
		HSO      *struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
			AdditionalContext        string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
		SystemMessage string `json:"systemMessage"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		p.t.Fatalf("stele hook printed something that is not the expected JSON: %v\n%s", err, out)
	}
	if dec.More() {
		p.t.Fatalf("stele hook printed more than one JSON value:\n%s", out)
	}
	a.Block, a.Warning = r.Decision == "block", r.SystemMessage
	if a.Block {
		a.Reason = r.Reason
	}
	if r.HSO != nil {
		a.Deny = r.HSO.PermissionDecision == "deny"
		if a.Deny {
			a.Reason = r.HSO.PermissionDecisionReason
		}
		a.Context = r.HSO.AdditionalContext
	}
	return a
}

// useInstalledHook makes p.hook run stele hook with the arguments stele
// init wrote on the PreToolUse command in .claude/settings.json, as Claude
// Code would.
func (p *project) useInstalledHook() {
	p.t.Helper()
	var s struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(p.read(".claude/settings.json")), &s); err != nil {
		p.t.Fatal(err)
	}
	for _, g := range s.Hooks["PreToolUse"] {
		for _, h := range g.Hooks {
			if w := strings.Fields(h.Command); len(w) >= 2 && w[0] == "stele" && w[1] == "hook" {
				p.hookArgs = w[2:]
				return
			}
		}
	}
	p.t.Fatal("no stele hook in .claude/settings.json")
}

// pre and post are the common payloads.
func pre(file, content string) event {
	return event{Name: "PreToolUse", Prompt: "turn-1", Tool: "Write", File: file, Content: content}
}

func post(file, content string) event {
	return event{Name: "PostToolUse", Prompt: "turn-1", Tool: "Write", File: file, Content: content}
}

// writeVia does what Claude Code does for one Write: the pre hook, and if it
// lets the write through, the write and the post hook.
func (p *project) writeVia(e event) (pre, post answer) {
	p.t.Helper()
	e.Name = "PreToolUse"
	pre = p.hook(e)
	if pre.Deny {
		return pre, answer{}
	}
	p.write(e.File, e.Content)
	e.Name = "PostToolUse"
	return pre, p.hook(e)
}

// agentWrite writes as an agent does: on a deny it reads the rules (a model
// round trip, longer than the batch window) and retries once. It returns the
// post-write answer.
func (p *project) agentWrite(e event) answer {
	p.t.Helper()
	pre, post := p.writeVia(e)
	if !pre.Deny {
		return post
	}
	time.Sleep(3100 * time.Millisecond)
	pre, post = p.writeVia(e)
	if pre.Deny {
		p.t.Fatalf("retry after reading the rules was denied:\n%s", pre.Reason)
	}
	return post
}

// hash snapshots files so a test can prove stele left them alone.
func (p *project) hash(rels ...string) map[string]string {
	m := map[string]string{}
	for _, r := range rels {
		data, err := os.ReadFile(p.path(r))
		if err != nil {
			m[r] = "<missing>"
			continue
		}
		m[r] = string(data)
	}
	return m
}

// measurements collects what the tests measure, for STELE_FT_MEASURE.
var measurements = struct {
	sync.Mutex
	m map[string]any
}{m: map[string]any{}}

func measure(name string, v any) {
	measurements.Lock()
	defer measurements.Unlock()
	measurements.m[name] = v
}

func writeMeasurements() {
	measurements.Lock()
	defer measurements.Unlock()
	if len(measurements.m) == 0 {
		return
	}
	measurements.m["_meta"] = map[string]any{
		"goos": runtime.GOOS, "goarch": runtime.GOARCH, "cpus": runtime.NumCPU(),
		"at": time.Now().Format(time.RFC3339), "token_estimate": "bytes/4 (cl100k-like average for English and code)",
	}
	data, err := json.MarshalIndent(measurements.m, "", "  ")
	if err != nil {
		return
	}
	// Only on request: a test run must leave the source tree as it found it.
	if out := os.Getenv("STELE_FT_MEASURE"); out != "" {
		os.WriteFile(out, append(data, '\n'), 0o644)
	}
}

// tokens estimates the tokens a text costs the model. Close enough to
// compare; the agent eval measures real ones.
func tokens(s string) int { return (len(s) + 3) / 4 }

// stats summarises durations.
type stats struct {
	N                  int
	P50, P95, P99, Max float64 // milliseconds
	Mean               float64
}

func summarize(ds []time.Duration) stats {
	if len(ds) == 0 {
		return stats{}
	}
	ms := make([]float64, len(ds))
	var sum float64
	for i, d := range ds {
		ms[i] = float64(d.Microseconds()) / 1000
		sum += ms[i]
	}
	sort.Float64s(ms)
	at := func(q float64) float64 { return ms[min(len(ms)-1, int(q*float64(len(ms))))] }
	return stats{N: len(ms), P50: at(.5), P95: at(.95), P99: at(.99), Max: ms[len(ms)-1], Mean: sum / float64(len(ms))}
}
