//go:build steletest

package main

import (
	"testing"
	"time"
)

// A test build reads the window from the environment.
func TestBatchWindowFromEnvInTestBuild(t *testing.T) {
	t.Setenv(windowEnv, "1m")
	if d := batchWindow(); d != time.Minute {
		t.Errorf("batchWindow = %v, want 1m", d)
	}
}
