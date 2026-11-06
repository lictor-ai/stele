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

// A copy inside the project is passed over, not run and not in the way: the
// real plugin later on PATH is found.
func TestFindPassesOverProjectCopy(t *testing.T) {
	root := t.TempDir()
	real := installPlugin(t, outside(t), "checker", "good")
	installPlugin(t, filepath.Join(root, "bin"), "checker", "good")
	got, err := New(root, nil).Find(plugin.KindChecker, "good")
	if err != nil || !sameFile(t, got, real) {
		t.Fatalf("Find = %s, %v; want %s", got, err, real)
	}
	t.Setenv("PATH", filepath.Join(root, "bin"))
	if _, err := New(root, nil).Find(plugin.KindChecker, "good"); !errors.Is(err, plugin.ErrPluginNotFound) {
		t.Fatalf("Find with only the project's copy = %v, want not found", err)
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

// A relative PATH entry, and on Windows the current directory, which
// exec.LookPath searches, are never looked in.
func TestFindSkipsRelativeAndCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	installPlugin(t, filepath.Join(dir, "rel"), "checker", "good")
	installPlugin(t, dir, "checker", "good")
	real := installPlugin(t, outside(t), "checker", "good")
	t.Chdir(dir)
	t.Setenv("PATH", "rel"+string(os.PathListSeparator)+filepath.Dir(real))
	got, err := New(t.TempDir(), nil).Find(plugin.KindChecker, "good")
	if err != nil || !sameFile(t, got, real) {
		t.Fatalf("Find = %s, %v; want %s", got, err, real)
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

// A plugin runs outside the project, where no DLL or program a clone
// brought is found first, with PATH and the loaders' paths cut (D80).
func TestRunOutsideTheProject(t *testing.T) {
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
	if rel, err := filepath.Rel(root, a.Cwd); err == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("cwd = %s, inside the project %s", a.Cwd, root)
	}
	for _, e := range filepath.SplitList(a.Path) {
		if strings.EqualFold(filepath.Clean(strings.Trim(e, `"`)), filepath.Clean(inside)) {
			t.Errorf("PATH holds %s, inside the project", e)
		}
	}
}

func TestRunCancelled(t *testing.T) {
	bin := installPlugin(t, outside(t), "checker", "slow")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(500*time.Millisecond, cancel)
	_, err := New(t.TempDir(), nil).Run(ctx, bin, describeReq(t), plugin.SmallAnswer, 30*time.Second)
	if !errors.Is(err, plugin.ErrCancelled) {
		t.Fatalf("Run = %v, want ErrCancelled", err)
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

// orphanRun runs mode, which starts a child holding stdout, and reports
// whether the child lived to write its marker.
func orphanRun(t *testing.T, mode string, timeout time.Duration) (err error, took time.Duration, survived bool) {
	t.Helper()
	dir := t.TempDir()
	bin := copyPlugin(t, filepath.Join(dir, "bin"), "checker", mode)
	marker := filepath.Join(dir, "alive")
	t.Setenv("STELE_FAKE_MARKER", marker)
	// Registered after t.TempDir, so it runs first: wait for the binary to
	// be free, should a child still hold it.
	t.Cleanup(func() {
		for i := 0; i < 50 && os.Remove(bin) != nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
	})
	start := time.Now()
	_, err = New(t.TempDir(), nil).Run(context.Background(), bin, describeReq(t), plugin.SmallAnswer, timeout)
	took = time.Since(start)
	time.Sleep(4 * time.Second) // past the child's 3 s
	_, statErr := os.Stat(marker)
	return err, took, statErr == nil
}

// A plugin that answers and exits but leaves a child holding stdout: the
// call fails, naming why, and the child is stopped.
func TestRunAnswerWithAHelperLeftBehind(t *testing.T) {
	err, took, survived := orphanRun(t, "orphan", 10*time.Second)
	if !errors.Is(err, plugin.ErrProtocol) || !strings.Contains(err.Error(), "left a process holding its output") {
		t.Errorf("Run = %v, want the helper named", err)
	}
	if took > 15*time.Second {
		t.Errorf("Run took %v", took)
	}
	if survived {
		t.Error("the helper outlived the call")
	}
}

// A plugin that times out with a child: both are stopped.
func TestRunTimeoutStopsTheChild(t *testing.T) {
	err, took, survived := orphanRun(t, "orphanslow", time.Second)
	if !errors.Is(err, plugin.ErrTimeout) {
		t.Errorf("Run = %v, want ErrTimeout", err)
	}
	if took > 15*time.Second {
		t.Errorf("Run took %v", took)
	}
	if survived {
		t.Error("the child outlived the timeout")
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
