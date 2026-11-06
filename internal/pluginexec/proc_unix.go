//go:build !windows

package pluginexec

import (
	"os/exec"
	"syscall"
)

// group starts the plugin in its own process group, and stopping it, at a
// timeout or a cancel, stops the group: what it started goes too.
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// contain returns what stops the rest of the plugin's process group once the
// plugin is done: nothing it started outlives the call.
func contain(cmd *exec.Cmd) func() {
	pid := cmd.Process.Pid
	return func() { _ = syscall.Kill(-pid, syscall.SIGKILL) }
}
