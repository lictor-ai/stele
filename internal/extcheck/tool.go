package extcheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lictor-ai/stele/internal/printable"
	"github.com/lictor-ai/stele/internal/toolpath"
)

var isWindows = runtime.GOOS == "windows"

// Tool is a scanner a plugin runs, found for the project at dir.
type Tool struct {
	Name string   // as on PATH: "opengrep", "trivy"
	Bin  string   // its absolute path
	Dir  string   // the project
	Env  []string // the environment it runs with
	Work string   // the directory it runs in, outside the project
}

// ErrNotFound is a scanner not on PATH, or only inside the project.
var ErrNotFound = errors.New("not found on PATH")

// FindTool finds name on PATH as stele finds a plugin (D43, D80): only in
// PATH entries that are absolute and outside the project at dir and the git
// repository that holds it, so a copy a clone brought is passed over, never
// run. On Windows only an .exe or a .com counts: anything else would run
// through cmd.exe. One whose links lead into the project is refused. It
// runs with that cut PATH, the loader paths cut the same way, and extra.
func FindTool(name, dir string, extra ...string) (*Tool, error) {
	env := toolpath.PluginEnv(os.Environ(), dir)
	var path string
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k == "PATH" || isWindows && strings.EqualFold(k, "PATH") {
			path = v
		}
	}
	dirs := filepath.SplitList(path)
	found, ok := look(dirs, exeNames(name))
	if !ok {
		if s, ok := look(dirs, scriptNames(name)); ok {
			return nil, fmt.Errorf("%s at %s is refused: on Windows stele's checkers run only an .exe or a .com", name, s)
		}
		return nil, fmt.Errorf("%s: %w (entries inside the project are not searched)", name, ErrNotFound)
	}
	bin, err := toolpath.Vet(name, found, dir)
	if err != nil {
		return nil, err
	}
	work, err := workDir(dir)
	if err != nil {
		return nil, err
	}
	return &Tool{Name: name, Bin: bin, Dir: dir, Env: append(env, extra...), Work: work}, nil
}

func exeNames(name string) []string {
	if isWindows {
		return []string{name + ".exe", name + ".com"}
	}
	return []string{name}
}

func scriptNames(name string) []string {
	if !isWindows {
		return nil
	}
	return []string{name + ".bat", name + ".cmd", name + ".ps1", name}
}

// look returns the first regular file named one of names in dirs, in order;
// on Unix it must be executable.
func look(dirs, names []string) (string, bool) {
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
			if !isWindows && info.Mode().Perm()&0o111 == 0 {
				continue
			}
			return p, true
		}
	}
	return "", false
}

// workDir is where the scanner runs: the plugin's own working directory,
// which stele puts outside the project (D80), else the temp directory. Never
// the project: on Windows a program finds DLLs and programs in its working
// directory first, and a scanner reads config files there (trivy.yaml).
func workDir(dir string) (string, error) {
	var tries []string
	if wd, err := os.Getwd(); err == nil {
		tries = append(tries, wd)
	}
	tries = append(tries, os.TempDir())
	for _, w := range tries {
		if abs, err := toolpath.Vet("working directory", w, dir); err == nil {
			return abs, nil
		}
	}
	return "", errors.New("no directory outside the project to run the scanner in")
}

// maxOutput caps what a scanner may write on stdout.
const maxOutput = 256 << 20

// Run runs the tool with args, with no shell, in its working directory, and
// returns its stdout and exit status. A scanner that could not start, wrote
// more than maxOutput, or was stopped is an error; a non-zero status is not,
// since scanners use some for findings: the caller decides.
func (t *Tool) Run(ctx context.Context, args ...string) (stdout []byte, code int, stderr string, err error) {
	cmd := exec.CommandContext(ctx, t.Bin, args...)
	cmd.Dir = t.Work
	cmd.Env = t.Env
	cmd.Stdin = nil
	out := &capped{max: maxOutput}
	var errOut tail
	cmd.Stdout, cmd.Stderr = out, &errOut
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return nil, 0, errOut.String(), fmt.Errorf("%s stopped: %v", t.Name, ctx.Err())
	case out.over:
		return nil, 0, errOut.String(), fmt.Errorf("%s wrote more than %d MiB", t.Name, maxOutput>>20)
	case errors.As(err, &ee):
		return out.buf.Bytes(), ee.ExitCode(), errOut.String(), nil
	case err != nil:
		return nil, 0, errOut.String(), fmt.Errorf("running %s: %v", t.Bin, err)
	}
	return out.buf.Bytes(), 0, errOut.String(), nil
}

// Failed is the error for a scanner that exited with code: its name, the
// status and the end of its stderr.
func (t *Tool) Failed(code int, stderr string) error {
	msg := fmt.Sprintf("%s exited with status %d", t.Name, code)
	if s := lastLines(stderr, 5); s != "" {
		msg += ": " + s
	}
	return errors.New(msg)
}

// lastLines is the last n non-empty lines of s, cleaned, joined by " ".
func lastLines(s string, n int) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(printable.Clean(l)); l != "" {
			keep = append(keep, l)
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, " ")
}

// capped keeps the first max bytes written to it, and notes there was more.
type capped struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.max {
		c.over = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

// tail keeps the last 64 KiB written to it.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 64<<10 {
		t.b = t.b[len(t.b)-64<<10:]
	}
	return len(p), nil
}

func (t *tail) String() string { return string(t.b) }
