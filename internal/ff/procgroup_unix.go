//go:build unix

package ff

import (
	"os/exec"
	"syscall"
	"time"
)

// configureProcessGroup puts the command in its own process group and makes
// cancellation kill the ENTIRE group.
//
// ffmpeg spawns helper threads and can itself spawn children; signalling only
// the direct child on timeout leaves those behind as zombies holding the temp
// directory open. WaitDelay bounds how long we wait for a well-behaved exit
// before the group is torn down.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
}
