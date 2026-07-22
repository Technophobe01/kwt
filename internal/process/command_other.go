//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris && !windows

package process

import "os/exec"

// ConfigureCommandCancellation retains os/exec's platform default on systems
// without Unix process groups.
func ConfigureCommandCancellation(_ *exec.Cmd) {}
