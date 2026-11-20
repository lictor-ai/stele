//go:build !steletest

package main

import "testing"

// A release build never reads the test window from the environment.
func TestBatchWindowIgnoresEnvInRelease(t *testing.T) {
	t.Setenv("STELE_TEST_BATCH_WINDOW", "1m")
	if d := batchWindow(); d != 0 {
		t.Errorf("batchWindow = %v in a release build, want 0 (backstop.Window)", d)
	}
}
