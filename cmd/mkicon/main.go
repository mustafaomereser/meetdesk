// mkicon: Meetdesk uygulama ikonunu üretir.
//
//	go run ./cmd/mkicon
//
// Çıktılar:
//
//	packaging/meetdesk.png  (1024, kaynak)
//	packaging/meetdesk.icns (macOS paketi)
//	packaging/meetdesk.ico  (Windows exe'ye gömülür, bkz. scripts/winres.sh)
//	web/icon.png            (256; pencere, bildirim alanı simgesi ve arayüz logosu)
//
// Tasarım: sıcak turuncu-kırmızı geçişli yuvarlak kare; ortada beyaz monitör (uzaktan
// erişim), sağ altta monitörün köşesine binen beyaz bir konuşma balonu (toplantı) ve
// balonun içinde üç nokta. Küçük boyutlarda (16-32 px) da okunur kalması için kalın çizgiler.
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

// sdRoundRect: merkezi (cx,cy), yarı boyutu (hx,hy), köşe yarıçapı r olan yuvarlak
// dikdörtgene işaretli uzaklık (içeride negatif).
func sdRoundRect(x, y, cx, cy, hx, hy, r float64) float64 {
	qx := math.Abs(x-cx) - hx + r
	qy := math.Abs(y-cy) - hy + r
	ox, oy := math.Max(qx, 0), math.Max(qy, 0)
	return math.Hypot(ox, oy) + math.Min(math.Max(qx, qy), 0) - r
}

// sdTriangle: üç köşeli çokgene işaretli uzaklık.
func sdTriangle(x, y float64, p [3][2]float64) float64 {
	d := math.Inf(1)
	pos, neg := false, false
	for i := range 3 {
		a, b := p[i], p[(i+1)%3]
		ex, ey := b[0]-a[0], b[1]-a[1]
		wx, wy := x-a[0], y-a[1]
		t := math.Max(0, math.Min(1, (wx*ex+wy*ey)/(ex*ex+ey*ey)))
		d = math.Min(d, math.Hypot(wx-ex*t, wy-ey*t))
		if c := ex*wy - ey*wx; c > 0 {
			pos = true
		} else if c < 0 {
			neg = true
		}
	}
	if !(pos && neg) { // köşe sırasından bağımsız: tüm kenarların aynı tarafında
		return -d
	}
	return d
}

func coverage(d, px float64) float64 { return math.Max(0, math.Min(1, 0.5-d/px)) }
func lerp(a, b, t float64) float64   { return a + (b-a)*t }

// render, 1024'lük tasarım ızgarasını size×size piksele çizer. pad: kenar boşluğu
// (macOS ikon ızgarası 100, Windows/web için daha az).
func render(size int, pad float64) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := 1024 / float64(size)
	half := 512 - pad
	for py := range size {
		for px := range size {
			x, y := (float64(px)+0.5)*s, (float64(py)+0.5)*s
			bg := coverage(sdRoundRect(x, y, 512, 512, half, half, half*0.45), s)
			if bg == 0 {
				continue
			}
			// Zemin: sol üstten sağ alta sıcak geçiş + hafif üst parlaklık
			t := math.Max(0, math.Min(1, (x+y-2*pad)/(2048-4*pad)))
			r, g, b := lerp(0xFF, 0xF0, t), lerp(0x6A, 0x3A, t), lerp(0x3D, 0x4E, t)
			if y < 512 {
				k := (512 - y) / 512 * 0.10
				r, g, b = lerp(r, 255, k), lerp(g, 255, k), lerp(b, 255, k)
			}

			// Ölçek: tasarım 824'lük alana göre yapıldı, pad değişince ortalanır
			u := (x-512)*(412/half) + 512
			v := (y-512)*(412/half) + 512
			ps := s * 412 / half

			// Monitör (sol üste kaydırılmış): kalın çerçeve + ayak + taban
			screen := math.Abs(sdRoundRect(u, v, 445, 395, 255, 175, 46)) - 30
			neck := sdRoundRect(u, v, 445, 622, 28, 40, 6)
			base := sdRoundRect(u, v, 445, 672, 100, 24, 24)
			monitor := math.Min(screen, math.Min(neck, base))

			// Konuşma balonu (sağ alt): balon + kuyruk; çevresinde zemin renginde boşluk
			bubble := math.Min(sdRoundRect(u, v, 742, 660, 150, 105, 56),
				sdTriangle(u, v, [3][2]float64{{650, 740}, {628, 828}, {720, 755}}))
			gap := bubble - 34 // balonun çevresi: monitör burada kesilir

			w := 0.0
			if gap > 0 {
				w = coverage(monitor, ps)
			} else {
				w = coverage(monitor, ps) * coverage(-gap, ps)
			}
			w = math.Max(w, coverage(bubble, ps))

			// Balonun içinde üç nokta (zemin renginde)
			dots := math.Inf(1)
			for _, cx := range []float64{682, 742, 802} {
				dots = math.Min(dots, math.Hypot(u-cx, v-660)-22)
			}
			dk := coverage(dots, ps)

			fr, fg, fb := lerp(r, 255, w), lerp(g, 255, w), lerp(b, 255, w)
			dr, dg, db := lerp(0xFF, 0xF0, 0.6), lerp(0x6A, 0x3A, 0.6), lerp(0x3D, 0x4E, 0.6)
			fr, fg, fb = lerp(fr, dr, dk), lerp(fg, dg, dk), lerp(fb, db, dk)
			img.SetNRGBA(px, py, color.NRGBA{uint8(fr), uint8(fg), uint8(fb), uint8(bg * 255)})
		}
	}
	return img
}

func pngBytes(size int, pad float64) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, render(size, pad)); err != nil {
		log.Fatal(err)
	}
	return buf.Bytes()
}

func write(p string, b []byte) {
	if err := os.WriteFile(p, b, 0o644); err != nil {
		log.Fatal(err)
	}
}

const (
	macPad = 100 // Apple ikon ızgarası (824x824)
	winPad = 40  // Windows / web: ikon alanını daha çok doldurur
)

func main() {
	dir := "packaging"
	os.MkdirAll(dir, 0o755)
	write(filepath.Join(dir, "meetdesk.png"), pngBytes(1024, macPad))
	write(filepath.Join("web", "icon.png"), pngBytes(256, winPad))

	// ICNS (macOS): PNG gömülü girdiler.
	entries := []struct {
		typ  string
		size int
	}{
		{"ic11", 32}, {"ic12", 64}, {"ic07", 128}, {"ic13", 256},
		{"ic08", 256}, {"ic14", 512}, {"ic09", 512}, {"ic10", 1024},
	}
	var body bytes.Buffer
	for _, e := range entries {
		data := pngBytes(e.size, macPad)
		body.WriteString(e.typ)
		binary.Write(&body, binary.BigEndian, uint32(8+len(data)))
		body.Write(data)
	}
	var icns bytes.Buffer
	icns.WriteString("icns")
	binary.Write(&icns, binary.BigEndian, uint32(8+body.Len()))
	icns.Write(body.Bytes())
	write(filepath.Join(dir, "meetdesk.icns"), icns.Bytes())

	// ICO (Windows): PNG gömülü girdiler (Vista ve sonrası).
	sizes := []int{16, 20, 24, 32, 40, 48, 64, 128, 256}
	imgs := make([][]byte, len(sizes))
	for i, sz := range sizes {
		imgs[i] = pngBytes(sz, winPad)
	}
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	off := 6 + 16*len(sizes)
	for i, sz := range sizes {
		d := uint8(sz)
		if sz >= 256 {
			d = 0
		}
		ico.Write([]byte{d, d, 0, 0})
		binary.Write(&ico, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&ico, binary.LittleEndian, [2]uint32{uint32(len(imgs[i])), uint32(off)})
		off += len(imgs[i])
	}
	for _, b := range imgs {
		ico.Write(b)
	}
	write(filepath.Join(dir, "meetdesk.ico"), ico.Bytes())
	log.Printf("yazıldı: packaging/meetdesk.{png,icns,ico}, web/icon.png")
}
