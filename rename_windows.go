//go:build windows

package main

// renameNoReplace gives a copied file its final name, and never replaces a
// file that is already there. MoveFileEx without MOVEFILE_REPLACE_EXISTING
// fails when the destination exists, which is exactly what we want.

import (
	"syscall"
	"unsafe"
)

var moveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func renameNoReplace(old, new string) error {
	from, err := syscall.UTF16PtrFromString(old)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(new)
	if err != nil {
		return err
	}
	r1, _, e1 := moveFileExW.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), 0)
	if r1 == 0 {
		if e1 == nil || e1 == syscall.Errno(0) {
			return syscall.EEXIST
		}
		return e1
	}
	return nil
}
