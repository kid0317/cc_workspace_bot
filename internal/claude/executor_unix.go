//go:build unix

package claude

import (
	"os/exec"
	"syscall"
)

// setProcAttrs sets Unix-specific process attributes.
func setProcAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
