//go:build windows

package desktop

import (
	"math"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// İşaret: bağlanan kişi ekranda bir noktayı gösterir. O kişinin renginde bir daire
// (kenarı tam renk, içi yarı saydam) hızlıca birkaç kez yanıp söner, sonra kaybolur.
// Her işaret, piksel başına saydamlığı olan (UpdateLayeredWindow) küçük bir penceredir;
// ekran yakalamaya girmez (izleyiciler işareti kendi ekranlarında çizer).

var (
	procUpdateLayeredWindow = user32.NewProc("UpdateLayeredWindow")
	procCreateDIBSection    = gdi32.NewProc("CreateDIBSection")

	pingOnce  sync.Once
	pingCtl   uintptr // mesajları alan görünmez pencere
	pingMu    sync.Mutex
	pingSlots []*pingSlot
	pingScale float64
)

const (
	wmAppPing = 0x8030
	pingLife  = 3 * time.Second
	pingBlink = 700 * time.Millisecond
	pingSize  = 64 // 96 DPI'da pencere boyu
)

type pingSlot struct {
	hwnd  uintptr
	dc    uintptr
	bmp   uintptr
	bits  []byte
	size  int32
	x, y  int32
	start time.Time
	live  bool
}

type pingReq struct {
	x, y  int32
	color uint32 // 0xRRGGBB
}

var pingQueue = make(chan pingReq, 16)

func ensurePing() bool {
	pingOnce.Do(func() {
		ready := make(chan struct{})
		go func() {
			runtime.LockOSThread()
			inst, _, _ := procGetModuleHandleW.Call(0)
			cls, _ := windows.UTF16PtrFromString("MeetdeskPing")
			wc := wndClassEx{WndProc: windows.NewCallback(pingProc), Instance: inst, ClassName: cls}
			wc.Size = uint32(unsafe.Sizeof(wc))
			procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
			// Kontrol penceresi: zamanlayıcı ve istekler (gizli, 0x0 boyutlu).
			h, _, _ := procCreateWindowExW.Call(0x00000080, uintptr(unsafe.Pointer(cls)), 0, 0x80000000, 0, 0, 0, 0, 0, 0, inst, 0)
			dpi := uintptr(96)
			if procGetDpiForSystem.Find() == nil {
				if d, _, _ := procGetDpiForSystem.Call(); d > 0 {
					dpi = d
				}
			}
			pingScale = float64(dpi) / 96
			pingCtl = h
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
	return pingCtl != 0
}

func newPingSlot(size int32) *pingSlot {
	inst, _, _ := procGetModuleHandleW.Call(0)
	cls, _ := windows.UTF16PtrFromString("MeetdeskPing")
	const exStyle = 0x00080000 | 0x00000020 | 0x00000008 | 0x00000080 | 0x08000000 // LAYERED|TRANSPARENT|TOPMOST|TOOLWINDOW|NOACTIVATE
	h, _, _ := procCreateWindowExW.Call(exStyle, uintptr(unsafe.Pointer(cls)), 0, 0x80000000, 0, 0, uintptr(size), uintptr(size), 0, 0, inst, 0)
	if h == 0 {
		return nil
	}
	if procSetWindowDisplayAffinity.Find() == nil {
		procSetWindowDisplayAffinity.Call(h, 0x11) // WDA_EXCLUDEFROMCAPTURE
	}
	screen, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, screen)
	dc, _, _ := procCreateCompatibleDC.Call(screen)
	type bih struct {
		Size                    uint32
		W, H                    int32
		Planes, Bits            uint16
		Compression, SizeImage  uint32
		XPels, YPels, Used, Imp int32
	}
	bi := bih{W: size, H: -size, Planes: 1, Bits: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	var p unsafe.Pointer
	bmp, _, _ := procCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&p)), 0, 0)
	if bmp == 0 || p == nil {
		procDeleteDC.Call(dc)
		return nil
	}
	procSelectObject.Call(dc, bmp)
	return &pingSlot{hwnd: h, dc: dc, bmp: bmp, bits: unsafe.Slice((*byte)(p), int(size*size*4)), size: size}
}

// render, daireyi (kenar + yarı saydam iç) kenar yumuşatmalı olarak çizer (önceden çarpılmış alfa).
func (p *pingSlot) render(color uint32) {
	r8, g8, b8 := float64(color>>16&0xFF), float64(color>>8&0xFF), float64(color&0xFF)
	n := float64(p.size)
	c := n / 2
	ring := 3 * pingScale
	rad := n/2 - ring/2 - 2*pingScale
	for y := 0; y < int(p.size); y++ {
		for x := 0; x < int(p.size); x++ {
			d := math.Hypot(float64(x)+0.5-c, float64(y)+0.5-c)
			fill := math.Max(0, math.Min(1, rad-d+0.5)) * 0.28
			edge := math.Max(0, math.Min(1, ring/2-math.Abs(d-rad)+0.5))
			a := edge + fill*(1-edge)
			i := (y*int(p.size) + x) * 4
			p.bits[i+0] = byte(b8 * a)
			p.bits[i+1] = byte(g8 * a)
			p.bits[i+2] = byte(r8 * a)
			p.bits[i+3] = byte(255 * a)
		}
	}
}

func (p *pingSlot) show(alpha byte) {
	type pt struct{ X, Y int32 }
	type sz struct{ W, H int32 }
	dst, src, size := pt{p.x, p.y}, pt{0, 0}, sz{p.size, p.size}
	blend := [4]byte{0, 0, alpha, 1} // AC_SRC_OVER, 0, SourceConstantAlpha, AC_SRC_ALPHA
	procUpdateLayeredWindow.Call(p.hwnd, 0, uintptr(unsafe.Pointer(&dst)), uintptr(unsafe.Pointer(&size)),
		p.dc, uintptr(unsafe.Pointer(&src)), 0, uintptr(unsafe.Pointer(&blend)), 2) // ULW_ALPHA
}

func pingProc(hwnd, m, wp, lp uintptr) uintptr {
	switch m {
	case wmAppPing:
		for {
			var req pingReq
			select {
			case req = <-pingQueue:
			default:
				return 0
			}
			pingMu.Lock()
			var slot *pingSlot
			for _, s := range pingSlots {
				if !s.live {
					slot = s
					break
				}
			}
			if slot == nil && len(pingSlots) < 8 {
				if slot = newPingSlot(int32(pingSize * pingScale)); slot != nil {
					pingSlots = append(pingSlots, slot)
				}
			}
			if slot == nil { // hepsi dolu: en eskisini yeniden kullan
				for _, s := range pingSlots {
					if slot == nil || s.start.Before(slot.start) {
						slot = s
					}
				}
			}
			pingMu.Unlock()
			if slot == nil {
				continue
			}
			slot.render(req.color)
			slot.x, slot.y = req.x-slot.size/2, req.y-slot.size/2
			slot.start, slot.live = time.Now(), true
			slot.show(255)
			const hwndTopmost = ^uintptr(0)
			procSetWindowPos.Call(slot.hwnd, hwndTopmost, 0, 0, 0, 0, 0x0001|0x0002|0x0010|0x0040) // NOSIZE|NOMOVE|NOACTIVATE|SHOWWINDOW
			procSetTimer.Call(pingCtl, 1, 16, 0)
		}
	case wmTimer:
		active := false
		for _, s := range pingSlots {
			if !s.live {
				continue
			}
			t := time.Since(s.start)
			switch {
			case t >= pingLife:
				s.live = false
				procShowWindow.Call(s.hwnd, 0)
				continue
			case t < pingBlink:
				// Hızlı yanıp sönme: ~110 ms açık / kapalı
				if (t/(110*time.Millisecond))%2 == 0 {
					s.show(255)
				} else {
					s.show(40)
				}
			case t > pingLife-600*time.Millisecond:
				s.show(byte(255 * float64(pingLife-t) / float64(600*time.Millisecond)))
			default:
				s.show(255)
			}
			active = true
		}
		if !active {
			procKillTimer.Call(pingCtl, 1)
		}
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

func ping(x, y int, color string) {
	if !ensurePing() {
		return
	}
	bgr := parseColor(color) // 0x00BBGGRR
	rgb := (bgr&0xFF)<<16 | (bgr>>8&0xFF)<<8 | (bgr >> 16 & 0xFF)
	select {
	case pingQueue <- pingReq{int32(x), int32(y), rgb}:
	default:
		return
	}
	procPostMessageW.Call(pingCtl, wmAppPing, 0, 0)
}
