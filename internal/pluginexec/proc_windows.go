package pluginexec

import "os/exec"

// stopGroup leaves the plugin as Go starts it: stopping it kills the plugin,
// and waitDelay stops a child it left holding its output from keeping stele
// waiting.
func stopGroup(cmd *exec.Cmd) {}
