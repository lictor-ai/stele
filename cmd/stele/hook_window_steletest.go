//go:build steletest

package main

import (
	"os"
	"time"
)

// windowEnv sets the batch window in a test build only: the functional
// tests run many hooks on a loaded machine, or step past a batch without
// waiting out the window. A release build has no such variable.
const windowEnv = "STELE_TEST_BATCH_WINDOW"

func init() {
	batchWindow = func() time.Duration { return parseWindow(os.Getenv(windowEnv)) }
}
