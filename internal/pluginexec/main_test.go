package pluginexec

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lictor-ai/stele/internal/plugin/plugintest"
)

// TestMain makes this test binary the fake plugin when it is started under a
// plugin's name (plugintest), and points the plugins' working directory at a
// temp directory. Two modes live only here, since they start a process:
// orphan answers, then exits, leaving a child holding stdout; orphanslow
// starts such a child and never answers. The child writes the file
// $STELE_FAKE_SLEEPER names after 3 s, unless it was stopped.
func TestMain(m *testing.M) {
	if marker := os.Getenv("STELE_FAKE_SLEEPER"); marker != "" {
		time.Sleep(3 * time.Second)
		_ = os.WriteFile(marker, []byte("alive"), 0o644)
		os.Exit(0)
	}
	if plugintest.Is(os.Args[0]) {
		switch strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") {
		case "stele-checker-orphan", "stele-checker-orphanslow":
			io.Copy(io.Discard, os.Stdin)
			c := exec.Command(os.Args[0])
			c.Env = append(os.Environ(), "STELE_FAKE_SLEEPER="+os.Getenv("STELE_FAKE_MARKER"))
			c.Stdout = os.Stdout
			_ = c.Start()
			if strings.HasSuffix(filepath.Base(os.Args[0]), "slow") || strings.HasSuffix(filepath.Base(os.Args[0]), "slow.exe") {
				time.Sleep(30 * time.Second)
			}
			json.NewEncoder(os.Stdout).Encode(map[string]any{"protocol": 1, "kind": "checker", "protocols": []int{1}, "version": "1"})
			os.Exit(0)
		}
		os.Exit(plugintest.Serve(os.Args[0], os.Stdin, os.Stdout, os.Stderr))
	}
	dir, err := os.MkdirTemp("", "stele-pluginexec-test")
	if err != nil {
		panic(err)
	}
	WorkBase = func() (string, error) { return dir, nil }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
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

// copyPlugin is installPlugin with a copy, never a link, and no change to
// PATH: for a plugin that starts a child, which would hold the test binary
// itself for as long as it runs.
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
