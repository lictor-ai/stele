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

// LookPath finds a program on PATH. Tests replace it.
var LookPath = exec.LookPath

// waitDelay is how long Run waits for stdout and stderr to close after the
// plugin exits or is stopped: a child it left holding them cannot keep stele
// waiting.
const waitDelay = 2 * time.Second

// Runner runs the plugins of the project at root.
type Runner struct {
	root   string
	env    []string
	stderr io.Writer
}

// New is the Runner for the project at root. What plugins write on stderr
// goes to stderr, each line cleaned and prefixed with the plugin's name; nil
// drops it. The environment is worked out once: PATH loses every entry inside
// the project (toolpath.Env).
func New(root string, stderr io.Writer) *Runner {
	if stderr == nil {
		stderr = io.Discard
	}
	return &Runner{root: root, env: toolpath.Env(os.Environ(), root), stderr: stderr}
}

// Find returns the absolute path of stele-<kind>-<name> on PATH. A name that
// is not a plugin name, a program found through a relative PATH entry, one
// inside the project root or the repository that holds it (D43), and on
// Windows one that is not an .exe or .com, which would run through cmd.exe,
// are refused.
func (r *Runner) Find(kind plugin.Kind, name string) (string, error) {
	if err := plugin.ValidName(name); err != nil {
		return "", fmt.Errorf("%w: %v", plugin.ErrPluginRefused, err)
	}
	exe := plugin.Exe(kind, name)
	p, err := LookPath(exe)
	if errors.Is(err, exec.ErrDot) {
		return "", fmt.Errorf("%s: found through a relative PATH entry: %w", exe, plugin.ErrPluginRefused)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", exe, plugin.ErrPluginNotFound)
	}
	if runtime.GOOS == "windows" {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".exe", ".com":
		default:
			return "", fmt.Errorf("%s at %s: %w: a plugin on Windows is an .exe or a .com", exe, p, plugin.ErrPluginRefused)
		}
	}
	abs, err := toolpath.Vet(exe, p, r.root)
	if errors.Is(err, toolpath.ErrRefused) {
		return "", fmt.Errorf("%w: %v", plugin.ErrPluginRefused, err)
	}
	return abs, err
}

// Run starts bin with no arguments and no shell, in the project root, writes
// req to its stdin, and returns its stdout. An answer over max bytes is
// ErrProtocol; no answer in timeout is ErrTimeout, and the plugin is stopped;
// a non-zero exit is a *plugin.ExitError with the end of its stderr.
func (r *Runner) Run(ctx context.Context, bin string, req []byte, max int, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Dir = r.root
	cmd.Env = r.env
	cmd.Stdin = bytes.NewReader(req)
	out := &capped{max: max}
	cmd.Stdout = out
	errs := newStderr(r.stderr, strings.TrimSuffix(filepath.Base(bin), filepath.Ext(bin)))
	cmd.Stderr = errs
	cmd.WaitDelay = waitDelay
	stopGroup(cmd)
	err := cmd.Run()
	errs.flush()
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return nil, plugin.Timeout(timeout)
	case out.over:
		return nil, fmt.Errorf("%w: answer over %d bytes", plugin.ErrProtocol, max)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil, &plugin.ExitError{Code: ee.ExitCode(), Stderr: errs.tailString()}
	}
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
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
