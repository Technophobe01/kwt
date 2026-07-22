//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package process

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigureCommandCancellationUsesProcessGroup(t *testing.T) {
	cmd := exec.Command("ignored")

	ConfigureCommandCancellation(cmd)

	require.NotNil(t, cmd.SysProcAttr)
	assert.True(t, cmd.SysProcAttr.Setpgid)
	assert.NotNil(t, cmd.Cancel)
}
