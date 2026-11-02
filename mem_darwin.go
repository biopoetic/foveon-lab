//go:build darwin

package main

// hw.memsize is the RAM the machine has. macOS has no cheap supported
// "available memory" number; the batch then uses half the total as its budget,
// which is the same conservative shape the other platforms use.

import (
	"encoding/binary"
	"syscall"
)

func systemMemory() memInfo {
	b, err := syscall.Sysctl("hw.memsize")
	if err != nil || len(b) < 8 {
		return memInfo{}
	}
	n := binary.LittleEndian.Uint64([]byte(b))
	if n == 0 {
		return memInfo{}
	}
	return memInfo{total: n, avail: n / 2}
}
