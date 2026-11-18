package video

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

// Frame, tek bir kareye ait H.264 erişim birimidir (Annex B, 4 baytlık başlangıç kodlarıyla).
// Anahtar karelerin başında SPS/PPS bulunur (izleyici çözücüyü bunlarla kurar).
type Frame struct {
	Data []byte
	Key  bool
}

// ffmpeg çıktısı FLV kabındadır: her kare kendi boyutuyla gelir, kare sınırı tahmin edilmez.
// (Ham H.264'te karenin bittiği ancak sonraki kare başlayınca ya da bir sessizlikten
// anlaşılabiliyordu; meşgul bir bilgisayarda sessizlik kareyi ortasından kesebiliyor,
// kesik kare izleyicide sessizce bozuk çiziliyor ve o bölge değişene kadar öyle kalıyordu.)

var errFLV = errors.New("geçersiz FLV akışı")

// readFLV, FLV akışını okur ve her video karesini Annex B olarak emit ile bildirir.
func readFLV(r io.Reader, emit func(Frame)) error {
	br := bufio.NewReaderSize(r, 256<<10)
	var hdr [13]byte // FLV başlığı (9) + PreviousTagSize0 (4)
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return err
	}
	if string(hdr[:3]) != "FLV" {
		return errFLV
	}
	if off := int(binary.BigEndian.Uint32(hdr[5:9])); off > 9 {
		if _, err := br.Discard(off - 9); err != nil {
			return err
		}
	}
	var params []byte // SPS + PPS (Annex B), anahtar karelerin önüne konur
	lenSize := 4
	var th [11]byte
	var body []byte
	for {
		if _, err := io.ReadFull(br, th[:]); err != nil {
			return err
		}
		typ := th[0] & 0x1f
		size := int(th[1])<<16 | int(th[2])<<8 | int(th[3])
		if size > 64<<20 {
			return errFLV
		}
		if cap(body) < size+4 {
			body = make([]byte, size+4)
		}
		body = body[:size+4] // veri + PreviousTagSize
		if _, err := io.ReadFull(br, body); err != nil {
			return err
		}
		d := body[:size]
		if typ != 9 || len(d) < 5 || d[0]&0x0f != 7 { // yalnızca AVC video
			continue
		}
		switch d[1] {
		case 0: // AVCDecoderConfigurationRecord
			if p, ls, ok := parseAVCC(d[5:]); ok {
				params, lenSize = p, ls
			}
		case 1: // NAL birimleri (uzunluk önekli)
			out, key, ok := toAnnexB(d[5:], lenSize)
			if !ok || len(out) == 0 {
				continue
			}
			if key && len(params) > 0 && !hasSPS(out) {
				out = append(append(make([]byte, 0, len(params)+len(out)), params...), out...)
			}
			emit(Frame{Data: out, Key: key})
		}
	}
}

func parseAVCC(b []byte) (params []byte, lenSize int, ok bool) {
	if len(b) < 6 || b[0] != 1 {
		return nil, 0, false
	}
	lenSize = int(b[4]&3) + 1
	i := 5
	for set := 0; set < 2; set++ { // önce SPS'ler, sonra PPS'ler
		if i >= len(b) {
			return nil, 0, false
		}
		n := int(b[i])
		if set == 0 {
			n &= 0x1f
		}
		i++
		for range n {
			if i+2 > len(b) {
				return nil, 0, false
			}
			l := int(binary.BigEndian.Uint16(b[i:]))
			i += 2
			if i+l > len(b) {
				return nil, 0, false
			}
			params = append(params, 0, 0, 0, 1)
			params = append(params, b[i:i+l]...)
			i += l
		}
	}
	return params, lenSize, true
}

func toAnnexB(b []byte, lenSize int) (out []byte, key bool, ok bool) {
	out = make([]byte, 0, len(b)+16)
	for len(b) > 0 {
		if len(b) < lenSize {
			return nil, false, false
		}
		l := 0
		for _, c := range b[:lenSize] {
			l = l<<8 | int(c)
		}
		b = b[lenSize:]
		if l == 0 || l > len(b) {
			return nil, false, false
		}
		nal := b[:l]
		b = b[l:]
		if nal[0]&0x1f == 5 {
			key = true
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, nal...)
	}
	return out, key, true
}

func hasSPS(annexB []byte) bool {
	for i := 0; i+4 < len(annexB); i++ {
		if annexB[i] == 0 && annexB[i+1] == 0 && annexB[i+2] == 1 && annexB[i+3]&0x1f == 7 {
			return true
		}
	}
	return false
}
