// SPDX-License-Identifier: MIT

package installa

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var queryFullProcessImageName = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")

// apertoWindows dice se c'è un processo il cui exe sta nella cartella del
// programma. Va chiesto a Windows: rinominare la cartella di un programma
// aperto riesce quasi sempre (provato il 14/10/2026), quindi non basta
// aspettare che la rinomina fallisca.
func apertoWindows(cartella string) bool {
	istantanea, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(istantanea)
	prefisso := strings.ToLower(filepath.Clean(cartella)) + `\`
	var p syscall.ProcessEntry32
	p.Size = uint32(unsafe.Sizeof(p))
	for err = syscall.Process32First(istantanea, &p); err == nil; err = syscall.Process32Next(istantanea, &p) {
		h, err := syscall.OpenProcess(0x1000, false, p.ProcessID) // PROCESS_QUERY_LIMITED_INFORMATION
		if err != nil {
			continue
		}
		exe := make([]uint16, 1024)
		n := uint32(len(exe))
		ok, _, _ := queryFullProcessImageName.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&exe[0])), uintptr(unsafe.Pointer(&n)))
		syscall.CloseHandle(h)
		if ok != 0 && strings.HasPrefix(strings.ToLower(syscall.UTF16ToString(exe[:n])), prefisso) {
			return true
		}
	}
	return false
}
