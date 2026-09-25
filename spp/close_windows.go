//go:build windows

package spp

import (
	"encoding/csv"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

var (
	user32                       = syscall.NewLazyDLL("user32.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetWindow                = user32.NewProc("GetWindow")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procPostMessageW             = user32.NewProc("PostMessageW")
)

const (
	wmClose = 0x0010
	gwOwner = 4
)

type sppWindow struct {
	h      uintptr
	dialog bool // a message box / owned window: the user must answer it
}

// Windows allows a limited number of callbacks per process, so one is
// created once and fed its target through package state.
var (
	enumMu     sync.Mutex
	enumPIDs   map[uint32]bool
	enumResult []sppWindow
	enumCB     = syscall.NewCallback(func(h, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&pid)))
		if !enumPIDs[pid] {
			return 1
		}
		if v, _, _ := procIsWindowVisible.Call(h); v == 0 {
			return 1
		}
		owner, _, _ := procGetWindow.Call(h, gwOwner)
		buf := make([]uint16, 256)
		procGetClassNameW.Call(h, uintptr(unsafe.Pointer(&buf[0])), 256)
		enumResult = append(enumResult, sppWindow{h: h, dialog: owner != 0 || syscall.UTF16ToString(buf) == "#32770"})
		return 1
	})
)

// windowsOf lists the visible top-level windows of the given processes.
func windowsOf(pids map[uint32]bool) []sppWindow {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumPIDs, enumResult = pids, nil
	procEnumWindows.Call(enumCB, 0)
	out := enumResult
	enumPIDs, enumResult = nil, nil
	return out
}

// sppPIDs returns the process ids of running SPP instances.
func sppPIDs() map[uint32]bool {
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+exeName, "/FO", "CSV", "/NH").Output()
	pids := map[uint32]bool{}
	if err != nil {
		return pids
	}
	recs, _ := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	for _, r := range recs {
		if len(r) > 1 && strings.EqualFold(r[0], exeName) {
			if n, err := strconv.ParseUint(r[1], 10, 32); err == nil {
				pids[uint32(n)] = true
			}
		}
	}
	return pids
}

// requestClose posts WM_CLOSE to SPP's windows — never while a dialog is
// showing, so SPP's own "save changes?" prompts are left for the user.
// resend reports whether a window may be asked again.
func requestClose(resend func(h uintptr) bool) {
	pids := sppPIDs()
	if len(pids) == 0 {
		return
	}
	ws := windowsOf(pids)
	for _, w := range ws {
		if w.dialog {
			return
		}
	}
	for _, w := range ws {
		if resend(w.h) {
			procPostMessageW.Call(w.h, wmClose, 0, 0)
		}
	}
}
