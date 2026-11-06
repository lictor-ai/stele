package pluginexec

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/plugin/plugintest"
)

// TestMain makes this test binary the fake plugin when it is started under a
// plugin's name (plugintest). orphan, only here, starts a child that holds
// stdout open and sleeps, then exits without answering.
func TestMain(m *testing.M) {
	if os.Getenv("STELE_FAKE_SLEEPER") != "" {
		time.Sleep(20 * time.Second)
		os.Exit(0)
	}
	if plugintest.Is(os.Args[0]) {
		base := filepath.Base(os.Args[0])
		if base == "stele-checker-orphan" || base == "stele-checker-orphan.exe" {
			io.Copy(io.Discard, os.Stdin)
			c := exec.Command(os.Args[0])
			c.Env = append(os.Environ(), "STELE_FAKE_SLEEPER=1")
			c.Stdout = os.Stdout
			_ = c.Start()
			os.Exit(0)
		}
		os.Exit(plugintest.Serve(os.Args[0], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// installPlugin puts this test binary in dir as the plugin of kind with mode,
// and returns its path. dir goes first on PATH.
func installPlugin(t *testing.T, dir, kind, mode string) string {
	t.Helper()
	name := "stele-" + kind + "-" + mode
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dst := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(os.Args[0], dst); err != nil {
		writeCopy(t, dst)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dst
}

// copyPlugin is installPlugin with a copy, never a link: for a plugin that
// leaves a process running, which would hold the test binary itself.
func copyPlugin(t *testing.T, dir, kind, mode string) string {
	t.Helper()
	name := "stele-" + kind + "-" + mode
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, name)
	writeCopy(t, dst)
	return dst
}

func writeCopy(t *testing.T, dst string) {
	t.Helper()
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
}
