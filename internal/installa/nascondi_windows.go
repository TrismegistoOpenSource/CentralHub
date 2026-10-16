// SPDX-License-Identifier: MIT

package installa

import (
	"os/exec"
	"syscall"
)

// nascondi evita che un comando apra una finestra nera sopra l'app.
func nascondi(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
