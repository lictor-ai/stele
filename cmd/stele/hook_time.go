package main

import (
	"time"
)

// started is when this hook call began, by the hook's clock (now), taken
// right after reading the event, before any lock wait: a batch of parallel
// writes is told by it, not by when each call got the session lock (#151).
// Zero outside runHook.
var started time.Time

// hookStart is when this hook call began, or now outside runHook.
func hookStart() time.Time {
	if started.IsZero() {
		return now()
	}
	return started
}

// startedWall is when this hook call began by the wall clock, which the
// agent's timeout runs on. Zero outside runHook.
var startedWall time.Time

const (
	// hookBudget is how long the hook may run before it must be done with
	// the session state: the agent adapters install it with a timeout of
	// 10 s (internal/adapter/claudecode, codex), past which the agent kills
	// it and the write goes through with no word. The rest is left for the
	// answer.
	hookBudget = 8500 * time.Millisecond
	// sessionWaitMax caps one wait for the session lock.
	sessionWaitMax = 5 * time.Second
)

// sessionWait is how long the hook may still wait for the session lock:
// compile.EnsureFresh may have waited for its own lock and rebuilt the index
// first, so the wait is what is left of hookBudget, at most sessionWaitMax.
// Past it the update fails, and block_on_error decides.
func sessionWait() time.Duration {
	if startedWall.IsZero() {
		return sessionWaitMax
	}
	return min(sessionWaitMax, hookBudget-time.Since(startedWall))
}

// batchWindow is the batch window the backstop uses: 0, backstop.Window. A
// test build (-tags steletest) sets it from the environment
// (hook_window_steletest.go); a release build never reads it.
var batchWindow = func() time.Duration { return 0 }

// parseWindow reads a batch window for a test build: a Go duration up to a
// minute, where 0 leaves only the chain of a batch. Anything else is 0,
// backstop.Window.
func parseWindow(v string) time.Duration {
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 || d > time.Minute {
		return 0
	}
	return max(d, time.Nanosecond)
}
