//go:build windows

package desktop

import (
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Gizlilik modu: bu bilgisayarın ekranını tamamen kaplayan siyah bir perde. Perde ekran
// yakalamaya girmez (bağlanan kişi ekranı görmeye devam eder) ve tıklamaları geçirir
// (uzaktaki kişinin fare olayları alttaki pencerelere ulaşır). Bilgisayarın başındaki
// kişi Ctrl+Alt+Shift+P ile kapatabilir.

var (
	procRegisterHotKey   = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey = user32.NewProc("UnregisterHotKey")

	pvOnce   sync.Once
	pvHwnd   uintptr
	pvMu     sync.Mutex
	pvOn     bool
	pvOnExit func()
	pvFont   uintptr
	pvSmall  uintptr
)

const (
	wmAppPrivacy = 0x8040
	wmHotkey     = 0x0312
)

func ensurePrivacy() bool {
	pvOnce.Do(func() {
		ready := make(chan struct{})
		go func() {
			runtime.LockOSThread()
			inst, _, _ := procGetModuleHandleW.Call(0)
			cls, _ := windows.UTF16PtrFromString("MeetdeskPrivacy")
			wc := wndClassEx{WndProc: windows.NewCallback(privacyProc), Instance: inst, ClassName: cls}
			wc.Size = uint32(unsafe.Sizeof(wc))
			procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
			const exStyle = 0x00080000 | 0x00000020 | 0x00000008 | 0x00000080 | 0x08000000
			h, _, _ := procCreateWindowExW.Call(exStyle, uintptr(unsafe.Pointer(cls)), 0, 0x80000000, 0, 0, 10, 10, 0, 0, inst, 0)
			if h != 0 {
				procSetLayeredWindowAttributes.Call(h, 0, 255, 2) // LWA_ALPHA: tamamen opak
				if procSetWindowDisplayAffinity.Find() != nil {
					// Ekran yakalamadan hariç tutulamıyorsa perde açılmaz (bağlanan kişi de siyah görürdü).
					h = 0
				} else if r, _, _ := procSetWindowDisplayAffinity.Call(h, 0x11); r == 0 {
					h = 0
				}
				dpi := uintptr(96)
				if procGetDpiForSystem.Find() == nil {
					if d, _, _ := procGetDpiForSystem.Call(); d > 0 {
						dpi = d
					}
				}
				face, _ := windows.UTF16PtrFromString("Segoe UI")
				pvFont, _, _ = procCreateFontW.Call(uintptr(-int32(20*dpi/72)), 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(face)))
				pvSmall, _, _ = procCreateFontW.Call(uintptr(-int32(11*dpi/72)), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(face)))
			}
			pvHwnd = h
			close(ready)
			if h == 0 {
				return
			}
			var m msg
			for {
				r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
				if int32(r) <= 0 {
					return
				}
				procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
				procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
			}
		}()
		<-ready
	})
	return pvHwnd != 0
}

func privacyProc(hwnd, m, wp, lp uintptr) uintptr {
	switch m {
	case wmAppPrivacy:
		pvMu.Lock()
		on := pvOn
		pvMu.Unlock()
		if on {
			x, y, w, h := virtualScreen()
			const hwndTopmost = ^uintptr(0)
			procSetWindowPos.Call(hwnd, hwndTopmost, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0x10|0x40)
			procInvalidateRect.Call(hwnd, 0, 1)
			const modAlt, modCtrl, modShift, modNoRepeat = 1, 2, 4, 0x4000
			procRegisterHotKey.Call(hwnd, 1, modCtrl|modAlt|modShift|modNoRepeat, 'P')
			procSetTimer.Call(hwnd, 2, 1000, 0)
		} else {
			procKillTimer.Call(hwnd, 2)
			procUnregisterHotKey.Call(hwnd, 1)
			procShowWindow.Call(hwnd, 0)
		}
		return 0
	case wmTimer:
		// Başka bir "her zaman üstte" pencere (görev çubuğu vb.) öne geçmesin.
		const hwndTopmost = ^uintptr(0)
		procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, 0x0001|0x0002|0x0010)
		return 0
	case wmHotkey:
		pvMu.Lock()
		f := pvOnExit
		pvMu.Unlock()
		if f != nil {
			go f()
		}
		return 0
	case wmPaint:
		var ps paintStruct
		hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		var rc rect
		procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		black, _, _ := procCreateSolidBrush.Call(0x00131211)
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), black)
		procDeleteObject.Call(black)
		// Yazı: ana monitörün ortasında (sanal masaüstü koordinatından pencereye)
		vx, vy, _, _ := virtualScreen()
		m := func(i uintptr) int32 { r, _, _ := procGetSystemMetrics.Call(i); return int32(r) }
		pw, ph := m(0), m(1) // SM_CXSCREEN, SM_CYSCREEN (ana monitör)
		cx, cy := -vx+pw/2, -vy+ph/2
		procSetBkMode.Call(hdc, 1)
		draw := func(text string, font uintptr, color uint32, top, bottom int32) {
			t, _ := windows.UTF16FromString(text)
			r := rect{cx - pw/2, cy + top, cx + pw/2, cy + bottom}
			procSelectObject.Call(hdc, font)
			procSetTextColor.Call(hdc, uintptr(color))
			procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&t[0])), uintptr(len(t)-1), uintptr(unsafe.Pointer(&r)), 0x1|0x4|0x20)
		}
		draw("Bu bilgisayar uzaktan kullanılıyor", pvFont, 0x00F0EEED, -40, 0)
		draw("Gizlilik modu açık. Kapatmak için Ctrl+Alt+Shift+P", pvSmall, 0x00BAB4B0, 8, 40)
		procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

func setPrivacy(on bool, onExit func()) bool {
	if !on && pvHwnd == 0 {
		return true
	}
	if !ensurePrivacy() {
		return false
	}
	pvMu.Lock()
	pvOn, pvOnExit = on, onExit
	pvMu.Unlock()
	procPostMessageW.Call(pvHwnd, wmAppPrivacy, 0, 0)
	return true
}
