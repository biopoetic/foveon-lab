//go:build !windows

package spp

func requestClose(func(h uintptr) bool) {}
