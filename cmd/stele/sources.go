package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lictor-ai/stele/internal/gitexec"
	"github.com/lictor-ai/stele/internal/lock"
	"github.com/lictor-ai/stele/internal/plugin"
	"github.com/lictor-ai/stele/internal/pluginexec"
	"github.com/lictor-ai/stele/internal/source"
	"github.com/lictor-ai/stele/internal/source/gitrepo"
)

// This file is the one in cmd/stele that reaches git for the git source
// (ADR-0011) and starts external plugins (ADR-0013). runCompile, runCheck
// and init call sources; stele hook and stele stats never do:
// TestHookCallsNeverReachGit and TestStatsNeverReachesNetwork follow their
// calls.

// gitCacheDir is where the git source keeps its clones: the user's cache
// directory. Tests point it at a temp directory.
var gitCacheDir = os.UserCacheDir

// localRemotes, set only by tests, maps a repo in stele.json to a bare
// repository on disk. It is never a flag, an environment variable or a
// stele.json value.
var localRemotes map[string]string

// sources is every source compile reads under root: the builtins, then the
// sources stele.json lists: git sources, which treat their pins as mode
// says, and external ones, whose stderr goes to stderr.
func sources(root string, mode gitrepo.Mode, stderr io.Writer) ([]source.Source, error) {
	f := gitexec.NewFetcher(root)
	f.CacheDir = gitCacheDir
	f.Remotes = localRemotes
	return plugin.Sources(root, f, mode, pluginexec.New(root, stderr))
}

// interruptible is the context of a command that runs plugins: Ctrl-C, or a
// signal to stop, cancels it, which stops the plugin and what it started,
// and the call says it was cancelled (D80). The hook never uses it.
func interruptible() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// printPins says, for each pinned source, which commit it was read at, and
// whether that moved since prev.
func printPins(w io.Writer, prev, next []source.Pin, update bool) {
	before := make(map[string]source.Pin, len(prev))
	for _, p := range prev {
		before[p.Source] = p
	}
	for _, p := range next {
		label := strings.TrimPrefix(p.Source, gitrepo.Name+":")
		ref := p.Ref
		if ref == "" {
			ref = "default branch"
		}
		old, ok := before[p.Source]
		switch {
		case !ok:
			fmt.Fprintf(w, "%s: pinned %s at %s\n", label, ref, short(p.Commit))
		case old.Commit != p.Commit:
			fmt.Fprintf(w, "%s: %s %s -> %s\n", label, ref, short(old.Commit), short(p.Commit))
		case update:
			fmt.Fprintf(w, "%s: %s already at %s\n", label, ref, short(p.Commit))
		}
	}
}

// committedPins are the pins of stele.lock as it stands, or none.
func committedPins(root string) []source.Pin {
	f, err := lock.Load(root)
	if err != nil {
		return nil
	}
	return f.Sources
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
