package pluginexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/plugin"
)

// outside is a directory outside any project: plugins found there may run.
func outside(t *testing.T) string { return filepath.Join(t.TempDir(), "bin") }

func describeReq(t *testing.T) []byte {
	req, err := plugin.Request("describe", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestFindOnPath(t *testing.T) {
	want := installPlugin(t, outside(t), "checker", "good")
	got, err := New(t.TempDir(), nil).Find(plugin.KindChecker, "good")
	if err != nil {
		t.Fatal(err)
	}
	if !sameFile(t, got, want) {
		t.Errorf("Find = %s, want %s", got, want)
	}
}

func TestFindNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := New(t.TempDir(), nil).Find(plugin.KindChecker, "missing")
	if !errors.Is(err, plugin.ErrPluginNotFound) {
		t.Fatalf("Find = %v, want ErrPluginNotFound", err)
	}
}

func TestFindRefusesInsideProject(t *testing.T) {
	root := t.TempDir()
	installPlugin(t, filepath.Join(root, "bin"), "checker", "good")
	_, err := New(root, nil).Find(plugin.KindChecker, "good")
	if !errors.Is(err, plugin.ErrPluginRefused) || !strings.Contains(err.Error(), "inside the project") {
		t.Fatalf("Find = %v, want refused inside the project", err)
	}
}

func TestFindRefusesLinkIntoProject(t *testing.T) {
	root := t.TempDir()
	real := installPlugin(t, filepath.Join(root, "bin"), "checker", "good")
	out := outside(t)
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(out, filepath.Base(real))
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot make a link here: %v", err)
	}
	t.Setenv("PATH", out)
	_, err := New(root, nil).Find(plugin.KindChecker, "good")
	if !errors.Is(err, plugin.ErrPluginRefused) {
		t.Fatalf("Find = %v, want refused", err)
	}
}

func TestFindRefusesRelativePathEntry(t *testing.T) {
	dir := t.TempDir()
	installPlugin(t, filepath.Join(dir, "rel"), "checker", "good")
	t.Chdir(dir)
	t.Setenv("PATH", "rel")
	_, err := New(t.TempDir(), nil).Find(plugin.KindChecker, "good")
	if !errors.Is(err, plugin.ErrPluginRefused) {
		t.Fatalf("Find = %v, want refused", err)
	}
}

func TestFindRefusesBatchOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only: a .bat runs through cmd.exe")
	}
	dir := outside(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stele-checker-batch.bat"), []byte("@echo {}\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	_, err := New(t.TempDir(), nil).Find(plugin.KindChecker, "batch")
	if !errors.Is(err, plugin.ErrPluginRefused) || !strings.Contains(err.Error(), ".exe") {
		t.Fatalf("Find = %v, want refused, naming .exe", err)
	}
}

func TestFindRevalidatesName(t *testing.T) {
	for _, n := range []string{"../x", "a.b", "regex"} {
		if _, err := New(t.TempDir(), nil).Find(plugin.KindChecker, n); err == nil {
			t.Errorf("Find(%q) = nil error", n)
		}
	}
}

func TestRunWritesRequestReadsAnswer(t *testing.T) {
	bin := installPlugin(t, outside(t), "checker", "good")
	out, err := New(t.TempDir(), nil).Run(context.Background(), bin, describeReq(t), plugin.SmallAnswer, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var info plugin.Info
	if err := plugin.DecodeAnswer(out, &info); err != nil || info.Kind != plugin.KindChecker {
		t.Fatalf("answer %s: %v %+v", out, err, info)
	}
}

func TestRunInRootWithNoProjectPath(t *testing.T) {
	root := t.TempDir()
	bin := installPlugin(t, outside(t), "checker", "env")
	inside := filepath.Join(root, "tools")
	t.Setenv("PATH", inside+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := New(root, nil).Run(context.Background(), bin, describeReq(t), plugin.SmallAnswer, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var a struct{ Path, Cwd string }
	if err := json.Unmarshal(out, &a); err != nil {
		t.Fatal(err)
	}
	if !sameFile(t, a.Cwd, root) {
		t.Errorf("cwd = %s, want %s", a.Cwd, root)
	}
	for _, e := range filepath.SplitList(a.Path) {
		if strings.EqualFold(filepath.Clean(strings.Trim(e, `"`)), filepath.Clean(inside)) {
			t.Errorf("PATH holds %s, inside the project", e)
		}
	}
}

func TestRunNonZeroExitKeepsStderrTail(t *testing.T) {
	bin := installPlugin(t, outside(t), "checker", "crash")
	var seen bytes.Buffer
	_, err := New(t.TempDir(), &seen).Run(context.Background(), bin, describeReq(t), plugin.SmallAnswer, 30*time.Second)
	var ee *plugin.ExitError
	if !errors.As(err, &ee) || ee.Code != 3 || !strings.Contains(ee.Stderr, "the fake plugin crashed") {
		t.Fatalf("Run = %v, want exit 3 with stderr", err)
	}
	if !strings.Contains(seen.String(), "stele-checker-crash: the fake plugin crashed") {
		t.Errorf("stderr passed through as %q", seen.String())
	}
}

func TestRunTimeout(t *testing.T) {
	bin := installPlugin(t, outside(t), "checker", "slow")
	start := time.Now()
	_, err := New(t.TempDir(), nil).Run(context.Background(), bin, describeReq(t), plugin.SmallAnswer, time.Second)
	if !errors.Is(err, plugin.ErrTimeout) || !strings.Contains(err.Error(), "1s") {
		t.Fatalf("Run = %v, want ErrTimeout after 1s", err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("Run took %v", d)
	}
}

func TestRunTimeoutWithOrphanHoldingStdout(t *testing.T) {
	// The child outlives the test for a while and keeps its executable and
	// its working directory, the root, in use, which Windows will not
	// delete: not a t.TempDir, and a copy of the test binary, not a link.
	dir, err := os.MkdirTemp("", "stele-orphan-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	bin := copyPlugin(t, filepath.Join(dir, "bin"), "checker", "orphan")
	root := filepath.Join(dir, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	out, err := New(root, nil).Run(context.Background(), bin, describeReq(t), plugin.SmallAnswer, time.Second)
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("Run took %v: a child holding stdout kept it waiting", d)
	}
	if err == nil {
		var info plugin.Info
		if err := plugin.DecodeAnswer(out, &info); !errors.Is(err, plugin.ErrProtocol) {
			t.Errorf("no answer decodes as %v, want ErrProtocol", err)
		}
	}
}

func TestRunOutputOverCap(t *testing.T) {
	bin := installPlugin(t, outside(t), "checker", "huge")
	_, err := New(t.TempDir(), nil).Run(context.Background(), bin, describeReq(t), plugin.SmallAnswer, 30*time.Second)
	if !errors.Is(err, plugin.ErrProtocol) || !strings.Contains(err.Error(), "over") {
		t.Fatalf("Run = %v, want ErrProtocol over the cap", err)
	}
}

func TestRunStderrPrefixedCleanedCapped(t *testing.T) {
	bin := installPlugin(t, outside(t), "checker", "noisy")
	var seen bytes.Buffer
	if _, err := New(t.TempDir(), &seen).Run(context.Background(), bin, describeReq(t), plugin.SmallAnswer, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	s := seen.String()
	if strings.Contains(s, "\x1b") {
		t.Error("stderr kept an escape sequence")
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) != maxStderrLines+1 || !strings.HasPrefix(lines[0], "stele-checker-noisy: line 0") || !strings.Contains(lines[len(lines)-1], "more dropped") {
		t.Errorf("stderr: %d lines, first %q, last %q", len(lines), lines[0], lines[len(lines)-1])
	}
}

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	ia, err1 := os.Stat(a)
	ib, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ia, ib)
}
