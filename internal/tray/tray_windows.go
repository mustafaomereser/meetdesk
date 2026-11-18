//go:build windows

// Package tray, uygulamanın bildirim alanı (saatin yanı) simgesidir.
package tray

import (
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	pShellNotify      = shell32.NewProc("Shell_NotifyIconW")
	pRegisterClass    = user32.NewProc("RegisterClassExW")
	pCreateWindow     = user32.NewProc("CreateWindowExW")
	pDefProc          = user32.NewProc("DefWindowProcW")
	pGetMsg           = user32.NewProc("GetMessageW")
	pTrans            = user32.NewProc("TranslateMessage")
	pDispatch         = user32.NewProc("DispatchMessageW")
	pPostMsg          = user32.NewProc("PostMessageW")
	pCreateMenu       = user32.NewProc("CreatePopupMenu")
	pAppendMenu       = user32.NewProc("AppendMenuW")
	pTrackMenu        = user32.NewProc("TrackPopupMenu")
	pDestroyMenu      = user32.NewProc("DestroyMenu")
	pGetCursorPos     = user32.NewProc("GetCursorPos")
	pSetForeground    = user32.NewProc("SetForegroundWindow")
	pRegisterMsg      = user32.NewProc("RegisterWindowMessageW")
	pIconRes          = user32.NewProc("CreateIconFromResourceEx")
	pGetSystemMetrics = user32.NewProc("GetSystemMetrics")
)

// Item, sağ tık menüsündeki bir satırdır. Label boşsa ayırıcıdır.
type Item struct {
	Label    string
	Checked  bool
	Disabled bool
	Bold     bool
	OnClick  func()
}

// Options, tepsi simgesinin ayarlarıdır.
type Options struct {
	Icon    []byte // PNG
	Tooltip string
	OnClick func()        // sol tık
	Menu    func() []Item // sağ tık (her açılışta yeniden üretilir)
}

type notifyIconData struct {
	Size            uint32
	Wnd             uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUID            windows.GUID
	BalloonIcon     uintptr
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	Private uint32
}

const (
	nimAdd, nimModify, nimDelete = 0, 1, 2
	nifMessage, nifIcon, nifTip  = 1, 2, 4
	nifInfo                      = 0x10
	wmTray                       = 0x8000 + 77
	wmLButtonUp                  = 0x0202
	wmRButtonUp                  = 0x0205
	wmContextMenu                = 0x007B
	wmAppQuit                    = 0x8000 + 78
)

var (
	mu           sync.Mutex
	hwnd         uintptr
	opts         Options
	nid          notifyIconData
	taskbarMsg   uintptr
	menuHandlers []func()
)

// Start, simgeyi kendi iş parçacığında başlatır. Başarısızsa false döner.
func Start(o Options) bool {
	ready := make(chan bool)
	go func() {
		runtime.LockOSThread()
		inst, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
		cls, _ := windows.UTF16PtrFromString("MeetdeskTray")
		wc := wndClassEx{WndProc: windows.NewCallback(proc), Instance: inst, ClassName: cls}
		wc.Size = uint32(unsafe.Sizeof(wc))
		pRegisterClass.Call(uintptr(unsafe.Pointer(&wc)))
		h, _, _ := pCreateWindow.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, 0, 0, inst, 0)
		if h == 0 {
			ready <- false
			return
		}
		tb, _ := windows.UTF16PtrFromString("TaskbarCreated")
		taskbarMsg, _, _ = pRegisterMsg.Call(uintptr(unsafe.Pointer(tb)))
		mu.Lock()
		hwnd, opts = h, o
		mu.Unlock()
		ok := add()
		ready <- ok
		if !ok {
			return
		}
		var m msg
		for {
			r, _, _ := pGetMsg.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				break
			}
			pTrans.Call(uintptr(unsafe.Pointer(&m)))
			pDispatch.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
	return <-ready
}

func add() bool {
	size, _, _ := pGetSystemMetrics.Call(49) // SM_CXSMICON
	var icon uintptr
	if len(opts.Icon) > 0 {
		icon, _, _ = pIconRes.Call(uintptr(unsafe.Pointer(&opts.Icon[0])), uintptr(len(opts.Icon)), 1, 0x00030000, size, size, 0)
	}
	nid = notifyIconData{Wnd: hwnd, ID: 1, Flags: nifMessage | nifIcon | nifTip, CallbackMessage: wmTray, Icon: icon}
	nid.Size = uint32(unsafe.Sizeof(nid))
	setTip(opts.Tooltip)
	r, _, _ := pShellNotify.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	return r != 0
}

func setTip(s string) {
	t, _ := windows.UTF16FromString(s)
	n := copy(nid.Tip[:len(nid.Tip)-1], t)
	nid.Tip[n] = 0
}

// SetTooltip, simgenin üzerine gelince görünen yazıyı değiştirir.
func SetTooltip(s string) {
	mu.Lock()
	defer mu.Unlock()
	if hwnd == 0 {
		return
	}
	nid.Flags = nifTip
	setTip(s)
	pShellNotify.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

// Notify, simgeden kısa bir bildirim (balon) gösterir.
func Notify(title, text string) {
	mu.Lock()
	defer mu.Unlock()
	if hwnd == 0 {
		return
	}
	n := nid
	n.Flags = nifInfo
	t, _ := windows.UTF16FromString(title)
	copy(n.InfoTitle[:len(n.InfoTitle)-1], t)
	x, _ := windows.UTF16FromString(text)
	copy(n.Info[:len(n.Info)-1], x)
	n.InfoFlags = 1 // NIIF_INFO
	pShellNotify.Call(nimModify, uintptr(unsafe.Pointer(&n)))
}

// Stop, simgeyi kaldırır.
func Stop() {
	mu.Lock()
	h := hwnd
	mu.Unlock()
	if h != 0 {
		pPostMsg.Call(h, wmAppQuit, 0, 0)
	}
}

func showMenu(h uintptr) {
	if opts.Menu == nil {
		return
	}
	items := opts.Menu()
	menu, _, _ := pCreateMenu.Call()
	defer pDestroyMenu.Call(menu)
	menuHandlers = menuHandlers[:0]
	for _, it := range items {
		if it.Label == "" {
			pAppendMenu.Call(menu, 0x800, 0, 0) // MF_SEPARATOR
			continue
		}
		menuHandlers = append(menuHandlers, it.OnClick)
		flags := uintptr(0)
		if it.Checked {
			flags |= 0x8 // MF_CHECKED
		}
		if it.Disabled {
			flags |= 0x3 // MF_GRAYED|MF_DISABLED
		}
		l, _ := windows.UTF16PtrFromString(it.Label)
		pAppendMenu.Call(menu, flags, uintptr(len(menuHandlers)), uintptr(unsafe.Pointer(l)))
	}
	var pt struct{ X, Y int32 }
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForeground.Call(h)                                                                          // menü dışına tıklayınca kapansın
	cmd, _, _ := pTrackMenu.Call(menu, 0x0100|0x0002|0x0020, uintptr(pt.X), uintptr(pt.Y), 0, h, 0) // RETURNCMD|RIGHTBUTTON|BOTTOMALIGN
	pPostMsg.Call(h, 0, 0, 0)
	if cmd > 0 && int(cmd) <= len(menuHandlers) && menuHandlers[cmd-1] != nil {
		go menuHandlers[cmd-1]()
	}
}

func proc(h, m, wp, lp uintptr) uintptr {
	switch {
	case m == wmTray:
		switch lp & 0xFFFF {
		case wmLButtonUp:
			if opts.OnClick != nil {
				go opts.OnClick()
			}
		case wmRButtonUp, wmContextMenu:
			showMenu(h)
		}
		return 0
	case m == wmAppQuit:
		mu.Lock()
		pShellNotify.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
		hwnd = 0
		mu.Unlock()
		windows.NewLazySystemDLL("user32.dll").NewProc("PostQuitMessage").Call(0)
		return 0
	case taskbarMsg != 0 && m == taskbarMsg:
		// Gezgin (explorer) yeniden başladı: simgeyi tekrar ekle.
		mu.Lock()
		add()
		mu.Unlock()
		return 0
	}
	r, _, _ := pDefProc.Call(h, m, wp, lp)
	return r
}
