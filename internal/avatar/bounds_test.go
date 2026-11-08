package avatar

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"testing"
	"time"
)

// jpegFrame is a JPEG whose markers say: SOI, a frame header of the given type, size and component count, and the given number
// of scans, each a scan header followed by a few bytes of entropy-coded data (with a stuffed byte and a restart marker, which
// the walk must step over), then EOI. It is not decodable; it is what the marker walk and the header checks read.
func jpegFrame(sof byte, w, h uint16, components, scans int) []byte {
	frame := []byte{8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), byte(components)}
	for i := range components {
		frame = append(frame, byte(i+1), 0x11, 0)
	}
	b := append([]byte{0xFF, 0xD8}, segment(sof, frame)...)
	for range scans {
		b = append(b, segment(0xDA, []byte{1, 1, 0, 1, 63, 0})...)
		b = append(b, 0x12, 0xFF, 0x00, 0x34, 0xFF, 0xD0, 0x56)
	}
	return append(b, 0xFF, 0xD9)
}

func TestR_API_055_RefusesAJPEGWithManyScans(t *testing.T) {
	t.Run("R-API-055 the walk counts the scans and steps over entropy-coded data", func(t *testing.T) {
		sh, err := walkJPEG(jpegFrame(0xC2, 2048, 2048, 3, 10))
		if err != nil || sh.scans != 10 || !sh.progressive || sh.components != 3 {
			t.Fatalf("shape %+v, err %v", sh, err)
		}
		sh, err = walkJPEG(jpegFrame(0xC0, 64, 64, 1, 1))
		if err != nil || sh.scans != 1 || sh.progressive || sh.components != 1 {
			t.Fatalf("shape %+v, err %v", sh, err)
		}
	})
	t.Run("R-API-055 the limit is MaxScans", func(t *testing.T) {
		if _, err := walkJPEG(jpegFrame(0xC2, 1024, 1024, 3, MaxScans)); err != nil {
			t.Fatalf("%d scans: %v", MaxScans, err)
		}
		if _, err := walkJPEG(jpegFrame(0xC2, 1024, 1024, 3, MaxScans+1)); !errors.Is(err, ErrComplex) {
			t.Fatalf("%d scans: %v, want ErrComplex", MaxScans+1, err)
		}
	})
	t.Run("R-API-055 a 2048 x 2048 progressive JPEG with 65,000 scans is refused before a slot or a decode", func(t *testing.T) {
		bomb := jpegFrame(0xC2, 2048, 2048, 3, 65000)
		// 65,000 scans of 15 bytes are about 1 MiB: the upload limit does not stop it, the scan limit does.
		if len(bomb) > MaxUpload {
			bomb = bomb[:MaxUpload]
		}
		p := newProcessor(1, time.Minute)
		p.slots <- struct{}{} // no free slot: a request that reached the slot would wait for it
		start := time.Now()
		_, err := p.Process(context.Background(), "image/jpeg", bomb)
		if !errors.Is(err, ErrComplex) {
			t.Fatalf("err = %v", err)
		}
		if time.Since(start) > time.Second {
			t.Fatalf("the refusal took %s", time.Since(start))
		}
	})
	t.Run("R-API-055 a truncated marker structure ends the walk without a panic", func(t *testing.T) {
		full := jpegFrame(0xC2, 64, 64, 3, 3)
		for i := range full {
			_, _ = walkJPEG(full[:i])
		}
	})
}

func TestR_API_055_StopsADecodeAtItsDeadline(t *testing.T) {
	in := encodePNG(t, quadrant(256, 256))
	t.Run("R-API-055 an expired deadline stops the decode at its next read", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		ctx, stop := context.WithTimeout(parent, time.Nanosecond)
		defer stop()
		time.Sleep(time.Millisecond)
		// decode derives its own timeout from the context it is given; a context that is already done ends it at once.
		if _, err := decode(ctx, formatPNG, in, DecodeTimeout); err == nil {
			t.Fatal("a decode with an expired context succeeded")
		}
	})
	t.Run("R-API-055 the end of the request is the request's error, not a refusal of the image", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := decode(ctx, formatPNG, in, DecodeTimeout); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
	t.Run("R-API-055 the deadline of the decode alone is ErrComplex", func(t *testing.T) {
		r := &ctxReader{ctx: expired(t), r: bytes.NewReader(in)}
		if _, err := r.Read(make([]byte, 8)); err == nil {
			t.Fatal("a read after the deadline succeeded")
		}
		if err := decodeStopped(context.Background(), context.DeadlineExceeded); !errors.Is(err, ErrComplex) {
			t.Fatalf("err = %v, want ErrComplex", err)
		}
	})
}

func expired(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	t.Cleanup(cancel)
	time.Sleep(time.Millisecond)
	return ctx
}

func TestR_API_055_BoundsTheMemoryOfADecodeFromTheHeader(t *testing.T) {
	const mib = 1 << 20
	side := func(n int) image.Config { return image.Config{Width: n, Height: n, ColorModel: color.NRGBAModel} }
	scaler := func(n int) int64 { return int64(OutputSide) * int64(n) * 32 }
	px := func(n int) int64 { return int64(n) * int64(n) }

	cases := []struct {
		name string
		f    format
		cfg  image.Config
		sh   jpegShape
		want int64
	}{
		{"PNG, 8 bits", formatPNG, side(2048), jpegShape{}, px(2048)*4 + scaler(2048)},
		{"PNG, 16 bits", formatPNG, image.Config{Width: 2048, Height: 2048, ColorModel: color.NRGBA64Model}, jpegShape{}, px(2048)*8 + scaler(2048)},
		{"WebP", formatWebP, side(2048), jpegShape{}, px(2048)*8 + scaler(2048)},
		{"baseline JPEG, 3 components", formatJPEG, side(2048), jpegShape{components: 3, scans: 1}, px(2048)*3 + scaler(2048)},
		{"baseline JPEG, 4 components", formatJPEG, side(2048), jpegShape{components: 4, scans: 1}, px(2048)*4 + scaler(2048)},
		{"progressive JPEG, 3 components", formatJPEG, side(2048), jpegShape{progressive: true, components: 3, scans: 10}, px(2048)*15 + scaler(2048)},
		{"progressive JPEG, 4 components", formatJPEG, side(2048), jpegShape{progressive: true, components: 4, scans: 10}, px(2048)*20 + scaler(2048)},
		{"a wide image is scaled from its short side", formatPNG, image.Config{Width: 4096, Height: 1024, ColorModel: color.NRGBAModel}, jpegShape{}, 4096*1024*4 + scaler(1024)},
	}
	for _, c := range cases {
		t.Run("R-API-055 the estimate for "+c.name, func(t *testing.T) {
			if got := decodeCost(c.f, c.cfg, c.sh); got != c.want {
				t.Errorf("cost = %d, want %d", got, c.want)
			}
		})
	}
	t.Run("R-API-055 the largest accepted PNG and the largest baseline JPEG are inside the bound", func(t *testing.T) {
		for _, c := range cases[:5] {
			if got := decodeCost(c.f, c.cfg, c.sh); got > MaxDecodeBytes {
				t.Errorf("%s: %d MiB is above the bound of %d MiB", c.name, got/mib, MaxDecodeBytes/mib)
			}
		}
	})
	t.Run("R-API-055 a progressive or four-component image of the full area is above the bound", func(t *testing.T) {
		for _, c := range cases[5:7] {
			if got := decodeCost(c.f, c.cfg, c.sh); got <= MaxDecodeBytes {
				t.Errorf("%s: %d MiB is inside the bound", c.name, got/mib)
			}
		}
	})
	t.Run("R-API-055 two decodes at the bound are 96 MiB, the number the documents give", func(t *testing.T) {
		if MaxConcurrent*MaxDecodeBytes != 96*mib {
			t.Errorf("%d MiB", MaxConcurrent*MaxDecodeBytes/mib)
		}
	})
	t.Run("R-API-055 Process refuses a progressive JPEG above the bound and takes one below it, by the header", func(t *testing.T) {
		p := proc()
		over := jpegFrame(0xC2, 2048, 2048, 3, 10)
		// No JFIF marker: DecodeConfig reports the size at the first scan, which the frame has.
		if _, err := p.Process(context.Background(), "image/jpeg", over); !errors.Is(err, ErrDimensions) {
			t.Fatalf("2048 x 2048 progressive: %v, want ErrDimensions", err)
		}
		cmyk := jpegFrame(0xC2, 1900, 1900, 4, 10)
		if _, err := p.Process(context.Background(), "image/jpeg", cmyk); !errors.Is(err, ErrDimensions) {
			t.Fatalf("1900 x 1900 progressive CMYK: %v, want ErrDimensions", err)
		}
		below := jpegFrame(0xC2, 1400, 1400, 3, 10)
		if _, err := p.Process(context.Background(), "image/jpeg", below); errors.Is(err, ErrDimensions) {
			t.Fatalf("1400 x 1400 progressive was refused by size: %v", err)
		}
	})
}
