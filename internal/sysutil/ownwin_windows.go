//go:build windows

package sysutil

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Meetdesk'in kendi pencereleri (ana pencere, oturum paneli) uzaktan kullanılamaz: bağlanan
// kişi şifreyi, ayarları ya da bağlantıyı değiştiremesin.

var (
	procWindowFromPoint       = user32.NewProc("WindowFromPoint")
	procGetAncestorOwn        = user32.NewProc("GetAncestor")
	procGetWindowThreadProcID = user32.NewProc("GetWindowThreadProcessId")
	procGetForegroundWindow   = user32.NewProc("GetForegroundWindow")
	ownPID                    = windows.GetCurrentProcessId()
)

func ownWindow(h uintptr) bool {
	if h == 0 {
		return false
	}
	if root, _, _ := procGetAncestorOwn.Call(h, 2 /* GA_ROOT */); root != 0 {
		h = root
	}
	var pid uint32
	procGetWindowThreadProcID.Call(h, uintptr(unsafe.Pointer(&pid)))
	return pid == ownPID
}

// OwnWindowAt, masaüstündeki noktada bu uygulamanın bir penceresi var mı.
func OwnWindowAt(x, y int) bool {
	h, _, _ := procWindowFromPoint.Call(uintptr(uint32(int32(x))) | uintptr(uint32(int32(y)))<<32)
	return ownWindow(h)
}

// OwnForeground, öndeki (klavyenin gittiği) pencere bu uygulamanın mı.
func OwnForeground() bool {
	h, _, _ := procGetForegroundWindow.Call()
	return ownWindow(h)
}
