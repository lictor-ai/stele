//go:build !windows

package pluginexec

import (
	"os/exec"
	"syscall"
)

// stopGroup starts the plugin in its own process group, and stopping it
// stops the group: what it started goes too.
func stopGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
