// Package pluginexec finds and starts external plugins (D78, D80): the one
// plugin.Runner. Only cmd/stele/sources.go hands it to internal/plugin, and
// only for stele compile, stele check and stele init: the hook and stele
// stats never reach it (D45, D79), as cmd/stele/hookreach_test.go checks. It
// imports os/exec and never a network package; it never prints, and what a
// plugin writes on stderr goes to the writer it is given.
package pluginexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/lictor-ai/stele/internal/plugin"
	"github.com/lictor-ai/stele/internal/printable"
	"github.com/lictor-ai/stele/internal/toolpath"
)

// waitDelay is how long Run waits for stdout and stderr to close after the
// plugin exits or is stopped: a child it left holding them cannot keep stele
// waiting.
const waitDelay = 2 * time.Second

// WorkBase is where New makes the directory plugins run in: stele/plugins
// under it. Tests replace it, so they never touch the user's cache.
var WorkBase = os.UserCacheDir

// Runner runs the plugins of the project at root.
type Runner struct {
	root   string
	env    []string
	dir    string // the working directory, outside the project
	dirErr error
	stderr io.Writer
}

// New is the Runner for the project at root. What plugins write on stderr
// goes to stderr, each line cleaned and prefixed with the plugin's name; nil
// drops it. The environment is worked out once: PATH and the variables a
// loader reads lose every entry that is empty, relative or inside the project
// (toolpath.PluginEnv).
func New(root string, stderr io.Writer) *Runner {
	if stderr == nil {
		stderr = io.Discard
	}
	r := &Runner{root: root, env: toolpath.PluginEnv(os.Environ(), root), stderr: stderr}
	r.dir, r.dirErr = workDir(root)
	return r
}

// workDir is the directory plugins run in: stele/plugins under the user's
// cache directory, or under the temp directory, whichever is outside the
// project. A plugin never runs in the project: on Windows a program started
// there finds the DLLs and the programs a clone brought before the system's
// (D80).
func workDir(root string) (string, error) {
	var bases []string
	if b, err := WorkBase(); err == nil {
		bases = append(bases, b)
	}
	bases = append(bases, os.TempDir())
	for _, b := range bases {
		dir := filepath.Join(b, "stele", "plugins")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			continue
		}
		if abs, err := toolpath.Vet("plugin directory", dir, root); err == nil {
			return abs, nil
		}
	}
	return "", fmt.Errorf("%w: no directory outside the project to run plugins in", plugin.ErrPluginRefused)
}

// Find returns the absolute path of stele-<kind>-<name>, looked up on the
// PATH plugins get: one with no entry that is empty, relative or inside the
// project, so a copy a clone put there is passed over, never run (D43). On
// Windows only an .exe or a .com is looked for: a .bat or .cmd would run
// through cmd.exe. A name that is not a plugin name, and a program that
// leads into the project through a link, are refused.
func (r *Runner) Find(kind plugin.Kind, name string) (string, error) {
	if err := plugin.ValidName(name); err != nil {
		return "", fmt.Errorf("%w: %v", plugin.ErrPluginRefused, err)
	}
	exe := plugin.Exe(kind, name)
	dirs := filepath.SplitList(r.pathValue())
	p, ok := look(dirs, exe, executableNames(exe))
	if !ok {
		if s, ok := look(dirs, exe, scriptNames(exe)); ok {
			return "", fmt.Errorf("%s at %s: %w: a plugin on Windows is an .exe or a .com", exe, s, plugin.ErrPluginRefused)
		}
		return "", fmt.Errorf("%s: %w", exe, plugin.ErrPluginNotFound)
	}
	abs, err := toolpath.Vet(exe, p, r.root)
	if errors.Is(err, toolpath.ErrRefused) {
		return "", fmt.Errorf("%w: %v", plugin.ErrPluginRefused, err)
	}
	return abs, err
}

// pathValue is PATH in the plugins' environment.
func (r *Runner) pathValue() string {
	for _, kv := range r.env {
		name, value, _ := strings.Cut(kv, "=")
		if name == "PATH" || runtime.GOOS == "windows" && strings.EqualFold(name, "PATH") {
			return value
		}
	}
	return ""
}

// executableNames are the file names exe may have on this OS.
func executableNames(exe string) []string {
	if runtime.GOOS == "windows" {
		return []string{exe + ".exe", exe + ".com"}
	}
	return []string{exe}
}

// scriptNames are names on Windows stele never runs, looked for only to say
// why.
func scriptNames(exe string) []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	return []string{exe + ".bat", exe + ".cmd", exe + ".ps1", exe}
}

// look returns the first regular file named one of names in dirs, in order;
// on Unix it must be executable.
func look(dirs []string, exe string, names []string) (string, bool) {
	for _, d := range dirs {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		for _, n := range names {
			p := filepath.Join(d, n)
			info, err := os.Stat(p)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
				continue
			}
			return p, true
		}
	}
	return "", false
}

// Run starts bin with no arguments and no shell, in a directory outside the
// project, writes req to its stdin, and returns its stdout. An answer over
// max bytes is ErrProtocol; no answer in timeout is ErrTimeout; a cancelled
// ctx is ErrCancelled; a non-zero exit is a *plugin.ExitError with the end of
// its stderr. Whatever way the call ends, what the plugin started is stopped
// with it: its process group on Unix, its job on Windows.
func (r *Runner) Run(ctx context.Context, bin string, req []byte, max int, timeout time.Duration) ([]byte, error) {
	if r.dirErr != nil {
		return nil, r.dirErr
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Dir = r.dir
	cmd.Env = r.env
	cmd.Stdin = bytes.NewReader(req)
	out := &capped{max: max}
	cmd.Stdout = out
	errs := newStderr(r.stderr, strings.TrimSuffix(filepath.Base(bin), filepath.Ext(bin)))
	cmd.Stderr = errs
	cmd.WaitDelay = waitDelay
	group(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	stop := contain(cmd)
	err := cmd.Wait()
	stop()
	errs.flush()
	switch {
	// A plugin that exited 0 and still failed was held up by what it left
	// holding its output, past waitDelay or the deadline. One stopped at its
	// timeout did not exit 0.
	case err != nil && cmd.ProcessState != nil && cmd.ProcessState.Success():
		return nil, fmt.Errorf("%w: it left a process holding its output, which stele stopped", plugin.ErrProtocol)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, plugin.Timeout(timeout)
	case ctx.Err() != nil:
		return nil, plugin.ErrCancelled
	case out.over:
		return nil, fmt.Errorf("%w: answer over %d bytes", plugin.ErrProtocol, max)
	}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		return nil, &plugin.ExitError{Code: ee.ExitCode(), Stderr: errs.tailString()}
	case err != nil:
		return nil, err
	}
	return out.buf.Bytes(), nil
}

// capped keeps the first max bytes written to it, and notes there was more.
type capped struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	room := c.max - c.buf.Len()
	if len(p) > room {
		c.over = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

// maxStderrLines caps the lines of a plugin's stderr passed through in one
// call, and maxTail what is kept of it for an error message.
const (
	maxStderrLines = 200
	maxTail        = 64 << 10
)

// stderrWriter passes a plugin's stderr through, line by line, each cleaned
// (printable.Clean) and prefixed with the plugin's name, and keeps its end.
type stderrWriter struct {
	mu      sync.Mutex
	out     io.Writer
	prefix  string
	partial []byte
	lines   int
	dropped bool
	tail    []byte
}

func newStderr(out io.Writer, name string) *stderrWriter {
	return &stderrWriter{out: out, prefix: name + ": "}
}

func (w *stderrWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tail = append(w.tail, p...)
	if len(w.tail) > maxTail {
		w.tail = w.tail[len(w.tail)-maxTail:]
	}
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		w.line(string(w.partial[:i]))
		w.partial = w.partial[i+1:]
	}
	if len(w.partial) > maxTail {
		w.line(string(w.partial))
		w.partial = nil
	}
	return len(p), nil
}

func (w *stderrWriter) line(s string) {
	s = strings.TrimRight(printable.Clean(s), " ")
	switch {
	case w.lines < maxStderrLines:
		fmt.Fprintf(w.out, "%s%s\n", w.prefix, s)
		w.lines++
	case !w.dropped:
		fmt.Fprintf(w.out, "%s… more dropped\n", w.prefix)
		w.dropped = true
	}
}

// flush passes on a last line with no newline.
func (w *stderrWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.partial) > 0 {
		w.line(string(w.partial))
		w.partial = nil
	}
}

func (w *stderrWriter) tailString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return printable.Lines(string(w.tail))
}
