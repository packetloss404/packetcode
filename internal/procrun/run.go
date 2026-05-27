package procrun

import (
	"os/exec"
	"time"
)

func ConfigureTreeCancel(cmd *exec.Cmd) {
	configurePlatform(cmd)
	cmd.Cancel = func() error {
		return KillTree(cmd)
	}
	cmd.WaitDelay = 250 * time.Millisecond
}
