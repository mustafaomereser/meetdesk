package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"remotedesk/internal/capture"
	"remotedesk/internal/video"
)

// Paylaşılan H.264 kodlayıcı: aynı monitörü aynı kalitede izleyen herkes için tek yakalama
// ve tek ffmpeg çalışır; kodlanan kareler herkese dağıtılır. Kişi sayısı arttıkça yalnızca
// gönderilen veri artar (her izleyiciye ayrı kodlayıcı açmak işlemciyi/ekran kartını
// kişi sayısıyla çarpıyordu; ekran kartlarının aynı anda açabildiği kodlayıcı sayısı da sınırlı).
//
// Hız: kodlayıcı, izleyicilerin hepsi yetişebildiği sürece yeni kare kodlar. Bağlantısı
// yavaş olan izleyici "geride" sayılır ve hızı belirlemez; o kişi kareleri atlar ve bir
// sonraki anahtar kareden devam eder (anahtar kare istenir, en çok 2 sn'de bir).
// Bit hızı: geride olmayanlardan biri zorlanıyorsa düşer, hepsi uzun süre rahatsa artar.

type hubKey struct {
	d capture.Display
	q int32
}

type hubFrame struct {
	f    video.Frame
	w, h int // kodlanan görüntü boyutu
	rate int
	gen  int // kodlayıcı numarası (her yeniden başlatmada artar)
}

type hubSub struct {
	ch  chan hubFrame
	err chan error

	// Oturum goroutine'i günceller, kodlayıcı okur.
	full    atomic.Bool  // izleyicinin akış penceresi dolu
	bad     atomic.Bool  // gecikme büyüyor (bit hızı düşmeli)
	good    atomic.Int64 // art arda "rahat" onay sayısı
	lagging atomic.Bool  // diğerlerine yetişemiyor: hızı belirlemez

	needKey   bool      // h.mu altında: anahtar kare gelene kadar kare verilmez
	fullSince time.Time // yalnızca kodlayıcı goroutine'i
}

type videoHub struct {
	key      hubKey
	srv      *Server
	bin, enc string

	mu     sync.Mutex
	subs   map[*hubSub]struct{}
	cancel context.CancelFunc
	dead   bool

	wake    chan struct{}
	keyReq  atomic.Bool // bir izleyici anahtar kare bekliyor (geride kaldı / çözücüsünü kaybetti)
	joinReq atomic.Bool // yeni katılan izleyici anahtar kare bekliyor (hemen verilir)
	lastKey time.Time   // yalnızca kodlayıcı goroutine'i

	rate, upNeed int // uyarlamalı bit hızı (yalnızca kodlayıcı goroutine'i)
}

var standaloneHubs sync.Mutex // srv == nil (testler) için

// videoSub, izleyiciyi monitörün paylaşılan kodlayıcısına bağlar (yoksa başlatır).
func (srv *Server) videoSub(key hubKey, bin, enc string) (*videoHub, *hubSub) {
	sub := &hubSub{ch: make(chan hubFrame, 48), err: make(chan error, 1), needKey: true}
	var h *videoHub
	existed := false
	if srv != nil {
		srv.vhubMu.Lock()
		if srv.vhubs == nil {
			srv.vhubs = map[hubKey]*videoHub{}
		}
		h = srv.vhubs[key]
		existed = h != nil
		if h != nil && (h.enc != enc || h.dead) {
			h = nil // kodlayıcı değişti (ör. kurulum sonrası): yenisi açılır, eskisi abonesi bitince kapanır
		}
		if h == nil {
			existed = false
			h = newVideoHub(srv, key, bin, enc)
			srv.vhubs[key] = h
		}
		h.mu.Lock()
		h.subs[sub] = struct{}{}
		h.mu.Unlock()
		srv.vhubMu.Unlock()
	} else {
		standaloneHubs.Lock()
		h = newVideoHub(nil, key, bin, enc)
		h.subs[sub] = struct{}{}
		standaloneHubs.Unlock()
	}
	if existed {
		// Çalışan kodlayıcıya katılan izleyici anahtar kareden başlamalı. Kodlayıcı zaten
		// ilk (anahtar) karesini üretmek üzereyse onu alır; istek o kare dağıtılınca düşer.
		h.joinReq.Store(true)
	}
	h.poke()
	return h, sub
}

func newVideoHub(srv *Server, key hubKey, bin, enc string) *videoHub {
	ctx, cancel := context.WithCancel(context.Background())
	h := &videoHub{key: key, srv: srv, bin: bin, enc: enc, subs: map[*hubSub]struct{}{}, cancel: cancel, wake: make(chan struct{}, 1)}
	go h.run(ctx)
	return h
}

// hubLinger: son izleyici çıkınca ffmpeg hemen kapanır ama ekran yakalayıcı bu süre açık
// kalır; monitörler arasında gidip gelince yakalama baştan kurulmaz.
const hubLinger = 20 * time.Second

// unsubscribe, izleyiciyi çıkarır. Kimse kalmazsa kodlayıcı durur, bir süre sonra kapanır.
func (h *videoHub) unsubscribe(sub *hubSub) {
	h.mu.Lock()
	delete(h.subs, sub)
	h.mu.Unlock()
	if h.srv == nil {
		h.cancel()
	}
	h.poke()
}

// retire, izleyicisiz kalan kodlayıcıyı kayıttan siler. Bu arada biri katıldıysa false.
func (h *videoHub) retire() bool {
	if h.srv != nil {
		h.srv.vhubMu.Lock()
		defer h.srv.vhubMu.Unlock()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) > 0 {
		return false
	}
	h.dead = true
	if h.srv != nil && h.srv.vhubs[h.key] == h {
		delete(h.srv.vhubs, h.key)
	}
	return true
}

func (h *videoHub) subCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *videoHub) poke() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *videoHub) requestKey() { h.keyReq.Store(true) }

// fail, kodlayıcı kullanılamaz durumdaysa izleyicilere bildirir ve kendini kayıttan siler;
// oturumlar mevcut kurallarla yeniden dener ya da JPEG'e geçer.
func (h *videoHub) fail(err error) {
	if h.srv != nil {
		h.srv.vhubMu.Lock()
		if h.srv.vhubs[h.key] == h {
			delete(h.srv.vhubs, h.key)
		}
		h.srv.vhubMu.Unlock()
	}
	h.mu.Lock()
	h.dead = true
	for sub := range h.subs {
		select {
		case sub.err <- err:
		default:
		}
	}
	h.mu.Unlock()
	h.cancel()
}

// broadcast, kodlanan kareyi izleyicilere dağıtır. Anahtar kare bekleyen izleyici ara
// kareleri almaz; kuyruğu dolan izleyici (çok geride) anahtar kareye kadar bekletilir.
func (h *videoHub) broadcast(hf hubFrame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if hf.f.Key {
		h.keyReq.Store(false) // bu kare bekleyenleri karşılar; karşılayamazsa aşağıda yeniden kurulur
		h.joinReq.Store(false)
	}
	for sub := range h.subs {
		if sub.needKey {
			if !hf.f.Key {
				h.keyReq.Store(true)
				continue
			}
			sub.needKey = false
		}
		select {
		case sub.ch <- hf:
		default:
			sub.needKey = true
			h.keyReq.Store(true)
		}
	}
}

func (h *videoHub) snapshot() []*hubSub {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*hubSub, 0, len(h.subs))
	for sub := range h.subs {
		out = append(out, sub)
	}
	return out
}

func (h *videoHub) grabber(cp *capture.Capturer, half *capture.Frame) func() (*capture.Frame, time.Duration, error) {
	return func() (*capture.Frame, time.Duration, error) {
		t := time.Now()
		cp.Full = h.key.q >= 85
		f, err := cp.Capture(h.key.d)
		if err != nil {
			return nil, 0, err
		}
		if h.key.q < 85 && f.W >= h.key.d.W*3/2 && f.H >= h.key.d.H*3/2 {
			capture.Half(f, half)
			f = half
		}
		return f, time.Since(t), nil
	}
}

func (h *videoHub) run(ctx context.Context) {
	cp, err := capture.New()
	if err != nil {
		h.fail(fmt.Errorf("%w: %v", errVideoFailed, err))
		return
	}
	defer cp.Close()
	var half capture.Frame
	grab := h.grabber(cp, &half)
	maxRate := video.BitrateFor(int(h.key.q))
	h.rate, h.upNeed = min(maxRate, 6_000_000), 300
	gen := 0
	var deaths []time.Time
	for ctx.Err() == nil {
		if h.subCount() == 0 && !h.idle(ctx) {
			return
		}
		gen++
		err := h.encode(ctx, grab, gen, maxRate)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRestart) || errors.Is(err, errNoViewers) {
			continue
		}
		if errors.Is(err, errVideoFailed) {
			h.fail(err)
			return
		}
		// Kodlayıcı kapandı: kısa sürede 3 kez olursa izleyicilere bildir (JPEG'e geçerler).
		now := time.Now()
		deaths = append(deaths, now)
		if len(deaths) > 3 {
			deaths = deaths[1:]
		}
		if len(deaths) == 3 && now.Sub(deaths[0]) < 30*time.Second {
			h.fail(errEncoderDied)
			return
		}
		log.Printf("ffmpeg kapandı, yeniden başlatılıyor: %v", err)
		if !sleepCtx(ctx, 300*time.Millisecond) {
			return
		}
	}
}

var errNoViewers = errors.New("izleyici kalmadı")

// idle, izleyici gelene kadar bekler (true); hubLinger boyunca gelmezse kapanır (false).
func (h *videoHub) idle(ctx context.Context) bool {
	deadline := time.Now().Add(hubLinger)
	for {
		if h.subCount() > 0 {
			return true
		}
		if time.Now().After(deadline) {
			if h.retire() {
				return false
			}
			continue
		}
		select {
		case <-ctx.Done():
			return false
		case <-h.wake:
		case <-time.After(time.Until(deadline)):
		}
	}
}

// encode, bir ffmpeg süreci boyunca çalışır. errRestart: yeniden başlatılmalı (anahtar
// kare isteği, bit hızı ya da çözünürlük değişimi).
func (h *videoHub) encode(ctx context.Context, grab func() (*capture.Frame, time.Duration, error), gen, maxRate int) error {
	// İlk kare (kilit ekranı/UAC'de yakalama bir süre başarısız olabilir)
	t0 := time.Now()
	var f *capture.Frame
	for {
		var err error
		if f, _, err = grab(); err == nil {
			break
		}
		log.Printf("yakalama hatası: %v", err)
		if !sleepCtx(ctx, 500*time.Millisecond) {
			return ctx.Err()
		}
	}
	inW, inH := f.W, f.H
	outW, outH := fitEven(inW, inH, maxVideoWidth)
	h.rate = min(h.rate, maxRate)
	rate := h.rate

	ectx, cancel := context.WithCancel(ctx)
	defer cancel()
	tGrab := time.Since(t0)
	e, err := video.Start(ectx, h.bin, h.enc, inW, inH, outW, outH, maxFPS, rate)
	if err != nil {
		return fmt.Errorf("%w: %v", errVideoFailed, err)
	}
	defer e.Close()
	h.keyReq.Store(false) // yeni kodlayıcının ilk karesi anahtar karedir
	h.joinReq.Store(false)
	h.lastKey = time.Now()
	var out atomic.Int32 // üretilen kare sayısı; ilk (anahtar) kare çıkmadan yeniden başlatma yok

	sendErr := make(chan error, 1)
	go func() {
		got := 0
		for fr := range e.Frames() {
			got++
			out.Add(1)
			if got == 1 {
				log.Printf("kodlayıcı hazır: %v (ilk yakalama %v), anahtar kare %d KB, %.1f Mbps", time.Since(t0).Round(time.Millisecond), tGrab.Round(time.Millisecond), len(fr.Data)>>10, float64(rate)/1e6)
			}
			h.broadcast(hubFrame{f: fr, w: outW, h: outH, rate: rate, gen: gen})
		}
		if ectx.Err() != nil {
			sendErr <- ectx.Err()
		} else if got == 0 {
			sendErr <- fmt.Errorf("%w: %s", errVideoFailed, e.ErrText())
		} else {
			sendErr <- fmt.Errorf("%w: %s", errEncoderDied, e.ErrText())
		}
	}()

	prev := make([]byte, len(f.Pix))
	copy(prev, f.Pix)
	if err := e.WriteFrame(f); err != nil {
		return fmt.Errorf("%w: %v", errVideoFailed, err)
	}
	refine := refineFrames
	frames, free, stop := startGrabber(ctx, grab)
	defer stop()

	for {
		select {
		case err := <-sendErr:
			return err
		default:
		}
		subs := h.snapshot()
		if len(subs) == 0 {
			return errNoViewers // ffmpeg kapanır, yakalayıcı bir süre açık kalır
		}
		// Anahtar kare: ffmpeg'e komut satırından "şimdi anahtar kare" denemediği için
		// kodlayıcı yeniden başlatılır (izleyicilerde görüntü kesilmez). Yeni katılan hemen,
		// geride kalan en çok 2 sn'de bir (her anahtar kare herkese ek yük).
		sinceKey := time.Since(h.lastKey)
		if out.Load() > 0 && (h.joinReq.Load() && sinceKey >= 250*time.Millisecond || h.keyReq.Load() && sinceKey >= 2*time.Second) {
			return errRestart
		}
		if restart := h.adaptRate(subs, rate, maxRate); restart {
			return errRestart
		}
		if !h.ready(ctx, subs, sendErr) {
			continue
		}

		var g *grabbed
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-sendErr:
			return err
		case <-h.wake:
			continue
		case g = <-frames:
		}
		if g.err != nil {
			log.Printf("yakalama hatası: %v", g.err)
			free <- g
			continue
		}
		cur := &g.f
		if cur.W != inW || cur.H != inH {
			free <- g
			return errRestart
		}
		send := true
		if !bytes.Equal(cur.Pix, prev) {
			copy(prev, cur.Pix)
			refine = refineFrames
		} else if refine > 0 {
			refine--
		} else {
			send = false // ekran durağan: trafik yok
		}
		if send {
			if err := e.WriteFrame(cur); err != nil {
				free <- g
				select {
				case err := <-sendErr:
					return err
				case <-time.After(time.Second):
					return errEncoderDied
				}
			}
		}
		free <- g
	}
}

// ready, geride olmayan izleyicilerin hepsinin yeni kareye yeri varsa true döner; yoksa
// kısa süre bekler. 400 ms'den uzun dolu kalan izleyici (başkaları beklerken) geride sayılır.
func (h *videoHub) ready(ctx context.Context, subs []*hubSub, sendErr chan error) bool {
	now := time.Now()
	anyFree, blocked := false, false
	for _, s := range subs {
		if !s.full.Load() {
			s.fullSince = time.Time{}
			if s.lagging.Load() {
				s.lagging.Store(false) // yetişti: yeniden hızı belirler
			}
			anyFree = true
			continue
		}
		if s.fullSince.IsZero() {
			s.fullSince = now
		}
	}
	for _, s := range subs {
		if !s.full.Load() || s.lagging.Load() {
			continue
		}
		if anyFree && now.Sub(s.fullSince) > 400*time.Millisecond {
			s.lagging.Store(true)
			continue
		}
		blocked = true
	}
	if !blocked {
		return true
	}
	select {
	case <-ctx.Done():
	case <-h.wake:
	case <-time.After(50 * time.Millisecond):
	}
	return false
}

// adaptRate, bit hızını izleyicilerin bağlantısına göre ayarlar; değiştiyse true.
func (h *videoHub) adaptRate(subs []*hubSub, rate, maxRate int) bool {
	anyBad, minGood, n := false, int64(1<<62), 0
	for _, s := range subs {
		if s.lagging.Load() {
			continue
		}
		n++
		if s.bad.Swap(false) {
			anyBad = true
		}
		minGood = min(minGood, s.good.Load())
	}
	if n == 0 {
		return false
	}
	if anyBad {
		if lower := video.StepRate(rate, -1); lower < rate {
			h.rate, h.upNeed = lower, min(h.upNeed*2, 9600)
			log.Printf("bit hızı düşürüldü: %.1f Mbps", float64(lower)/1e6)
			return true
		}
		return false
	}
	if minGood >= int64(h.upNeed) && rate < maxRate {
		h.rate = min(video.StepRate(rate, +1), maxRate)
		log.Printf("bit hızı yükseltildi: %.1f Mbps", float64(h.rate)/1e6)
		return true
	}
	return false
}
