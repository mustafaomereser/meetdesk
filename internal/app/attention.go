package app

import (
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"remotedesk/internal/tray"
)

// İzin istekleri (ör. çizim) ve dikkat çekme: oturum paneli kapalıyken gelen mesaj ya da
// istek fark edilsin diye bildirim alanı simgesine mavi nokta konur ve kısa bir bildirim
// gösterilir. Panel ya da ana pencere açılınca nokta kalkar.

// Ask, bağlanan bir kişinin izin isteğidir.
type Ask struct {
	ID   int    `json:"id"` // oturum
	Name string `json:"name"`
	Kind string `json:"kind"` // "draw"
	TS   int64  `json:"ts"`
}

var (
	attnMu   sync.Mutex
	attnLast = map[string]time.Time{} // aynı türden bildirimleri seyrekleştirmek için
)

// attention, panel kapalıysa simgeye nokta koyar ve (tür başına en çok 5 sn'de bir) bildirim gösterir.
func (a *App) attention(kind, title, text string, always bool) {
	if !a.trayOn {
		return
	}
	if !always && panelOpen() {
		return
	}
	tray.SetBadge(true)
	attnMu.Lock()
	ok := time.Since(attnLast[kind]) > 5*time.Second
	if ok {
		attnLast[kind] = time.Now()
	}
	attnMu.Unlock()
	if ok {
		tray.Notify(title, text)
	}
}

func (a *App) clearAttention() { tray.SetBadge(false) }

func (a *App) onAsk(sid int, name, kind string) {
	if name == "" {
		name = "Uzak kullanıcı"
	}
	a.update(func(s *State) {
		s.Asks = slices.DeleteFunc(s.Asks, func(x Ask) bool { return x.ID == sid && x.Kind == kind })
		s.Asks = append(s.Asks, Ask{ID: sid, Name: name, Kind: kind, TS: time.Now().UnixMilli()})
	})
	a.attention("ask", AppName, fmt.Sprintf("%s ekrana çizmek istiyor. İzin vermek için oturum panelini aç.", name), false)
}

// POST /api/host/ask {id, kind, allow}
func (a *App) handleAsk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID    int    `json:"id"`
		Kind  string `json:"kind"`
		Allow bool   `json:"allow"`
	}
	if err := readJSON(r, &body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	a.update(func(s *State) {
		s.Asks = slices.DeleteFunc(s.Asks, func(x Ask) bool { return x.ID == body.ID && x.Kind == body.Kind })
	})
	if body.Kind != "draw" {
		writeOK(w)
		return
	}
	if body.Allow {
		if !a.setSessionDraw(body.ID, true) {
			http.Error(w, "Bu kişi artık bağlı değil.", 404)
			return
		}
	} else {
		a.host.Notice(body.ID, "Çizim isteğin kabul edilmedi.")
	}
	writeOK(w)
}

func (a *App) setSessionDraw(sid int, on bool) bool {
	if !a.host.SetDraw(sid, on) {
		return false
	}
	a.update(func(s *State) {
		for i := range s.Sessions {
			if s.Sessions[i].ID == sid {
				s.Sessions[i].Draw = on
			}
		}
		if on {
			s.Asks = slices.DeleteFunc(s.Asks, func(x Ask) bool { return x.ID == sid && x.Kind == "draw" })
		}
	})
	return true
}

// refreshDraw, genel ayar değişince kişilerin çizim durumunu yeniler.
func (a *App) refreshDraw() {
	a.mu.Lock()
	ids := make([]int, len(a.st.Sessions))
	for i, x := range a.st.Sessions {
		ids[i] = x.ID
	}
	a.mu.Unlock()
	draw := map[int]bool{}
	for _, id := range ids {
		draw[id] = a.host.DrawAllowed(id)
	}
	a.update(func(s *State) {
		for i := range s.Sessions {
			s.Sessions[i].Draw = draw[s.Sessions[i].ID]
		}
	})
}
