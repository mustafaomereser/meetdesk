//go:build darwin

package app

import "remotedesk/internal/desktop"

// openNativeWindow, arayüzü uygulamanın kendi penceresinde (WKWebView) açar.
func openNativeWindow(url, dataDir string, onClose func(), minimized bool) bool {
	return desktop.OpenAppWindow(url, AppName, onClose)
}

// macOS: tepsi simgesi ve sağ alt oturum paneli henüz yok (ana penceredeki Oturum sayfası kullanılır).
func focusNativeMain() bool         { return false }
func openPanel(url, dataDir string) {}
func focusPanel() bool              { return false }
func closePanel()                   {}
func panelOpen() bool               { return false }

func viewerDataDir(dataDir string) string { return "" }
