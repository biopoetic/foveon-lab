//go:build windows

package main

// GlobalMemoryStatusEx gives total and currently available physical memory
// without cgo. ullAvailPhys is what another program on the machine has left.

import (
	"syscall"
	"unsafe"
)

type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

var globalMemoryStatusEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

func systemMemory() memInfo {
	var ms memoryStatusEx
	ms.length = uint32(unsafe.Sizeof(ms))
	r, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	if r == 0 {
		return memInfo{}
	}
	return memInfo{total: ms.totalPhys, avail: ms.availPhys}
}
