//go:build windows

package claude

import (
	"os/exec"
)

// setProcAttrs sets Windows-specific process attributes.
// On Windows, we don't set process group attributes as Setpgid is Unix-only.
func setProcAttrs(cmd *exec.Cmd) {
	// No-op on Windows
}
