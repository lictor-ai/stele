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

// probeTimeout caps the version probe: init waits for nothing longer.
const probeTimeout = 5 * time.Second

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

// runProbe runs argv from root and returns its output.
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
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	cmd.Dir = root
	cmd.Env = toolpath.Env(os.Environ(), root)
	return cmd.Output()
}
