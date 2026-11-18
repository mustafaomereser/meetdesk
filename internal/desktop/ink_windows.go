//go:build windows

package desktop

import (
	"math"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Ekrana çizim katmanı: tüm masaüstünü kaplayan, tıklamaları geçiren, her zaman üstte,
// saydam bir pencere. Ekran yakalamaya girmez (WDA_EXCLUDEFROMCAPTURE): izleyiciler
// çizgileri kendi ekranlarında zaten çiziyor, görüntüde ikinci kez görünmesin.
//
// Piksel başına saydamlık (UpdateLayeredWindow, önceden çarpılmış ARGB) ve GDI+ ile kenar
// yumuşatmalı çizim: neon (lazer) çizgiler izleyicilerdeki gibi parlar ve sönerek kaybolur.
// (Eski renk anahtarlı pencere yarı saydamlık çizemiyordu: neon düz görünüp birden kayboluyordu.)

var (
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	procSetWindowDisplayAffinity = user32.NewProc("SetWindowDisplayAffinity")
	procSetTimer                 = user32.NewProc("SetTimer")
	procKillTimer                = user32.NewProc("KillTimer")
	procExtCreatePen             = gdi32.NewProc("ExtCreatePen")
	procDeleteObject             = gdi32.NewProc("DeleteObject")
	procPolyline                 = gdi32.NewProc("Polyline")
	procCreateCompatibleDC       = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap   = gdi32.NewProc("CreateCompatibleBitmap")
	procDeleteDC                 = gdi32.NewProc("DeleteDC")
	procBitBlt                   = gdi32.NewProc("BitBlt")
	procEllipse                  = gdi32.NewProc("Ellipse")

	gdiplus                         = windows.NewLazySystemDLL("gdiplus.dll")
	procGdiplusStartup              = gdiplus.NewProc("GdiplusStartup")
	procGdipCreateBitmapFromScan0   = gdiplus.NewProc("GdipCreateBitmapFromScan0")
	procGdipGetImageGraphicsContext = gdiplus.NewProc("GdipGetImageGraphicsContext")
	procGdipSetSmoothingMode        = gdiplus.NewProc("GdipSetSmoothingMode")
	procGdipGraphicsClear           = gdiplus.NewProc("GdipGraphicsClear")
	procGdipCreatePen1              = gdiplus.NewProc("GdipCreatePen1")
	procGdipSetPenLineCap197819     = gdiplus.NewProc("GdipSetPenLineCap197819")
	procGdipSetPenLineJoin          = gdiplus.NewProc("GdipSetPenLineJoin")
	procGdipDrawLinesI              = gdiplus.NewProc("GdipDrawLinesI")
	procGdipCreateSolidFill         = gdiplus.NewProc("GdipCreateSolidFill")
	procGdipFillEllipseI            = gdiplus.NewProc("GdipFillEllipseI")
	procGdipDeletePen               = gdiplus.NewProc("GdipDeletePen")
	procGdipDeleteBrush             = gdiplus.NewProc("GdipDeleteBrush")
	procGdipDeleteGraphics          = gdiplus.NewProc("GdipDeleteGraphics")
	procGdipDisposeImage            = gdiplus.NewProc("GdipDisposeImage")
)

const (
	wmTimer      = 0x0113
	wmAppInkSync = 0x8010
	inkLaserLife = 1500 * time.Millisecond
	inkKey       = 0x00010203 // saydam renk (çizgilerde kullanılmayacak kadar özel)
)

type inkLine struct {
	laser bool
	color uint32 // BGR
	width int
	pts   []point // pencere koordinatı
	ended time.Time
	done  bool
	upd   time.Time // son nokta (bağlantısı kopan kişinin lazeri ekranda kalmasın)
}

var (
	inkOnce  sync.Once
	inkHwnd  uintptr
	inkMu    sync.Mutex
	inkLines = map[string]*inkLine{}
	inkOrder []string
	inkVis   bool
)

func ensureInk() bool {
	inkOnce.Do(func() {
		ready := make(chan struct{})
		go func() {
			runtime.LockOSThread()
			createInk()
			close(ready)
			if inkHwnd == 0 {
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
	return inkHwnd != 0
}

func createInk() {
	inst, _, _ := procGetModuleHandleW.Call(0)
	cls, _ := windows.UTF16PtrFromString("RemoteDeskInk")
	wc := wndClassEx{WndProc: windows.NewCallback(inkProc), Instance: inst, ClassName: cls}
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
	// RD_DEV_INK=1: test için çizim katmanı ekran görüntülerine de girer.
	if procSetWindowDisplayAffinity.Find() == nil && os.Getenv("RD_DEV_INK") != "1" {
		procSetWindowDisplayAffinity.Call(h, 0x11) // WDA_EXCLUDEFROMCAPTURE (Windows 10 2004+)
	}
	inkHwnd = h
	var tok uintptr
	in := struct {
		Version  uint32
		Callback uintptr
		NoThread int32
		NoCodecs int32
	}{Version: 1}
	if r, _, _ := procGdiplusStartup.Call(uintptr(unsafe.Pointer(&tok)), uintptr(unsafe.Pointer(&in)), 0); r == 0 {
		inkGdip = true
	}
}

// ---- çizim yüzeyi (yalnızca pencere goroutine'i) ----

var (
	inkGdip bool
	inkSurf struct {
		w, h      int32
		dc, bmp   uintptr
		old       uintptr
		gbmp, gfx uintptr
	}
)

func freeSurface() {
	sf := &inkSurf
	if sf.gfx != 0 {
		procGdipDeleteGraphics.Call(sf.gfx)
	}
	if sf.gbmp != 0 {
		procGdipDisposeImage.Call(sf.gbmp)
	}
	if sf.dc != 0 {
		procSelectObject.Call(sf.dc, sf.old)
		procDeleteObject.Call(sf.bmp)
		procDeleteDC.Call(sf.dc)
	}
	*sf = struct {
		w, h      int32
		dc, bmp   uintptr
		old       uintptr
		gbmp, gfx uintptr
	}{}
}

func ensureSurface(w, h int32) bool {
	sf := &inkSurf
	if sf.gfx != 0 && sf.w == w && sf.h == h {
		return true
	}
	freeSurface()
	screen, _, _ := procGetDC.Call(0)
	dc, _, _ := procCreateCompatibleDC.Call(screen)
	procReleaseDC.Call(0, screen)
	type bih struct {
		Size                    uint32
		W, H                    int32
		Planes, Bits            uint16
		Compression, SizeImage  uint32
		XPels, YPels, Used, Imp int32
	}
	bi := bih{W: w, H: -h, Planes: 1, Bits: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	var bits unsafe.Pointer
	bmp, _, _ := procCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		procDeleteDC.Call(dc)
		return false
	}
	sf.w, sf.h, sf.dc, sf.bmp = w, h, dc, bmp
	sf.old, _, _ = procSelectObject.Call(dc, bmp)
	const pixelFormat32bppPARGB = 0x000E200B
	if r, _, _ := procGdipCreateBitmapFromScan0.Call(uintptr(w), uintptr(h), uintptr(w*4), pixelFormat32bppPARGB, uintptr(bits), uintptr(unsafe.Pointer(&sf.gbmp))); r != 0 {
		freeSurface()
		return false
	}
	if r, _, _ := procGdipGetImageGraphicsContext.Call(sf.gbmp, uintptr(unsafe.Pointer(&sf.gfx))); r != 0 {
		freeSurface()
		return false
	}
	procGdipSetSmoothingMode.Call(sf.gfx, 4) // SmoothingModeAntiAlias
	return true
}

// argb, BGR rengi ve 0..1 opaklığı GDI+ ARGB'ye çevirir.
func argb(bgr uint32, a float64) uintptr {
	r, g, b := bgr&0xFF, bgr>>8&0xFF, bgr>>16&0xFF
	al := uint32(max(0, min(255, a*255)))
	return uintptr(al<<24 | r<<16 | g<<8 | b)
}

func f32(v float64) uintptr { return uintptr(math.Float32bits(float32(v))) }

// stroke, çizgiyi (ya da tek noktayı) verilen renk, opaklık ve kalınlıkla çizer.
func stroke(gfx uintptr, pts []point, color uintptr, width float64) {
	if len(pts) == 1 {
		var br uintptr
		procGdipCreateSolidFill.Call(color, uintptr(unsafe.Pointer(&br)))
		r := int32(max(1, width/2))
		p := pts[0]
		procGdipFillEllipseI.Call(gfx, br, uintptr(p.X-r), uintptr(p.Y-r), uintptr(2*r), uintptr(2*r))
		procGdipDeleteBrush.Call(br)
		return
	}
	var pen uintptr
	procGdipCreatePen1.Call(color, f32(width), 2 /* UnitPixel */, uintptr(unsafe.Pointer(&pen)))
	procGdipSetPenLineCap197819.Call(pen, 2, 2, 2) // yuvarlak uçlar
	procGdipSetPenLineJoin.Call(pen, 2)            // yuvarlak birleşim
	procGdipDrawLinesI.Call(gfx, pen, uintptr(unsafe.Pointer(&pts[0])), uintptr(len(pts)))
	procGdipDeletePen.Call(pen)
}

// renderInk, bütün çizgileri yüzeye çizer ve pencereye aktarır. Neon: renkli ışık halesi
// (genişten dara artan opaklık) + renkli çizgi + beyaza yakın ince öz; bitince söner.
func renderInk(hwnd uintptr) {
	x, y, w, h := virtualScreen()
	if !inkGdip || !ensureSurface(w, h) {
		return
	}
	gfx := inkSurf.gfx
	procGdipGraphicsClear.Call(gfx, 0)
	now := time.Now()
	inkMu.Lock()
	for _, id := range inkOrder {
		l := inkLines[id]
		if l == nil || len(l.pts) == 0 {
			continue
		}
		wd := float64(l.width)
		if !l.laser {
			stroke(gfx, l.pts, argb(l.color, 1), wd)
			continue
		}
		a := 1.0
		if l.done {
			a = max(0, 1-float64(now.Sub(l.ended))/float64(inkLaserLife))
		}
		for _, g := range [...]struct{ k, o float64 }{{5, .07}, {3.4, .12}, {2.2, .22}} {
			stroke(gfx, l.pts, argb(l.color, g.o*a), wd*g.k)
		}
		stroke(gfx, l.pts, argb(l.color, a), wd)
		stroke(gfx, l.pts, argb(mixWhite(l.color, .6), .85*a), max(1, wd*.4))
	}
	inkMu.Unlock()
	type pt struct{ X, Y int32 }
	type sz struct{ W, H int32 }
	dst, src, size := pt{x, y}, pt{0, 0}, sz{w, h}
	blend := [4]byte{0, 0, 255, 1} // AC_SRC_OVER, 0, 255, AC_SRC_ALPHA
	procUpdateLayeredWindow.Call(hwnd, 0, uintptr(unsafe.Pointer(&dst)), uintptr(unsafe.Pointer(&size)),
		inkSurf.dc, uintptr(unsafe.Pointer(&src)), 0, uintptr(unsafe.Pointer(&blend)), 2) // ULW_ALPHA
}

func mixWhite(bgr uint32, t float64) uint32 {
	m := func(c uint32) uint32 { return uint32(float64(c) + (255-float64(c))*t) }
	return m(bgr>>16&0xFF)<<16 | m(bgr>>8&0xFF)<<8 | m(bgr&0xFF)
}

func virtualScreen() (x, y, w, h int32) {
	m := func(i uintptr) int32 { r, _, _ := procGetSystemMetrics.Call(i); return int32(r) }
	return m(76), m(77), m(78), m(79) // SM_XVIRTUALSCREEN, SM_YVIRTUALSCREEN, SM_CXVIRTUALSCREEN, SM_CYVIRTUALSCREEN
}

func inkProc(hwnd, m, wp, lp uintptr) uintptr {
	switch m {
	case wmAppInkSync:
		inkMu.Lock()
		n := len(inkLines)
		laser := false
		for _, l := range inkLines {
			laser = laser || l.laser
		}
		inkMu.Unlock()
		if n == 0 {
			procShowWindow.Call(hwnd, 0)
			procKillTimer.Call(hwnd, 1)
			freeSurface() // tüm ekran boyutunda tampon: boşken bellekte tutulmaz
			inkMu.Lock()
			inkVis = false
			inkMu.Unlock()
			return 0
		}
		inkMu.Lock()
		show := !inkVis
		inkVis = true
		inkMu.Unlock()
		renderInk(hwnd)
		if show {
			x, y, w, h := virtualScreen()
			const hwndTopmost = ^uintptr(0)
			procSetWindowPos.Call(hwnd, hwndTopmost, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0x10|0x40)
		}
		if laser {
			procSetTimer.Call(hwnd, 1, 33, 0) // sönme animasyonu
		}
		return 0
	case wmTimer:
		// Lazer izleri: bırakıldıktan bir süre sonra silinir.
		now := time.Now()
		inkMu.Lock()
		changed, laser := false, false
		for id, l := range inkLines {
			if l.laser && ((l.done && now.Sub(l.ended) > inkLaserLife) || now.Sub(l.upd) > 5*time.Second) {
				delete(inkLines, id)
				inkOrder = removeID(inkOrder, id)
				changed = true
			} else if l.laser {
				laser = true
			}
		}
		inkMu.Unlock()
		if !laser {
			procKillTimer.Call(hwnd, 1)
		}
		if changed {
			procPostMessageW.Call(hwnd, wmAppInkSync, 0, 0)
		} else if laser {
			renderInk(hwnd) // sönen neon çizgiler
		}
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

func parseColor(c string) uint32 {
	if len(c) != 7 {
		return 0x003F4BEF
	}
	v, err := strconv.ParseUint(c[1:], 16, 32)
	if err != nil {
		return 0x003F4BEF
	}
	r, g, b := uint32(v>>16)&0xFF, uint32(v>>8)&0xFF, uint32(v)&0xFF
	bgr := b<<16 | g<<8 | r
	if bgr == inkKey {
		bgr++
	}
	return bgr
}

func inkAdd(id string, laser bool, color string, width int, dx, dy, dw, dh int, pts []int, end bool) {
	if !ensureInk() {
		return
	}
	vx, vy, _, _ := virtualScreen()
	inkMu.Lock()
	l := inkLines[id]
	if l == nil {
		// Kalınlık izleyicinin ekranına göre verilir (CSS pikseli); bu ekranda monitör
		// genişliğine oranla ölçeklenir.
		w := max(1, width*dw/1600)
		if laser {
			w = max(w, 3)
		}
		l = &inkLine{laser: laser, color: parseColor(color), width: w}
		inkLines[id] = l
		inkOrder = append(inkOrder, id)
	}
	for i := 0; i+1 < len(pts); i += 2 {
		x := int32(dx+pts[i]*dw/10000) - vx
		y := int32(dy+pts[i+1]*dh/10000) - vy
		l.pts = append(l.pts, point{x, y})
	}
	l.upd = time.Now()
	if end {
		l.done, l.ended = true, l.upd
	}
	inkMu.Unlock()
	procPostMessageW.Call(inkHwnd, wmAppInkSync, 0, 0)
}

func inkRemove(id string) {
	if inkHwnd == 0 {
		return
	}
	inkMu.Lock()
	delete(inkLines, id)
	inkOrder = removeID(inkOrder, id)
	inkMu.Unlock()
	procPostMessageW.Call(inkHwnd, wmAppInkSync, 0, 0)
}

func inkClear() {
	if inkHwnd == 0 {
		return
	}
	inkMu.Lock()
	inkLines = map[string]*inkLine{}
	inkOrder = nil
	inkMu.Unlock()
	procPostMessageW.Call(inkHwnd, wmAppInkSync, 0, 0)
}

func removeID(list []string, id string) []string {
	for i, v := range list {
		if v == id {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}
