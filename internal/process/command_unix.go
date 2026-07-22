//go:build unix

package process

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ConfigureCommandCancellation places a command and its descendants in a
// separate process group so context cancellation terminates the entire tree.
func ConfigureCommandCancellation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
