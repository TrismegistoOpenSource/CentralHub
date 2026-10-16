// SPDX-License-Identifier: MIT

//go:build !windows

package installa

import "os/exec"

func nascondi(*exec.Cmd) {}

func apertoWindows(string) bool { return false }
