package main

import "syscall"

// Make the console print UTF-8 so Croatian log text is not garbled.
func init() {
	syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleOutputCP").Call(65001)
}
