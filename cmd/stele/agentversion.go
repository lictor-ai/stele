package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/lictor-ai/stele/internal/adapter"
	"github.com/lictor-ai/stele/internal/toolpath"
)

// probeTimeout caps the version probe: init waits for nothing longer. Tests
// raise it: a fake probe can start slowly on a busy machine.
var probeTimeout = 5 * time.Second

// probeVersion runs agent a's version probe for stele init under root and
// returns what the adapter makes of it. The program is found on PATH, never
// inside the project (D43), and gets a PATH cut down the same way. stele
// hook never runs it: the hook starts no process (D45).
func probeVersion(a adapter.Adapter, root string) []string {
	argv := a.VersionProbe()
	if len(argv) == 0 {
		return nil
	}
	out, err := runProbe(argv, root)
	return a.VersionNotes(out, err)
}

// runProbe runs argv for the project at root and returns its output. It runs
// outside the project, as plugins and scanners do (D80): on Windows npm's
// codex.cmd runs through cmd.exe, which looks for the node it starts in the
// working directory before PATH.
func runProbe(argv []string, root string) ([]byte, error) {
	found, err := exec.LookPath(argv[0])
	if errors.Is(err, exec.ErrDot) {
		return nil, fmt.Errorf("%s: found through a relative PATH entry: %w", argv[0], toolpath.ErrRefused)
	}
	if err != nil {
		return nil, err
	}
	bin, err := toolpath.Vet(argv[0], found, root)
	if err != nil {
		return nil, err
	}
	dir, err := probeDir(root)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	cmd.Dir = dir
	cmd.Env = toolpath.Env(os.Environ(), root)
	// npm's codex.cmd starts node, which the timeout's kill of the shim does
	// not reach: once the shim is gone, stop waiting for its output.
	cmd.WaitDelay = time.Second
	return cmd.Output()
}

// probeDir is the directory the probe runs in: the temp directory, or the
// user's home directory, whichever is a directory outside the project at
// root.
func probeDir(root string) (string, error) {
	tries := []string{os.TempDir()}
	if home, err := os.UserHomeDir(); err == nil {
		tries = append(tries, home)
	}
	for _, d := range tries {
		abs, err := toolpath.Vet("working directory", d, root)
		if err != nil {
			continue
		}
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			return abs, nil
		}
	}
	return "", errors.New("no directory outside the project to run the version probe in")
}
