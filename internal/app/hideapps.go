package app

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"remotedesk/internal/capture"
)

// Gizlenen programlar/pencereler: bağlananlara giden görüntüde karartılır (bu bilgisayarda
// görünmeye devam eder). Liste kalıcıdır; program kapalıyken de listede durur.

// HiddenApp, bir gizleme kuralıdır:
//   - Exe + Title: o programın başlığında Title geçen pencereleri (ör. Chrome'daki "WhatsApp Web")
//   - yalnızca Exe: programın bütün pencereleri
//   - yalnızca Title: hangi programda olursa olsun başlığında Title geçen pencereler
type HiddenApp struct {
	Exe   string `json:"exe"`
	Title string `json:"title,omitempty"`
	Name  string `json:"name"` // gösterilecek ad
}

func (h HiddenApp) key() string { return h.Exe + "|" + strings.ToLower(h.Title) }

// applyHiddenLocked, listeyi yakalamaya ve arayüze yansıtır. a.mu tutulurken çağrılır.
func (a *App) applyHiddenLocked() {
	rules := make([]capture.HideRule, len(a.set.HiddenApps))
	for i, h := range a.set.HiddenApps {
		rules[i] = capture.HideRule{Exe: h.Exe, Title: h.Title}
	}
	capture.SetHidden(rules)
	a.st.HiddenApps = append([]HiddenApp{}, a.set.HiddenApps...)
	a.st.HideSupported = capture.HideSupported
}

// GET /api/hide/apps: ekranda penceresi olan programlar.
func (a *App) handleHideApps(w http.ResponseWriter, r *http.Request) {
	apps := capture.Apps()
	if apps == nil {
		apps = []capture.RunningApp{}
	}
	slices.SortFunc(apps, func(x, y capture.RunningApp) int {
		return strings.Compare(strings.ToLower(x.Title), strings.ToLower(y.Title))
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apps)
}

// POST /api/hide {exe, title, name, on}
func (a *App) handleHide(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Exe   string `json:"exe"`
		Title string `json:"title"`
		Name  string `json:"name"`
		On    bool   `json:"on"`
	}
	if err := readJSON(r, &body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	exe := strings.ToLower(strings.TrimSpace(body.Exe))
	title := capture.CleanTitle(body.Title)
	if exe == "" && len([]rune(title)) < 2 {
		http.Error(w, "En az 2 harf yaz.", 400)
		return
	}
	if len(exe) > 260 || strings.ContainsAny(exe, `/\`) || len([]rune(title)) > 200 {
		http.Error(w, "geçersiz program", 400)
		return
	}
	name := strings.TrimSpace(body.Name)
	if r := []rune(name); len(r) > 80 {
		name = string(r[:80]) + "…"
	}
	if name == "" {
		name = title
	}
	if name == "" {
		name = exe
	}
	rule := HiddenApp{Exe: exe, Title: title, Name: name}
	a.mu.Lock()
	a.set.HiddenApps = slices.DeleteFunc(a.set.HiddenApps, func(h HiddenApp) bool { return h.key() == rule.key() })
	if body.On {
		if len(a.set.HiddenApps) >= 50 {
			a.mu.Unlock()
			http.Error(w, "En çok 50 program gizlenebilir.", 400)
			return
		}
		a.set.HiddenApps = append(a.set.HiddenApps, rule)
	}
	a.saveSettingsLocked()
	a.applyHiddenLocked()
	a.broadcastLocked()
	a.mu.Unlock()
	writeOK(w)
}
