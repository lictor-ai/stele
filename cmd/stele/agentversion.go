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
// from a fresh folder outside the project, like plugins and scanners (D80).
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
	// A program the probe started may still hold the folder: best effort.
	defer os.RemoveAll(dir)
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

// probeDir makes the empty folder the probe runs in, under the temp
// directory and outside the project at root. The caller removes it.
func probeDir(root string) (string, error) {
	dir, err := os.MkdirTemp("", "stele-probe-")
	if err != nil {
		return "", fmt.Errorf("no folder to run the version probe in: %w", err)
	}
	abs, err := toolpath.Vet("working directory", dir, root)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return abs, nil
}
