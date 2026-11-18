package host

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"remotedesk/internal/desktop"
)

// Toplantı özellikleri: işaret etme, sohbet, gizlilik modu, monitör takibi, dosya aktarımı.
//
//	izleyici -> {t:"pt", x, y}                      işaret (0..10000, izlenen monitöre göre)
//	            {t:"chat", text}                    sohbet mesajı
//	            {t:"privacy", on}                   gizlilik modu (fare/klavye izni gerekir)
//	            {t:"follow", on}                    herkes benim monitörümü izlesin (sunum)
//	            {t:"fs", fid, name, size} {t:"fc", fid, n, data} {t:"fe", fid} {t:"fx", fid}
//	                                                dosya gönder (parçalar base64)
//	            {t:"fa", fid, n}                    bana gönderilen dosyanın n baytı alındı
//	herkese  -> {t:"pt", x, y, d, c, n}  {t:"chat", id, n, c, text, ts}
//	            {t:"privacy", on, n}  {t:"follow", id, n, on}
//	            {t:"fs"/"fc"/"fe"} (bu bilgisayardan gönderilen dosya)  {t:"fa"/"fd"/"fx"}

// ChatMsg, bir sohbet mesajıdır (ID 0: bu bilgisayarın başındaki kişi).
type ChatMsg struct {
	ID    int    `json:"id"`
	Name  string `json:"n"`
	Color string `json:"c"`
	Text  string `json:"text"`
	TS    int64  `json:"ts"`
}

// HostColor, bu bilgisayarın başındaki kişinin sohbet/işaret rengi.
const HostColor = "#f5f5f7"

const (
	maxChat     = 2000
	fileChunk   = 48 << 10
	fileWindow  = 1 << 20 // yanıtsız en çok bu kadar bayt yolda
	maxFileSize = 8 << 30
)

// rate, basit bir hız sınırı: pencere başına en çok n olay.
type rate struct {
	mu    sync.Mutex
	start time.Time
	count int
}

func (r *rate) allow(n int, per time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.start) > per {
		r.start, r.count = time.Now(), 0
	}
	r.count++
	return r.count <= n
}

func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

// handleMeeting, toplantı mesajlarını işler; mesaj bu gruptan değilse false döner.
func (s *Server) handleMeeting(ctx context.Context, sess *session, m *message) bool {
	switch m.T {
	case "pt":
		v := sess.view.Load()
		if v == nil || !sess.ptRate.allow(8, time.Second) {
			return true
		}
		x, y := min(max(m.X, 0), 10000), min(max(m.Y, 0), 10000)
		color := s.SessionColor(sess.sid)
		desktop.Ping(v.d.X+x*v.d.W/10000, v.d.Y+y*v.d.H/10000, color)
		s.broadcast(map[string]any{"t": "pt", "x": x, "y": y, "d": v.idx, "c": color, "n": sess.name}, sess.sid)
	case "chat":
		text := cleanText(m.Text, maxChat)
		if text == "" || !sess.chatRate.allow(5, time.Second) {
			return true
		}
		s.chat(ChatMsg{ID: sess.sid, Name: sess.name, Color: s.SessionColor(sess.sid), Text: text, TS: time.Now().UnixMilli()})
	case "privacy":
		if !sess.allowed().Control {
			sess.c.WriteJSON(ctx, map[string]any{"t": "notice", "msg": "Gizlilik modu için fare/klavye izni gerekir."})
			return true
		}
		if err := s.SetPrivacy(m.On, sess.sid); err != nil {
			sess.c.WriteJSON(ctx, map[string]any{"t": "notice", "msg": err.Error()})
		}
	case "follow":
		s.setPresenter(sess, m.On)
	case "fs", "fc", "fe", "fx":
		s.recvFile(ctx, sess, m)
	case "fa":
		sess.files.ack(m.FID, m.N)
	default:
		return false
	}
	return true
}

// ---- sohbet ----

func (s *Server) chat(msg ChatMsg) {
	s.broadcast(map[string]any{"t": "chat", "id": msg.ID, "n": msg.Name, "c": msg.Color, "text": msg.Text, "ts": msg.TS}, -1)
	if s.OnChat != nil {
		s.OnChat(msg)
	}
}

// Chat, bu bilgisayarın başındaki kişinin mesajını herkese gönderir.
func (s *Server) Chat(name, text string) error {
	text = cleanText(text, maxChat)
	if text == "" {
		return errors.New("Boş mesaj.")
	}
	if len(s.live()) == 0 {
		return errors.New("Bağlı kimse yok.")
	}
	name = cleanName(name)
	if name == "" {
		name = "Bu bilgisayar"
	}
	s.chat(ChatMsg{ID: 0, Name: name, Color: HostColor, Text: text, TS: time.Now().UnixMilli()})
	return nil
}

// ---- gizlilik modu ----

// SetPrivacy, bu bilgisayarın ekranını karartır (görüntü bağlananlara gelmeye devam eder).
// by: açan kişi (0: bu bilgisayar); o kişi ayrılınca kapanır.
func (s *Server) SetPrivacy(on bool, by int) error {
	if on {
		ok := desktop.SetPrivacy(true, func() { s.SetPrivacy(false, 0) })
		if !ok {
			return errors.New("Gizlilik modu bu bilgisayarda desteklenmiyor.")
		}
	} else {
		desktop.SetPrivacy(false, nil)
	}
	s.mu.Lock()
	changed := s.privacy != on
	s.privacy = on
	if on {
		s.privacyBy = by
	} else {
		s.privacyBy = 0
	}
	name := ""
	if m := s.sessions[by]; m != nil {
		name = m.name
	}
	s.mu.Unlock()
	if changed {
		log.Printf("gizlilik modu: %v", on)
		s.broadcast(map[string]any{"t": "privacy", "on": on, "n": name}, -1)
		if s.OnPrivacy != nil {
			s.OnPrivacy(on)
		}
	}
	return nil
}

// Privacy, gizlilik modu açık mı.
func (s *Server) Privacy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.privacy
}

// ---- monitör takibi (sunum) ----

func (s *Server) setPresenter(sess *session, on bool) {
	s.mu.Lock()
	if on {
		s.presenter = sess.sid
	} else if s.presenter == sess.sid {
		s.presenter = 0
	} else {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.broadcast(map[string]any{"t": "follow", "id": sess.sid, "n": sess.name, "on": on}, -1)
	if on {
		if v := sess.view.Load(); v != nil {
			s.followDisplay(sess.sid, v.idx)
		}
	}
}

// followDisplay: sunan kişi monitör değiştirdi; diğer herkes de o monitöre geçer.
func (s *Server) followDisplay(sid, idx int) {
	s.mu.Lock()
	presenting := s.presenter == sid && sid != 0
	s.mu.Unlock()
	if !presenting {
		return
	}
	for id, o := range s.live() {
		if id == sid {
			continue
		}
		if v := o.view.Load(); v == nil || v.idx != idx {
			o.wantDisplay.Store(int32(idx))
			o.pokeLoop()
		}
	}
}

// presenterLeft: oturum bitince sunum/gizlilik sahibi o ise kapanır.
func (s *Server) sessionGone(sid int) {
	s.mu.Lock()
	wasPresenter := s.presenter == sid
	if wasPresenter {
		s.presenter = 0
	}
	privacy := s.privacy && s.privacyBy == sid
	s.mu.Unlock()
	if wasPresenter {
		s.broadcast(map[string]any{"t": "follow", "id": sid, "on": false}, -1)
	}
	if privacy {
		s.SetPrivacy(false, 0)
	}
}

// ---- dosya aktarımı ----

// fileState, bir oturumdaki süren dosya aktarımlarıdır.
type fileState struct {
	mu   sync.Mutex
	in   map[string]*inFile    // bu kişiden gelen
	acks map[string]chan int64 // bu kişiye giden (alınan bayt bildirimi)
}

type inFile struct {
	f    *os.File
	tmp  string
	name string
	size int64
	n    int64
	last int64 // en son bildirilen
}

func (fs *fileState) ack(fid string, n int64) {
	fs.mu.Lock()
	ch := fs.acks[fid]
	fs.mu.Unlock()
	if ch != nil {
		select {
		case ch <- n:
		default:
		}
	}
}

func (fs *fileState) closeAll() {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, f := range fs.in {
		f.f.Close()
		os.Remove(f.tmp)
	}
	fs.in = nil
}

// safeName, dosya adını klasör ayıracı ve tehlikeli karakterlerden arındırır.
func safeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "dosya"
	}
	if r := []rune(name); len(r) > 120 {
		ext := filepath.Ext(name)
		name = string(r[:100]) + ext
	}
	return name
}

// UniquePath, klasörde var olan bir dosyanın üzerine yazmamak için "ad (2).uz" üretir.
func UniquePath(dir, name string) string {
	p := filepath.Join(dir, name)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
	}
}

func (s *Server) recvFile(ctx context.Context, sess *session, m *message) {
	fs := &sess.files
	reply := func(v map[string]any) { sess.c.WriteJSON(ctx, v) }
	reject := func(msg string) { reply(map[string]any{"t": "fx", "fid": m.FID, "msg": msg}) }
	if m.FID == "" || len(m.FID) > 40 {
		return
	}
	switch m.T {
	case "fs":
		if !sess.allowed().Files || s.FileDir == nil {
			reject("Karşı taraf dosya almayı kapatmış.")
			return
		}
		if m.Size < 0 || m.Size > maxFileSize {
			reject("Dosya çok büyük (en fazla 8 GB).")
			return
		}
		dir := s.FileDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			reject("Dosya kaydedilemiyor.")
			return
		}
		f, err := os.CreateTemp(dir, ".meetdesk-*.part")
		if err != nil {
			reject("Dosya kaydedilemiyor.")
			return
		}
		fs.mu.Lock()
		if fs.in == nil {
			fs.in = map[string]*inFile{}
		}
		if len(fs.in) >= 8 {
			fs.mu.Unlock()
			f.Close()
			os.Remove(f.Name())
			reject("Aynı anda çok fazla dosya.")
			return
		}
		fs.in[m.FID] = &inFile{f: f, tmp: f.Name(), name: safeName(m.Name), size: m.Size}
		fs.mu.Unlock()
		reply(map[string]any{"t": "fa", "fid": m.FID, "n": 0})
	case "fc":
		fs.mu.Lock()
		in := fs.in[m.FID]
		fs.mu.Unlock()
		if in == nil {
			return
		}
		b, err := base64.StdEncoding.DecodeString(m.Data)
		if err != nil || m.N != in.n || in.n+int64(len(b)) > in.size {
			s.dropFile(sess, m.FID)
			reject("Dosya bozuk geldi.")
			return
		}
		if _, err := in.f.Write(b); err != nil {
			s.dropFile(sess, m.FID)
			reject("Dosya yazılamadı (disk dolu olabilir).")
			return
		}
		in.n += int64(len(b))
		if in.n-in.last >= 256<<10 {
			in.last = in.n
			reply(map[string]any{"t": "fa", "fid": m.FID, "n": in.n})
		}
	case "fe":
		fs.mu.Lock()
		in := fs.in[m.FID]
		delete(fs.in, m.FID)
		fs.mu.Unlock()
		if in == nil {
			return
		}
		in.f.Close()
		if in.n != in.size {
			os.Remove(in.tmp)
			reject("Dosya eksik geldi.")
			return
		}
		final := UniquePath(filepath.Dir(in.tmp), in.name)
		if err := os.Rename(in.tmp, final); err != nil {
			os.Remove(in.tmp)
			reject("Dosya kaydedilemedi.")
			return
		}
		log.Printf("dosya alındı: %s (%d bayt, %s)", final, in.size, sess.name)
		reply(map[string]any{"t": "fd", "fid": m.FID, "name": filepath.Base(final)})
		if s.OnFile != nil {
			s.OnFile(sess.sid, sess.name, final, in.size)
		}
	case "fx":
		s.dropFile(sess, m.FID)
	}
}

func (s *Server) dropFile(sess *session, fid string) {
	fs := &sess.files
	fs.mu.Lock()
	in := fs.in[fid]
	delete(fs.in, fid)
	fs.mu.Unlock()
	if in != nil {
		in.f.Close()
		os.Remove(in.tmp)
	}
}

// SendFile, bu bilgisayardan bir dosyayı bağlı kişilere gönderir (sid 0: herkese).
// open her alıcı için dosyayı baştan açar.
func (s *Server) SendFile(sid int, name string, size int64, open func() (io.ReadCloser, error)) error {
	var targets []*session
	for id, sess := range s.live() {
		if sid == 0 || id == sid {
			targets = append(targets, sess)
		}
	}
	if len(targets) == 0 {
		return errors.New("Bağlı kimse yok.")
	}
	name = safeName(name)
	var wg sync.WaitGroup
	errs := make(chan error, len(targets))
	for _, t := range targets {
		wg.Add(1)
		go func(t *session) {
			defer wg.Done()
			if err := t.sendFile(name, size, open); err != nil {
				errs <- err
			}
		}(t)
	}
	wg.Wait()
	close(errs)
	return <-errs
}

var fileSeq struct {
	mu sync.Mutex
	n  int
}

func (sess *session) sendFile(name string, size int64, open func() (io.ReadCloser, error)) error {
	r, err := open()
	if err != nil {
		return err
	}
	defer r.Close()
	fileSeq.mu.Lock()
	fileSeq.n++
	fid := fmt.Sprintf("h%d", fileSeq.n)
	fileSeq.mu.Unlock()
	acks := make(chan int64, 16)
	fs := &sess.files
	fs.mu.Lock()
	if fs.acks == nil {
		fs.acks = map[string]chan int64{}
	}
	fs.acks[fid] = acks
	fs.mu.Unlock()
	defer func() {
		fs.mu.Lock()
		delete(fs.acks, fid)
		fs.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	if err := sess.c.WriteJSON(ctx, map[string]any{"t": "fs", "fid": fid, "name": name, "size": size}); err != nil {
		return err
	}
	buf := make([]byte, fileChunk)
	var sent, acked int64
	for sent < size {
		for sent-acked > fileWindow {
			select {
			case n := <-acks:
				acked = max(acked, n)
			case <-time.After(60 * time.Second):
				return errors.New("Karşı taraf dosyayı almıyor.")
			}
		}
		n, err := io.ReadFull(r, buf[:min(int64(len(buf)), size-sent)])
		if err != nil {
			sess.c.WriteJSON(ctx, map[string]any{"t": "fx", "fid": fid})
			return err
		}
		if err := sess.c.WriteJSON(ctx, map[string]any{"t": "fc", "fid": fid, "n": sent, "data": base64.StdEncoding.EncodeToString(buf[:n])}); err != nil {
			return err
		}
		sent += int64(n)
	}
	return sess.c.WriteJSON(ctx, map[string]any{"t": "fe", "fid": fid})
}
