//go:build windows

package desktop

import (
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Engellenen tıklama halkası: uzaktaki kişi tıklamaya çalıştı ama tıklama uygulanmadı
// (ana kullanıcı fareyi kullanıyor ya da yalnızca izleme izni var). O noktada küçük bir
// halka büyüyerek belirir ve ~0,6 sn'de söner. Tıklamaları geçiren, odak almayan, her
// zaman üstte, katmanlı küçük bir pencere; saydamlık (alfa) her karede azaltılır.

var (
	procGetStockObject = gdi32.NewProc("GetStockObject")

	rpOnce  sync.Once
	rpHwnd  uintptr
	rpMu    sync.Mutex
	rpStart time.Time
	rpScale float64
)

const (
	wmAppRipple = 0x8020
	rippleLife  = 650 * time.Millisecond
	rippleSize  = 44         // 96 DPI'da pencere boyu (px)
	rippleColor = 0x003F4BEF // #EF4B3F (BGR)
)

func ensureRipple() bool {
	rpOnce.Do(func() {
		ready := make(chan struct{})
		go func() {
			runtime.LockOSThread()
			createRipple()
			close(ready)
			if rpHwnd == 0 {
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
	return rpHwnd != 0
}

func createRipple() {
	inst, _, _ := procGetModuleHandleW.Call(0)
	cls, _ := windows.UTF16PtrFromString("RemoteDeskRipple")
	wc := wndClassEx{WndProc: windows.NewCallback(rippleProc), Instance: inst, ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	const (
		wsPopup         = 0x80000000
		wsExLayered     = 0x00080000
		wsExTransparent = 0x00000020
		wsExTopmost     = 0x00000008
		wsExToolWindow  = 0x00000080
		wsExNoActivate  = 0x08000000
	)
	h, _, _ := procCreateWindowExW.Call(
		wsExLayered|wsExTransparent|wsExTopmost|wsExToolWindow|wsExNoActivate,
		uintptr(unsafe.Pointer(cls)), 0, wsPopup, 0, 0, 10, 10, 0, 0, inst, 0)
	if h == 0 {
		return
	}
	dpi := uintptr(96)
	if procGetDpiForSystem.Find() == nil {
		if d, _, _ := procGetDpiForSystem.Call(); d > 0 {
			dpi = d
		}
	}
	rpScale = float64(dpi) / 96
	procSetLayeredWindowAttributes.Call(h, colorKey, 0, 1|2) // LWA_COLORKEY | LWA_ALPHA
	rpHwnd = h
}

func rippleProgress() float64 {
	rpMu.Lock()
	defer rpMu.Unlock()
	return float64(time.Since(rpStart)) / float64(rippleLife)
}

func rippleProc(hwnd, m, wp, lp uintptr) uintptr {
	switch m {
	case wmAppRipple:
		size := int32(rippleSize * rpScale)
		x, y := int32(int64(wp))-size/2, int32(int64(lp))-size/2
		rpMu.Lock()
		rpStart = time.Now()
		rpMu.Unlock()
		procSetLayeredWindowAttributes.Call(hwnd, colorKey, 230, 1|2)
		const hwndTopmost = ^uintptr(0)
		procSetWindowPos.Call(hwnd, hwndTopmost, uintptr(x), uintptr(y), uintptr(size), uintptr(size), 0x10|0x40) // NOACTIVATE|SHOWWINDOW
		procInvalidateRect.Call(hwnd, 0, 1)
		procSetTimer.Call(hwnd, 1, 16, 0)
		return 0
	case wmTimer:
		t := rippleProgress()
		if t >= 1 {
			procKillTimer.Call(hwnd, 1)
			procShowWindow.Call(hwnd, 0)
			return 0
		}
		procSetLayeredWindowAttributes.Call(hwnd, colorKey, uintptr(230*(1-t)), 1|2) // söner
		procInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case wmPaint:
		var ps paintStruct
		hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		var rc rect
		procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		key, _, _ := procCreateSolidBrush.Call(colorKey)
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), key)
		procDeleteObject.Call(key)
		// Halka: küçükten büyüğe açılır.
		t := min(rippleProgress(), 1)
		size := float64(rc.R)
		pw := max(2, int32(2.5*rpScale))
		r := int32((0.25 + 0.7*t) * (size/2 - float64(pw)))
		c := int32(size / 2)
		pen, _, _ := procCreatePen.Call(0, uintptr(pw), rippleColor)
		oldPen, _, _ := procSelectObject.Call(hdc, pen)
		oldBr, _, _ := procSelectObject.Call(hdc, func() uintptr { b, _, _ := procGetStockObject.Call(5); return b }()) // NULL_BRUSH
		procEllipse.Call(hdc, uintptr(c-r), uintptr(c-r), uintptr(c+r), uintptr(c+r))
		// Ortada küçük nokta: tıklanmak istenen yer
		dot := max(2, int32(3*rpScale))
		br, _, _ := procCreateSolidBrush.Call(rippleColor)
		procSelectObject.Call(hdc, br)
		procEllipse.Call(hdc, uintptr(c-dot), uintptr(c-dot), uintptr(c+dot), uintptr(c+dot))
		procSelectObject.Call(hdc, oldBr)
		procSelectObject.Call(hdc, oldPen)
		procDeleteObject.Call(br)
		procDeleteObject.Call(pen)
		procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

func clickRipple(x, y int) {
	if !ensureRipple() {
		return
	}
	procPostMessageW.Call(rpHwnd, wmAppRipple, uintptr(int64(x)), uintptr(int64(y)))
}
