package app

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"remotedesk/internal/host"
)

// Toplantı: sohbet geçmişi, alınan dosyalar, gizlilik modu ve bunların yerel API'si.

// RecvFile, bağlanan birinden alınıp kaydedilen dosyadır.
type RecvFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
	From string `json:"from"`
	Size int64  `json:"size"`
	TS   int64  `json:"ts"`
}

const maxChatHistory = 200

// fileDir: alınan dosyaların klasörü (İndirilenler/Meetdesk).
func fileDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads", "Meetdesk")
}

func (a *App) onChat(m host.ChatMsg) {
	a.update(func(s *State) {
		s.Chat = append(s.Chat, m)
		if len(s.Chat) > maxChatHistory {
			s.Chat = s.Chat[len(s.Chat)-maxChatHistory:]
		}
	})
}

func (a *App) onFile(sid int, from, path string, size int64) {
	a.update(func(s *State) {
		s.Received = append(s.Received, RecvFile{Name: filepath.Base(path), Path: path, From: from, Size: size, TS: time.Now().UnixMilli()})
		if len(s.Received) > 20 {
			s.Received = s.Received[len(s.Received)-20:]
		}
	})
}

func (a *App) onPrivacy(on bool) { a.update(func(s *State) { s.Privacy = on }) }

func (a *App) handleChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := a.host.Chat(body.Name, body.Text); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeOK(w)
}

func (a *App) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On bool `json:"on"`
	}
	if err := readJSON(r, &body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := a.host.SetPrivacy(body.On, 0); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeOK(w)
}

// handleFileSend: bu bilgisayardan bağlı kişilere dosya gönder. Gövde dosyanın kendisidir;
// önce geçici dosyaya yazılır, gönderim arka planda sürer.
func (a *App) handleFileSend(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	sid, _ := strconv.Atoi(r.URL.Query().Get("sid"))
	if name == "" {
		http.Error(w, "dosya adı yok", 400)
		return
	}
	tmp, err := os.CreateTemp("", "meetdesk-send-*")
	if err != nil {
		http.Error(w, "geçici dosya açılamadı", 500)
		return
	}
	size, err := io.Copy(tmp, http.MaxBytesReader(w, r.Body, 8<<30))
	tmp.Close()
	if err != nil {
		os.Remove(tmp.Name())
		http.Error(w, "dosya okunamadı", 400)
		return
	}
	go func() {
		defer os.Remove(tmp.Name())
		err := a.host.SendFile(sid, name, size, func() (io.ReadCloser, error) { return os.Open(tmp.Name()) })
		if err != nil {
			log.Printf("dosya gönderilemedi: %v", err)
		}
	}()
	writeOK(w)
}

// handleFileSave: izleme penceresinin karşıdan aldığı dosyayı kaydeder (gövde = dosya).
func (a *App) handleFileSave(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(strings.ReplaceAll(r.URL.Query().Get("name"), "\\", "/"))
	if name == "" || name == "." || name == ".." {
		http.Error(w, "dosya adı yok", 400)
		return
	}
	dir := fileDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "klasör oluşturulamadı", 500)
		return
	}
	p := host.UniquePath(dir, name)
	f, err := os.Create(p)
	if err != nil {
		http.Error(w, "dosya yazılamadı", 500)
		return
	}
	_, err = io.Copy(f, http.MaxBytesReader(w, r.Body, 8<<30))
	f.Close()
	if err != nil {
		os.Remove(p)
		http.Error(w, "dosya yazılamadı", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"path": p, "name": filepath.Base(p)})
}

// handleFileReveal: dosyayı (yalnızca alınan dosyalar klasöründe) dosya yöneticisinde gösterir.
func (a *App) handleFileReveal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	readJSON(r, &body)
	dir := fileDir()
	p := filepath.Clean(body.Path)
	if body.Path == "" {
		p = dir
		os.MkdirAll(dir, 0o755)
	} else if rel, err := filepath.Rel(dir, p); err != nil || strings.HasPrefix(rel, "..") {
		http.Error(w, "izin yok", 403)
		return
	}
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "windows" && p != dir:
		cmd = exec.Command("explorer", "/select,", p)
	case runtime.GOOS == "windows":
		cmd = exec.Command("explorer", p)
	case p != dir:
		cmd = exec.Command("open", "-R", p)
	default:
		cmd = exec.Command("open", p)
	}
	cmd.Start()
	writeOK(w)
}
