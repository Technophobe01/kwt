//go:build windows

package process

import (
	"os"
	"os/exec"
	"strconv"
)

// ConfigureCommandCancellation uses taskkill's tree mode so filters and setup
// subprocesses do not outlive a canceled command on Windows.
func ConfigureCommandCancellation(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
