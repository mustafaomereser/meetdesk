//go:build windows

package capture

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Program/pencere gizleme: kurala uyan pencereler bağlananlara giden görüntüde karartılır.
// Bu bilgisayarın ekranında hiçbir şey değişmez. Kural bir program (exe adı: bütün
// pencereleri), bir başlık parçası (başlığında o geçen pencereler, ör. "WhatsApp") ya da
// ikisi birlikte olabilir. Kurala uyan pencerenin sahip olduğu açılır pencereler (menüler,
// iletişim kutuları) de gizlenir. Gizlenen pencerenin üstünde başka bir pencere varsa o
// kısım normal görünür.

const HideSupported = true

var (
	dwmapi = windows.NewLazySystemDLL("dwmapi.dll")

	procEnumWindows              = user32.NewProc("EnumWindows")
	procEnumChildWindows         = user32.NewProc("EnumChildWindows")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsIconic                 = user32.NewProc("IsIconic")
	procGetWindowRect            = user32.NewProc("GetWindowRect")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowLongPtrW        = user32.NewProc("GetWindowLongPtrW")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindow                = user32.NewProc("GetWindow")
	procGetAncestor              = user32.NewProc("GetAncestor")
	procDwmGetWindowAttribute    = dwmapi.NewProc("DwmGetWindowAttribute")
)

const (
	gwlExStyle        = -20
	wsExTopmost       = 0x8
	wsExTransparent   = 0x20
	wsExToolWindow    = 0x80
	wsExLayered       = 0x80000
	wsExNoActivate    = 0x8000000
	dwmaCloaked       = 14
	dwmaExtendedFrame = 9
	gwOwner           = 4
	gaRootOwner       = 3
)

var (
	hideMu    sync.Mutex
	hideRules []HideRule
	hideAny   atomic.Bool
	hideOwn   atomic.Bool // bu uygulamanın kendi pencereleri (şifre, ayarlar) her zaman gizli
	ownPID    = windows.GetCurrentProcessId()
	exeCache  = map[uint32]string{}
	exeAt     time.Time
)

// SetHideOwn, bu uygulamanın kendi pencerelerinin (ana pencere, oturum paneli) bağlananlara
// karartılmış gitmesini açar. Saydam bindirme pencereleri (çizim, işaret, ad etiketi) hariç.
func SetHideOwn(on bool) { hideOwn.Store(on) }

// HideRule: Exe boş değilse yalnızca o program; Title boş değilse başlığında o geçen
// pencereler (büyük/küçük harf fark etmez).
type HideRule struct {
	Exe   string
	Title string
}

// SetHidden, gizleme kurallarını ayarlar.
func SetHidden(rules []HideRule) {
	hideMu.Lock()
	defer hideMu.Unlock()
	hideRules = hideRules[:0:0]
	for _, r := range rules {
		r.Exe = strings.ToLower(strings.TrimSpace(r.Exe))
		r.Title = strings.ToLower(strings.TrimSpace(r.Title))
		if r.Exe != "" || r.Title != "" {
			hideRules = append(hideRules, r)
		}
	}
	hideAny.Store(len(hideRules) > 0)
}

// ownWindow, pencere bu uygulamanın normal (bindirme olmayan) penceresi mi.
func ownWindow(hwnd uintptr) bool {
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid == ownPID && exStyle(hwnd)&(wsExLayered|wsExTransparent) == 0
}

func windowTitle(hwnd uintptr) string {
	var buf [256]uint16
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

// matchLocked, pencere bir kurala uyuyor mu. hideMu tutulurken çağrılır.
func matchLocked(hwnd uintptr) bool {
	e, pid := appExe(hwnd)
	if pid == ownPID {
		return false
	}
	title, loaded := "", false
	for _, r := range hideRules {
		if r.Exe != "" && r.Exe != e {
			continue
		}
		if r.Title == "" {
			return true
		}
		if !loaded {
			title, loaded = strings.ToLower(windowTitle(hwnd)), true
		}
		if strings.Contains(title, r.Title) {
			return true
		}
	}
	return false
}

// CleanTitle, başlıktaki okunmamış sayısını ("(3) WhatsApp") atar.
func CleanTitle(t string) string {
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "(") {
		if i := strings.Index(t, ") "); i > 1 && i < 8 {
			t = strings.TrimSpace(t[i+2:])
		}
	}
	return t
}

// RunningApp, ekrandaki bir penceredir (gizleme listesinde seçmek için).
type RunningApp struct {
	Exe   string `json:"exe"`
	Title string `json:"title"`
}

type rect struct{ x0, y0, x1, y1 int }

func (r rect) empty() bool { return r.x1 <= r.x0 || r.y1 <= r.y0 }

func (r rect) and(o rect) rect {
	return rect{max(r.x0, o.x0), max(r.y0, o.y0), min(r.x1, o.x1), min(r.y1, o.y1)}
}

// minus, r'den o'yu çıkarır (en çok 4 parça).
func (r rect) minus(o rect, out []rect) []rect {
	i := r.and(o)
	if i.empty() {
		return append(out, r)
	}
	if r.y0 < i.y0 {
		out = append(out, rect{r.x0, r.y0, r.x1, i.y0})
	}
	if i.y1 < r.y1 {
		out = append(out, rect{r.x0, i.y1, r.x1, r.y1})
	}
	if r.x0 < i.x0 {
		out = append(out, rect{r.x0, i.y0, i.x0, i.y1})
	}
	if i.x1 < r.x1 {
		out = append(out, rect{i.x1, i.y0, r.x1, i.y1})
	}
	return out
}

func sameRects(a, b []rect) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// exeOf, pencerenin programının küçük harf exe adını döner ("" bilinmiyorsa).
// hideMu tutulurken çağrılır.
func exeOf(hwnd uintptr) (string, uint32) {
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return "", 0
	}
	if time.Since(exeAt) > 30*time.Second { // PID'ler yeniden kullanılabilir
		clear(exeCache)
		exeAt = time.Now()
	}
	if e, ok := exeCache[pid]; ok {
		return e, pid
	}
	e := ""
	if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid); err == nil {
		buf := make([]uint16, 1024)
		n := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
			e = strings.ToLower(filepath.Base(windows.UTF16ToString(buf[:n])))
		}
		windows.CloseHandle(h)
	}
	exeCache[pid] = e
	return e, pid
}

// Mağaza (UWP) uygulamalarının penceresi ApplicationFrameHost'a aittir; asıl program
// içindeki alt pencerededir.
var (
	childPID  uint32
	childHost uint32
	childCB   = windows.NewCallback(func(hwnd, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid != 0 && pid != childHost {
			childPID = pid
			return 0
		}
		return 1
	})
)

func appExe(hwnd uintptr) (string, uint32) {
	e, pid := exeOf(hwnd)
	if e != "applicationframehost.exe" {
		return e, pid
	}
	childPID, childHost = 0, pid
	procEnumChildWindows.Call(hwnd, childCB, 0)
	if childPID == 0 {
		return e, pid
	}
	var sub string
	if s, ok := exeCache[childPID]; ok {
		sub = s
	} else if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, childPID); err == nil {
		buf := make([]uint16, 1024)
		n := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
			sub = strings.ToLower(filepath.Base(windows.UTF16ToString(buf[:n])))
		}
		windows.CloseHandle(h)
		exeCache[childPID] = sub
	}
	if sub == "" {
		return e, pid
	}
	return sub, childPID
}

func cloaked(hwnd uintptr) bool {
	var v uint32
	r, _, _ := procDwmGetWindowAttribute.Call(hwnd, dwmaCloaked, uintptr(unsafe.Pointer(&v)), 4)
	return r == 0 && v != 0
}

func winRect(hwnd uintptr) rect {
	var r rectW
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return rect{int(r.Left), int(r.Top), int(r.Right), int(r.Bottom)}
}

// frameRect, pencerenin görünen sınırları (görünmez yeniden boyutlandırma kenarı hariç).
func frameRect(hwnd uintptr) rect {
	var r rectW
	if res, _, _ := procDwmGetWindowAttribute.Call(hwnd, dwmaExtendedFrame, uintptr(unsafe.Pointer(&r)), unsafe.Sizeof(r)); res == 0 {
		return rect{int(r.Left), int(r.Top), int(r.Right), int(r.Bottom)}
	}
	return winRect(hwnd)
}

func exStyle(hwnd uintptr) uintptr {
	idx := gwlExStyle
	v, _, _ := procGetWindowLongPtrW.Call(hwnd, uintptr(idx))
	return v
}

// ---- pencereleri z sırasıyla (üstten alta) gez ----

var (
	walkFn func(hwnd uintptr) bool
	walkCB = windows.NewCallback(func(hwnd, _ uintptr) uintptr {
		if walkFn(hwnd) {
			return 1
		}
		return 0
	})
)

// Animasyonlar: Windows küçültme/geri açma, büyütme ve kapanma animasyonlarını pencerenin
// görüntüsünden ayrıca çizer; pencere o sırada "küçültülmüş" ya da yok görünür. Bu yüzden
// gizlenen pencerelerin durumu izlenir ve geçişlerde fazladan karartılır:
//   - küçültme / geri açma: pencerenin bulunduğu monitörün tamamı kısa süre
//   - büyütme / yerleştirme (konum sıçraması): eski ve yeni alanın birleşimi kısa süre
//   - kapanma / gizlenme: son alanı kısa süre
// Görev çubuğu önizlemesi ve Alt+Tab ekranı gizlenen pencerenin küçük görüntüsünü de
// gösterir: gizlenen bir pencere varken bunlar açıksa monitör karartılır.
const (
	animIconic = 700 * time.Millisecond
	animMove   = 450 * time.Millisecond
	animGone   = 450 * time.Millisecond
)

type winState struct {
	rect     rect
	iconic   bool
	trans    time.Time // küçültme / geri açma başladı
	anim     rect      // konum sıçramasında eski ∪ yeni
	animAt   time.Time
	lastSeen time.Time
}

var (
	winStates = map[uintptr]*winState{}
	procClass = user32.NewProc("GetClassNameW")
)

// switcher, görev çubuğu önizlemesi / Alt+Tab / görev görünümü penceresi mi.
func switcher(hwnd uintptr) bool {
	var buf [64]uint16
	n, _, _ := procClass.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	switch windows.UTF16ToString(buf[:n]) {
	case "TaskListThumbnailWnd", "XamlExplorerHostIslandWindow", "MultitaskingViewFrame", "TaskSwitcherWnd", "TaskSwitcherOverlayWnd":
		return true
	}
	return false
}

func union(a, b rect) rect {
	return rect{min(a.x0, b.x0), min(a.y0, b.y0), max(a.x1, b.x1), max(a.y1, b.y1)}
}

// hiddenRects, d monitöründe karartılacak alanları (kareye göre koordinatlarla) döner.
func hiddenRects(d Display) []rect {
	if !hideAny.Load() && !hideOwn.Load() {
		return nil
	}
	hideMu.Lock()
	defer hideMu.Unlock()
	disp := rect{d.X, d.Y, d.X + d.W, d.Y + d.H}
	now := time.Now()
	hiddenOpen := len(winStates) > 0 // gizlenen bir pencere var (önizlemesi de gizlenmeli)
	full := false
	var occ, out []rect
	walkFn = func(hwnd uintptr) bool {
		if v, _, _ := procIsWindowVisible.Call(hwnd); v == 0 {
			return true
		}
		if cloaked(hwnd) {
			return true
		}
		ic, _, _ := procIsIconic.Call(hwnd)
		iconic := ic != 0
		hit := hideOwn.Load() && ownWindow(hwnd) || matchLocked(hwnd)
		if !hit {
			if o, _, _ := procGetAncestor.Call(hwnd, gaRootOwner); o != 0 && o != hwnd {
				hit = matchLocked(o)
			}
		}
		if hit {
			st := winStates[hwnd]
			r := winRect(hwnd)
			if st == nil {
				st = &winState{rect: r, iconic: iconic}
				winStates[hwnd] = st
			}
			st.lastSeen = now
			if iconic != st.iconic {
				st.trans = now // küçültme ya da geri açma animasyonu başladı
			}
			if !iconic && !st.iconic && r != st.rect {
				st.anim, st.animAt = union(st.rect, r), now
			}
			if !iconic {
				st.rect = r
			}
			st.iconic = iconic
			if now.Sub(st.trans) < animIconic && !st.rect.and(disp).empty() {
				full = true
			}
			if now.Sub(st.animAt) < animMove {
				if a := st.anim.and(disp); !a.empty() {
					out = append(out, a)
				}
			}
			if iconic {
				return true
			}
			// Gölge/kenar dahil bütün pencere alanı; üstteki pencerelerin kapattığı kısım çıkarılır.
			r = r.and(disp)
			if r.empty() {
				return true
			}
			pieces := []rect{r}
			for _, o := range occ {
				var next []rect
				for _, p := range pieces {
					next = p.minus(o, next)
				}
				pieces = next
				if len(pieces) == 0 || len(pieces) > 64 {
					break
				}
			}
			if len(pieces) > 64 {
				pieces = []rect{r} // çok parçalı: fazlasını karartmak, sızdırmaktan iyidir
			}
			out = append(out, pieces...)
			return true
		}
		if iconic {
			return true
		}
		if hiddenOpen && switcher(hwnd) {
			if !frameRect(hwnd).and(disp).empty() {
				full = true // önizleme / Alt+Tab gizlenen pencerenin küçük görüntüsünü gösterebilir
			}
			return true
		}
		// Yalnızca opak, normal pencereler alttakini örter. Saydam/katmanlı pencereler
		// (bindirmeler, çizim katmanları) örtmüyor sayılır: gizlenen pencere görünmeye başlamasın.
		if ex := exStyle(hwnd); ex&(wsExLayered|wsExTransparent) == 0 {
			if r := frameRect(hwnd).and(disp); !r.empty() {
				occ = append(occ, r)
			}
		}
		return true
	}
	procEnumWindows.Call(walkCB, 0)
	walkFn = nil
	// Kapanan / gizlenen pencereler: kapanma animasyonu bitene kadar son alanı.
	for h, st := range winStates {
		if st.lastSeen.Equal(now) {
			continue
		}
		if now.Sub(st.lastSeen) < animGone {
			if !st.iconic {
				if a := st.rect.and(disp); !a.empty() {
					out = append(out, a)
				}
			}
			continue
		}
		delete(winStates, h)
	}
	if full {
		out = []rect{disp}
	}
	for i := range out {
		out[i].x0 -= d.X
		out[i].x1 -= d.X
		out[i].y0 -= d.Y
		out[i].y1 -= d.Y
	}
	return out
}

// applyMask, alanları çizgili koyu bir desenle boyar.
func applyMask(f *Frame, rs []rect) {
	for _, r := range rs {
		r = r.and(rect{0, 0, f.W, f.H})
		for y := r.y0; y < r.y1; y++ {
			row := f.Pix[y*f.Stride:]
			for x := r.x0; x < r.x1; x++ {
				c := byte(0x1f)
				if (x+y)/14%2 == 0 {
					c = 0x27
				}
				i := x * 4
				row[i], row[i+1], row[i+2], row[i+3] = c, c, c+2, 255
			}
		}
	}
}

// Apps, ekrandaki pencereleri (program + başlık) döner (bu program hariç).
func Apps() []RunningApp {
	hideMu.Lock()
	defer hideMu.Unlock()
	seen := map[string]bool{}
	var out []RunningApp
	walkFn = func(hwnd uintptr) bool {
		if v, _, _ := procIsWindowVisible.Call(hwnd); v == 0 || cloaked(hwnd) {
			return true
		}
		if o, _, _ := procGetWindow.Call(hwnd, gwOwner); o != 0 {
			return true
		}
		if exStyle(hwnd)&(wsExToolWindow|wsExNoActivate) != 0 {
			return true
		}
		title := CleanTitle(windowTitle(hwnd))
		if title == "" {
			return true
		}
		e, pid := appExe(hwnd)
		k := e + "|" + title
		if e == "" || pid == ownPID || seen[k] || e == "explorer.exe" && title == "Program Manager" {
			return true
		}
		seen[k] = true
		out = append(out, RunningApp{Exe: e, Title: title})
		return true
	}
	procEnumWindows.Call(walkCB, 0)
	walkFn = nil
	return out
}
