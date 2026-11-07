package avatar

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// quadrant is a w x h image, blue, with a red rectangle in its top-left corner (a third of the width, half of the height).
func quadrant(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if x < w/3 && y < h/2 {
				img.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
			} else {
				img.SetNRGBA(x, y, color.NRGBA{B: 255, A: 255})
			}
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// segment is a JPEG marker segment with a payload.
func segment(marker byte, payload []byte) []byte {
	s := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(s[2:], uint16(len(payload)+2))
	return append(s, payload...)
}

// withExif puts an Exif segment with the orientation, a GPS-looking string and a comment right after the SOI of a JPEG.
func withExif(jpg []byte, orientation uint16) []byte {
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08")
	tiff = append(tiff, 0, 1)                         // one entry
	tiff = append(tiff, 0x01, 0x12, 0, 3, 0, 0, 0, 1) // orientation, SHORT, count 1
	tiff = binary.BigEndian.AppendUint16(tiff, orientation)
	tiff = append(tiff, 0, 0)       // value padding
	tiff = append(tiff, 0, 0, 0, 0) // no next IFD
	tiff = append(tiff, []byte("GPS-SECRET-LOCATION 52.5200N 13.4050E")...)
	out := append([]byte{}, jpg[:2]...)
	out = append(out, segment(0xE1, append([]byte("Exif\x00\x00"), tiff...))...)
	out = append(out, segment(0xFE, []byte("camera-serial-0042"))...)
	return append(out, jpg[2:]...)
}

func decodeOut(t *testing.T, out []byte) *image.NRGBA {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("the result is no PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != OutputSide || b.Dy() != OutputSide {
		t.Fatalf("the result is %dx%d, want %dx%d", b.Dx(), b.Dy(), OutputSide, OutputSide)
	}
	n := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			n.Set(x, y, img.At(x, y))
		}
	}
	return n
}

func near(c color.NRGBA, r, g, b, a uint8) bool {
	d := func(x, y uint8) int {
		if x > y {
			return int(x - y)
		}
		return int(y - x)
	}
	return d(c.R, r) < 40 && d(c.G, g) < 40 && d(c.B, b) < 40 && d(c.A, a) < 8
}

func proc() *Processor { return NewProcessor() }

func TestR_API_055_ReEncodesToTheFixedSquare(t *testing.T) {
	// 96 x 64: the centre square is 64 x 64 starting at x = 16, so the red rectangle (x < 32, y < 32) covers the left 16 and
	// the top 32 of the square, which is [0,64) x [0,128) of the 256 x 256 result.
	cases := []struct {
		name, mediaType string
		data            []byte
	}{
		{"PNG", "image/png", encodePNG(t, quadrant(96, 64))},
		{"JPEG", "image/jpeg", encodeJPEG(t, quadrant(96, 64))},
		{"WebP lossy", "image/webp", fixture(t, "lossy.webp")},
		{"WebP lossless", "image/webp", fixture(t, "lossless.webp")},
	}
	for _, c := range cases {
		t.Run("R-API-055 "+c.name, func(t *testing.T) {
			out, err := proc().Process(context.Background(), c.mediaType, c.data)
			if err != nil {
				t.Fatal(err)
			}
			if got := http.DetectContentType(out); got != ContentType {
				t.Fatalf("the result sniffs as %s", got)
			}
			img := decodeOut(t, out)
			if p := img.NRGBAAt(10, 10); !near(p, 255, 0, 0, 255) {
				t.Errorf("(10,10) = %v, want red", p)
			}
			if p := img.NRGBAAt(200, 10); !near(p, 0, 0, 255, 255) {
				t.Errorf("(200,10) = %v, want blue", p)
			}
			if p := img.NRGBAAt(10, 200); !near(p, 0, 0, 255, 255) {
				t.Errorf("(10,200) = %v, want blue", p)
			}
		})
	}
	t.Run("R-API-055 WebP with alpha keeps its transparency", func(t *testing.T) {
		out, err := proc().Process(context.Background(), "image/webp", fixture(t, "alpha.webp"))
		if err != nil {
			t.Fatal(err)
		}
		img := decodeOut(t, out)
		if p := img.NRGBAAt(128, 10); p.A != 255 {
			t.Errorf("(128,10) alpha = %d, want opaque", p.A)
		}
		if p := img.NRGBAAt(128, 240); p.A != 0 {
			t.Errorf("(128,240) alpha = %d, want transparent", p.A)
		}
	})
	t.Run("R-API-055 the same image gives the same bytes", func(t *testing.T) {
		in := encodeJPEG(t, quadrant(96, 64))
		a, err1 := proc().Process(context.Background(), "image/jpeg", in)
		b, err2 := proc().Process(context.Background(), "image/jpeg", in)
		if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
			t.Fatalf("two results differ: %v %v", err1, err2)
		}
	})
	t.Run("R-API-055 a media type parameter does not matter", func(t *testing.T) {
		if _, err := proc().Process(context.Background(), "IMAGE/PNG; charset=binary", encodePNG(t, quadrant(64, 64))); err != nil {
			t.Fatal(err)
		}
	})
}

// chunks lists the chunk types of a PNG and the number of bytes after IEND.
func chunks(t *testing.T, b []byte) (types []string, trailing int) {
	t.Helper()
	off := 8
	for off+8 <= len(b) {
		n := int(binary.BigEndian.Uint32(b[off:]))
		typ := string(b[off+4 : off+8])
		types = append(types, typ)
		off += 12 + n
		if typ == "IEND" {
			return types, len(b) - off
		}
	}
	t.Fatal("no IEND")
	return nil, 0
}

func TestR_API_055_ResultCarriesNoMetadataAndNoAppendedData(t *testing.T) {
	jpg := withExif(encodeJPEG(t, quadrant(64, 64)), 1)
	if !bytes.Contains(jpg, []byte("GPS-SECRET-LOCATION")) {
		t.Fatal("the fixture lost its metadata")
	}
	html := []byte("<html><script>alert(document.cookie)</script></html>")
	// A PNG with a text chunk before the data and a page after the end.
	plain := encodePNG(t, quadrant(64, 64))
	text := pngChunk("tEXt", []byte("Comment\x00GPS-SECRET-LOCATION"))
	tainted := append(append(append([]byte{}, plain[:33]...), text...), plain[33:]...)
	tainted = append(tainted, html...)

	for name, c := range map[string]struct {
		mediaType string
		data      []byte
	}{"JPEG with Exif and a comment": {"image/jpeg", jpg}, "PNG with a text chunk and a page after IEND": {"image/png", tainted}} {
		t.Run("R-API-055 "+name, func(t *testing.T) {
			out, err := proc().Process(context.Background(), c.mediaType, c.data)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range []string{"GPS-SECRET-LOCATION", "camera-serial", "script", "Exif", "tEXt"} {
				if bytes.Contains(out, []byte(s)) {
					t.Errorf("the result still holds %q", s)
				}
			}
			types, trailing := chunks(t, out)
			for _, typ := range types {
				if typ != "IHDR" && typ != "IDAT" && typ != "IEND" {
					t.Errorf("the result has a %s chunk", typ)
				}
			}
			if trailing != 0 {
				t.Errorf("%d bytes follow IEND", trailing)
			}
		})
	}
}

func pngChunk(typ string, data []byte) []byte {
	c := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	c = append(c, typ...)
	c = append(c, data...)
	return binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(append([]byte(typ), data...)))
}

func TestR_API_055_RefusesWhatIsNotAPNGJPEGOrWebP(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64"><script>alert(1)</script></svg>`)
	html := []byte("<!DOCTYPE html><html><body><script>alert(1)</script></body></html>")
	gif := append([]byte("GIF89a\x40\x00\x40\x00\x00\x00\x00"), bytes.Repeat([]byte{0}, 64)...)
	polyglot := append([]byte("GIF89a/*"), []byte("*/=1;alert(1);//"+strings.Repeat(" ", 64))...)
	cases := []struct {
		name, mediaType string
		data            []byte
	}{
		{"SVG declared as SVG", "image/svg+xml", svg},
		{"SVG declared as PNG", "image/png", svg},
		{"HTML declared as PNG", "image/png", html},
		{"HTML declared as HTML", "text/html", html},
		{"GIF declared as GIF", "image/gif", gif},
		{"GIF declared as PNG", "image/png", gif},
		{"GIF and script polyglot declared as JPEG", "image/jpeg", polyglot},
		{"a PNG declared as JSON", "application/json", encodePNG(t, quadrant(64, 64))},
		{"a PNG without a declared type", "", encodePNG(t, quadrant(64, 64))},
		{"text declared as WebP", "image/webp", []byte("RIFF is not enough")},
	}
	for _, c := range cases {
		t.Run("R-API-055 "+c.name, func(t *testing.T) {
			if _, err := proc().Process(context.Background(), c.mediaType, c.data); !errors.Is(err, ErrUnsupportedType) {
				t.Fatalf("err = %v, want ErrUnsupportedType", err)
			}
		})
	}
	t.Run("R-API-055 an image of another accepted format than the declared one", func(t *testing.T) {
		for _, c := range []struct{ declared, data string }{
			{"image/png", "jpeg"}, {"image/jpeg", "png"}, {"image/webp", "png"}, {"image/png", "webp"},
		} {
			in := map[string][]byte{"jpeg": encodeJPEG(t, quadrant(64, 64)), "png": encodePNG(t, quadrant(64, 64)), "webp": fixture(t, "lossy.webp")}[c.data]
			if _, err := proc().Process(context.Background(), c.declared, in); !errors.Is(err, ErrTypeMismatch) {
				t.Errorf("%s declared for %s content: err = %v, want ErrTypeMismatch", c.declared, c.data, err)
			}
		}
	})
	t.Run("R-API-055 an empty body", func(t *testing.T) {
		if _, err := proc().Process(context.Background(), "image/png", nil); !errors.Is(err, ErrEmpty) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestR_API_055_RefusesABodyAboveTheLimit(t *testing.T) {
	valid := encodePNG(t, quadrant(64, 64))
	at := append(append([]byte{}, valid...), make([]byte, MaxUpload-len(valid))...)
	over := append(append([]byte{}, valid...), make([]byte, MaxUpload+1-len(valid))...)
	if _, err := proc().Process(context.Background(), "image/png", over); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("1 MiB + 1: err = %v, want ErrTooLarge", err)
	}
	// A body of exactly the limit is not too large; the zeros after IEND are dropped like any appended data.
	if _, err := proc().Process(context.Background(), "image/png", at); err != nil {
		t.Fatalf("1 MiB: err = %v", err)
	}
}

// pngHeader is a PNG that declares w x h and holds no image data: a decoder that allocated by it would fail, or run out of
// memory, after the allocation.
func pngHeader(w, h uint32) []byte {
	ihdr := binary.BigEndian.AppendUint32(nil, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, 8, 6, 0, 0, 0)
	return append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr)...)
}

func jpegHeader(w, h uint16) []byte {
	sof := []byte{8}
	sof = binary.BigEndian.AppendUint16(sof, h)
	sof = binary.BigEndian.AppendUint16(sof, w)
	sof = append(sof, 1, 1, 0x11, 0)
	// The decoder reads the size from the frame header and reports it at the start of the scan.
	return append(append([]byte{0xFF, 0xD8}, segment(0xC0, sof)...), segment(0xDA, []byte{1, 1, 0, 0, 63, 0})...)
}

func webpHeader(w, h uint32) []byte {
	vp8x := []byte{0, 0, 0, 0}
	vp8x = append(vp8x, byte(w-1), byte((w-1)>>8), byte((w-1)>>16), byte(h-1), byte((h-1)>>8), byte((h-1)>>16))
	body := append([]byte("WEBPVP8X\x0a\x00\x00\x00"), vp8x...)
	b := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(body)))
	return append(b, body...)
}

func TestR_API_055_RefusesTheDimensionsBeforeDecoding(t *testing.T) {
	cases := []struct {
		name, mediaType string
		data            []byte
	}{
		{"PNG of 100,000 x 100,000 pixels in a header", "image/png", pngHeader(100000, 100000)},
		{"JPEG of 65,535 x 65,535 pixels", "image/jpeg", jpegHeader(65535, 65535)},
		{"WebP canvas of 16,000 x 16,000 pixels", "image/webp", webpHeader(16000, 16000)},
		{"PNG one pixel above the area limit", "image/png", pngHeader(2049, 2048)},
		{"PNG below the smallest side", "image/png", encodePNG(t, quadrant(MinSide-1, 200))},
		{"PNG of one pixel", "image/png", encodePNG(t, quadrant(1, 1))},
	}
	for _, c := range cases {
		t.Run("R-API-055 "+c.name, func(t *testing.T) {
			// The headers hold no image data, so only a refusal by the declared size gives ErrDimensions: a decode would give
			// ErrCorrupt.
			if _, err := proc().Process(context.Background(), c.mediaType, c.data); !errors.Is(err, ErrDimensions) {
				t.Fatalf("err = %v, want ErrDimensions", err)
			}
		})
	}
	t.Run("R-API-055 the largest accepted area is accepted", func(t *testing.T) {
		if _, err := proc().Process(context.Background(), "image/png", encodePNG(t, quadrant(2048, 2048))); err != nil {
			t.Fatal(err)
		}
	})
}

func TestR_API_055_RefusesAnImageThatDoesNotDecode(t *testing.T) {
	pngData := encodePNG(t, quadrant(96, 64))
	jpgData := encodeJPEG(t, quadrant(96, 64))
	webp := fixture(t, "lossy.webp")
	cases := []struct {
		name, mediaType string
		data            []byte
	}{
		{"PNG cut in the data", "image/png", pngData[:len(pngData)/2]},
		{"PNG cut in the header", "image/png", pngData[:20]},
		{"JPEG cut in the data", "image/jpeg", jpgData[:len(jpgData)/2]},
		{"JPEG cut in the header", "image/jpeg", jpgData[:12]},
		{"WebP cut in the data", "image/webp", webp[:len(webp)-40]},
		{"WebP cut in the header", "image/webp", webp[:16]},
	}
	for _, c := range cases {
		t.Run("R-API-055 "+c.name, func(t *testing.T) {
			if _, err := proc().Process(context.Background(), c.mediaType, c.data); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestR_API_055_RefusesAnimatedImages(t *testing.T) {
	for name, c := range map[string]struct{ mediaType, file string }{
		"animated PNG": {"image/png", "animated.png"}, "animated WebP": {"image/webp", "animated.webp"},
	} {
		t.Run("R-API-055 "+name, func(t *testing.T) {
			if _, err := proc().Process(context.Background(), c.mediaType, fixture(t, c.file)); !errors.Is(err, ErrAnimated) {
				t.Fatalf("err = %v, want ErrAnimated", err)
			}
		})
	}
}

func TestR_API_055_AppliesTheExifOrientationOfAPhoto(t *testing.T) {
	// The stored image has the red corner top left. Orientation 6 says "turn 90 degrees clockwise to display", so the corner
	// belongs top right; 3 turns it by 180 degrees, to the bottom right; 8 turns it counter-clockwise, to the bottom left.
	src := encodeJPEG(t, quadrant(96, 96))
	for orientation, want := range map[uint16][2]int{1: {10, 10}, 6: {245, 10}, 3: {245, 245}, 8: {10, 245}} {
		t.Run("R-API-055 orientation "+string(rune('0'+orientation)), func(t *testing.T) {
			out, err := proc().Process(context.Background(), "image/jpeg", withExif(src, orientation))
			if err != nil {
				t.Fatal(err)
			}
			img := decodeOut(t, out)
			// The red rectangle is a third of the width and half of the height, so probe well inside it in the turned image.
			x, y := want[0], want[1]
			if p := img.NRGBAAt(x, y); !near(p, 255, 0, 0, 255) {
				t.Errorf("(%d,%d) = %v, want red", x, y, p)
			}
			if p := img.NRGBAAt(128, 128); !near(p, 0, 0, 255, 255) {
				t.Errorf("(128,128) = %v, want blue", p)
			}
		})
	}
	t.Run("R-API-055 a damaged Exif block is no orientation", func(t *testing.T) {
		jpg := encodeJPEG(t, quadrant(96, 96))
		bad := append(append([]byte{}, jpg[:2]...), segment(0xE1, []byte("Exif\x00\x00MM\x00\x2a\xff\xff\xff\xff"))...)
		bad = append(bad, jpg[2:]...)
		out, err := proc().Process(context.Background(), "image/jpeg", bad)
		if err != nil {
			t.Fatal(err)
		}
		if p := decodeOut(t, out).NRGBAAt(10, 10); !near(p, 255, 0, 0, 255) {
			t.Errorf("(10,10) = %v, want red", p)
		}
	})
}

func FuzzR_API_055_ExifOrientation(f *testing.F) {
	jpg := withExif(append([]byte{0xFF, 0xD8}, 0xFF, 0xD9), 6)
	f.Add(jpg)
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x04, 'E', 'x'})
	f.Add([]byte("MM\x00\x2a\x00\x00\x00\x08"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if o := exifOrientation(data); o < 1 || o > 8 {
			t.Fatalf("orientation %d", o)
		}
	})
}

func TestR_API_055_BoundsTheImagesDecodedAtOnce(t *testing.T) {
	in := encodePNG(t, quadrant(64, 64))
	t.Run("R-API-055 a request without a free slot is refused after the wait", func(t *testing.T) {
		p := newProcessor(1, 30*time.Millisecond)
		p.slots <- struct{}{}
		start := time.Now()
		if _, err := p.Process(context.Background(), "image/png", in); !errors.Is(err, ErrBusy) {
			t.Fatalf("err = %v, want ErrBusy", err)
		}
		if time.Since(start) < 30*time.Millisecond {
			t.Error("the request did not wait")
		}
		<-p.slots
		if _, err := p.Process(context.Background(), "image/png", in); err != nil {
			t.Fatalf("after the slot was freed: %v", err)
		}
		if len(p.slots) != 0 {
			t.Error("the slot was not given back")
		}
	})
	t.Run("R-API-055 the wait ends with the request", func(t *testing.T) {
		p := newProcessor(1, time.Minute)
		p.slots <- struct{}{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := p.Process(ctx, "image/png", in); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
	t.Run("R-API-055 a refused image takes no slot", func(t *testing.T) {
		p := newProcessor(1, time.Minute)
		p.slots <- struct{}{}
		if _, err := p.Process(context.Background(), "image/png", pngHeader(100000, 100000)); !errors.Is(err, ErrDimensions) {
			t.Fatalf("err = %v, want ErrDimensions without waiting for a slot", err)
		}
	})
}
