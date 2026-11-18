package video

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"remotedesk/internal/capture"
)

func TestFLVRoundTrip(t *testing.T) {
	bin := os.Getenv("FFBIN")
	if bin == "" {
		t.Skip("FFBIN yok")
	}
	for _, enc := range []string{"h264_nvenc", "libx264"} {
		ctx, cancel := context.WithCancel(context.Background())
		e, err := Start(ctx, bin, enc, 1920, 1080, 1920, 1080, 60, 10_000_000)
		if err != nil {
			t.Fatal(err)
		}
		const n = 120
		var mu = make(chan struct{}, 1)
		mu <- struct{}{}
		var fs []Frame
		go func() {
			for f := range e.Frames() {
				<-mu
				fs = append(fs, f)
				mu <- struct{}{}
			}
		}()
		f := &capture.Frame{W: 1920, H: 1080, Stride: 1920 * 4, Pix: make([]byte, 1920*1080*4)}
		for i := range n {
			// kayan metin benzeri desen: her karede değişir
			for y := 0; y < 1080; y++ {
				for x := 0; x < 1920; x += 1 {
					v := byte((x*7 + (y+i*9)*13) ^ (x>>3)*(y>>3))
					o := y*f.Stride + x*4
					f.Pix[o], f.Pix[o+1], f.Pix[o+2], f.Pix[o+3] = v, v^0x55, byte(y+i), 255
				}
			}
			if err := e.WriteFrame(f); err != nil {
				t.Fatal(err)
			}
			time.Sleep(16 * time.Millisecond)
		}
		time.Sleep(2 * time.Second)
		<-mu
		e.Close()
		if len(fs) == 0 {
			t.Fatalf("%s: kare yok: %s", enc, e.ErrText())
		}
		cancel()
		var all []byte
		keys := 0
		for _, fr := range fs {
			if fr.Key {
				keys++
				if !hasSPS(fr.Data) {
					t.Errorf("%s: anahtar karede SPS yok", enc)
				}
			}
			all = append(all, fr.Data...)
		}
		p := t.TempDir() + "/out.h264"
		os.WriteFile(p, all, 0o644)
		out, _ := exec.Command(bin, "-v", "error", "-i", p, "-f", "null", "-").CombinedOutput()
		t.Logf("%s: %d kare, %d anahtar, ilk anahtar=%v, çözme hataları: %q", enc, len(fs), keys, fs[0].Key, strings.TrimSpace(string(out)))
	}
}
