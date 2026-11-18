//go:build windows

package app

import (
	"log"
	"path/filepath"
	"sync"

	"remotedesk/internal/frame"
	"remotedesk/web"
)

func init() {
	frame.Icon, _ = web.FS.ReadFile("icon.png")
}

var (
	nativeMu sync.Mutex
	mainWin  frame.Window
	panelWin frame.Window
)

// openNativeWindow, arayüzü uygulamanın kendi çerçevesiz penceresinde (WebView2) açar.
// WebView2 çalışma zamanı yoksa false döner (çağıran tarayıcıya düşer).
func openNativeWindow(url, dataDir string, onClose func(), minimized bool) bool {
	w, err := frame.Open(frame.Options{
		URL: url, Title: AppName, W: 1100, H: 740, MinW: 380, MinH: 520,
		DataDir: filepath.Join(dataDir, "webview"), Minimized: minimized,
		OnClose: func() {
			nativeMu.Lock()
			mainWin = nil
			nativeMu.Unlock()
			onClose()
		},
	})
	if err != nil {
		log.Printf("uygulama penceresi açılamadı: %v", err)
		return false
	}
	nativeMu.Lock()
	mainWin = w
	nativeMu.Unlock()
	return true
}

// focusNativeMain, açık ana pencereyi öne getirir; pencere yoksa false.
func focusNativeMain() bool {
	nativeMu.Lock()
	w := mainWin
	nativeMu.Unlock()
	if w == nil {
		return false
	}
	w.Focus()
	return true
}

// openPanel, ekranın sağ altında küçük oturum panelini açar (zaten açıksa dokunmaz).
func openPanel(url, dataDir string) {
	nativeMu.Lock()
	open := panelWin != nil
	nativeMu.Unlock()
	if open {
		return
	}
	w, err := frame.Open(frame.Options{
		URL: url, Title: AppName + " · Oturum", W: 360, H: 560, MinW: 320, MinH: 420,
		DataDir: filepath.Join(dataDir, "webview"), Corner: true,
		OnClose: func() {
			nativeMu.Lock()
			panelWin = nil
			nativeMu.Unlock()
		},
	})
	if err != nil {
		log.Printf("oturum paneli açılamadı: %v", err)
		return
	}
	nativeMu.Lock()
	panelWin = w
	nativeMu.Unlock()
	log.Printf("oturum paneli açıldı")
}

// focusPanel, paneli öne getirir (yoksa false).
func focusPanel() bool {
	nativeMu.Lock()
	w := panelWin
	nativeMu.Unlock()
	if w == nil {
		return false
	}
	w.Focus()
	return true
}

func panelOpen() bool {
	nativeMu.Lock()
	defer nativeMu.Unlock()
	return panelWin != nil
}

func closePanel() {
	nativeMu.Lock()
	w := panelWin
	nativeMu.Unlock()
	if w != nil {
		w.Close()
	}
}

func viewerDataDir(dataDir string) string { return filepath.Join(dataDir, "webview") }
