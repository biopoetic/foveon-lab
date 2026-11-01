package main

import (
	"errors"
	"io/fs"
	"strings"
	"syscall"
	"testing"
)

func TestPlainFileError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"unix disk full", syscall.ENOSPC, "no space left"},
		{"windows disk full", syscall.Errno(112), "no space left"},
		{"permission", fs.ErrPermission, "read-only"},
		{"other", errors.New("device removed"), "could not write"},
	}
	for _, c := range cases {
		got := plainFileError(c.err, `C:\photos\out.jpg`)
		if got == nil || !strings.Contains(strings.ToLower(got.Error()), c.want) {
			t.Errorf("%s: got %q, want it to mention %q", c.name, got, c.want)
		}
	}
	if plainFileError(nil, "x") != nil {
		t.Error("nil error must stay nil")
	}
}
