//go:build !windows && !darwin

package main

// Unknown platform: the batch falls back to a fixed, conservative budget.

func systemMemory() memInfo { return memInfo{} }
