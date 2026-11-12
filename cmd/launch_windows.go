//go:build windows

package cmd

import (
	"syscall"
	"unsafe"
)

// startedByDoubleClick reports whether ytd.exe was started from Explorer
// rather than from a terminal: Windows then creates a console just for this
// process, so the console has no other attached process (the shell).
func startedByDoubleClick() bool {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList")
	if proc.Find() != nil {
		return false
	}
	var ids [2]uint32
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	return n == 1
}
