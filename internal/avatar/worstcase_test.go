package avatar

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"image"
	"image/jpeg"
	"math/bits"
	"os"
	"slices"
	"sync"
	"testing"
	"time"
)

// The inputs of this file are the slowest images the checks of Process accept that the authors could build, in code and
// deterministic: what fits in MaxUpload, has the area MaxPixels allows, whose estimated memory is inside MaxDecodeBytes and whose
// scans add up to the block visits MaxBlockVisits allows, with the content that costs a decoder the most per pixel (filters on
// every row, interlacing, 16 bits per channel, noise, many scans, scans that carry nothing). R-API-055 bounds the time of a
// decode by DecodeTimeout; the tests here keep the slowest of them inside a fraction of it.

// xorshift is a deterministic source of noise; the inputs are the same on every run.
type xorshift uint64

func (x *xorshift) next() uint64 {
	*x ^= *x << 13
	*x ^= *x >> 7
	*x ^= *x << 17
	return uint64(*x)
}

// adam7 is the passes of the interlacing: the first column and row, and the step in each direction.
var adam7 = [7][4]int{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}}

// worstPNG is a PNG of side x side pixels, RGBA with the given bits per channel, optionally Adam7 interlaced, whose every
// row uses the Paeth filter (the slowest) and whose filtered bytes are sparse noise: a byte is non-zero with the probability
// density/1024. The noise is what keeps the deflate stream made of literals and short matches, which inflates slowest; the
// density is chosen so the file fits MaxUpload. The filter bytes are not computed from an image: the decoder undoes whatever
// the bytes say, and the bytes are what is compressed.
func worstPNG(t testing.TB, side, bits int, interlaced bool, density uint64) []byte {
	t.Helper()
	rng := xorshift(0x9E3779B97F4A7C15)
	var raw bytes.Buffer
	row := func(pixels int) {
		raw.WriteByte(4)
		for range pixels * 4 * bits / 8 {
			if rng.next()%1024 < density {
				raw.WriteByte(byte(rng.next()))
			} else {
				raw.WriteByte(0)
			}
		}
	}
	if interlaced {
		for _, p := range adam7 {
			pw, ph := (side-p[0]+p[2]-1)/p[2], (side-p[1]+p[3]-1)/p[3]
			for range ph {
				row(pw)
			}
		}
	} else {
		for range side {
			row(side)
		}
	}
	var z bytes.Buffer
	zw, err := zlib.NewWriterLevel(&z, zlib.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	ihdr := binary.BigEndian.AppendUint32(nil, uint32(side))
	ihdr = binary.BigEndian.AppendUint32(ihdr, uint32(side))
	il := byte(0)
	if interlaced {
		il = 1
	}
	ihdr = append(ihdr, byte(bits), 6, 0, 0, il)
	out := append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr)...)
	out = append(out, pngChunk("IDAT", z.Bytes())...)
	return append(out, pngChunk("IEND", nil)...)
}

// bitWriter writes the entropy-coded data of a JPEG scan: bits most significant first, a zero byte after every 0xFF, the
// last byte padded with ones.
type bitWriter struct {
	out  []byte
	acc  uint32
	bits uint
}

func (w *bitWriter) put(code uint32, n uint) {
	w.acc = w.acc<<n | code&(1<<n-1)
	w.bits += n
	for w.bits >= 8 {
		b := byte(w.acc >> (w.bits - 8))
		w.out = append(w.out, b)
		if b == 0xFF {
			w.out = append(w.out, 0)
		}
		w.bits -= 8
	}
}

func (w *bitWriter) flush() []byte {
	if w.bits > 0 {
		w.put(1<<(8-w.bits)-1, 8-w.bits)
	}
	return w.out
}

// The Huffman tables of worstJPEG. The DC table has twelve codes of four bits (the category is the code). The AC table gives
// the end of the band the one-bit code 0 and every other symbol a nine-bit code, so a block costs one bit when it has no
// coefficient in the band and the table needs no knowledge of the values; a coefficient is the symbol run<<4|size.
func huffmanSegments() []byte {
	dc := []byte{0x00, 0, 0, 0, 12, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	for s := range 12 {
		dc = append(dc, byte(s))
	}
	ac := []byte{0x10, 1, 0, 0, 0, 0, 0, 0, 0, 255, 0, 0, 0, 0, 0, 0, 0}
	for s := range 256 {
		ac = append(ac, byte(s))
	}
	return append(segment(0xC4, dc), segment(0xC4, ac)...)
}

// worstJPEG is a progressive JPEG of side x side pixels with the given number of components, all sampled at full resolution,
// and scans scans: one for the DC coefficients of all components, then the AC coefficients of each component in bands, the
// bands spread over the scans. A block holds coefficients in about the proportion nonzero/63 of the positions of a band.
// Every scan walks every block of its component, so the cost grows with the scans (MaxScans bounds it).
func worstJPEG(t testing.TB, side, components, scans, nonzero int) []byte {
	t.Helper()
	if (scans-1)%components != 0 {
		t.Fatalf("scans = %d: one DC scan and a whole number of bands per component", scans)
	}
	bandsPerComponent := (scans - 1) / components
	blocks := (side + 7) / 8 * ((side + 7) / 8)
	rng := xorshift(0x2545F4914F6CDD1D)

	dqt := append([]byte{0}, bytes.Repeat([]byte{1}, 64)...)
	frame := []byte{8, byte(side >> 8), byte(side), byte(side >> 8), byte(side), byte(components)}
	for c := range components {
		frame = append(frame, byte(c+1), 0x11, 0)
	}
	out := append([]byte{0xFF, 0xD8}, segment(0xDB, dqt)...)
	out = append(out, segment(0xC2, frame)...)
	out = append(out, huffmanSegments()...)

	// the DC scan: every component's first coefficient of every block, interleaved
	sos := []byte{byte(components)}
	for c := range components {
		sos = append(sos, byte(c+1), 0x00)
	}
	out = append(out, segment(0xDA, append(sos, 0, 0, 0))...)
	var w bitWriter
	for range blocks * components {
		cat := uint32(rng.next() % 6)
		w.put(cat, 4)
		w.put(uint32(rng.next()), uint(cat))
	}
	out = append(out, w.flush()...)

	// the AC scans of each component, the 63 positions cut into bands
	for c := range components {
		for b := range bandsPerComponent {
			ss := 1 + b*63/bandsPerComponent
			se := (b + 1) * 63 / bandsPerComponent
			out = append(out, segment(0xDA, []byte{1, byte(c + 1), 0x00, byte(ss), byte(se), 0})...)
			var w bitWriter
			for range blocks {
				n := 0
				for k := ss; k <= se; k++ {
					if int(rng.next()%63) < nonzero {
						w.put(256, 9) // the symbol run 0, size 1: the codes of the nine-bit symbols start at 256
						w.put(uint32(rng.next()), 1)
						n++
					}
				}
				if n < se-ss+1 {
					w.put(0, 1) // end of band
				}
			}
			out = append(out, w.flush()...)
		}
	}
	return append(out, 0xFF, 0xD9)
}

// worstBaselineJPEG is a baseline JPEG of noise, 4:2:0, of the highest quality that still fits MaxUpload: the Huffman
// decoder has the most symbols to read when the quantiser keeps the most coefficients.
func worstBaselineJPEG(t testing.TB, side int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	rng := xorshift(0xD1B54A32D192ED03)
	for i := 0; i < len(img.Pix); i += 4 {
		v := rng.next()
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(v), byte(v>>8), byte(v>>16), 255
	}
	var best []byte
	lo, hi := 1, 100
	for lo <= hi {
		q := (lo + hi) / 2
		var b bytes.Buffer
		if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: q}); err != nil {
			t.Fatal(err)
		}
		if b.Len() <= MaxUpload {
			best, lo = b.Bytes(), q+1
		} else {
			hi = q - 1
		}
	}
	return best
}

// lsbWriter writes bits least significant first, the order of a WebP lossless stream.
type lsbWriter struct {
	out  []byte
	acc  uint64
	bits uint
}

func (w *lsbWriter) put(v uint64, n uint) {
	w.acc |= v & (1<<n - 1) << w.bits
	w.bits += n
	for w.bits >= 8 {
		w.out = append(w.out, byte(w.acc))
		w.acc >>= 8
		w.bits -= 8
	}
}

// worstWebP is a lossless WebP (VP8L) of side x side pixels with no transform and no colour cache, whose green channel has two
// symbols, so every pixel costs one bit of noise, and whose other channels have one symbol and cost none. It is the densest
// pixel stream that fits MaxUpload (a pixel with more than one bit does not at this area); the lossy format has no encoder
// to build from.
func worstWebP(side int) []byte {
	var w lsbWriter
	w.put(0x2f, 8)
	w.put(uint64(side-1), 14)
	w.put(uint64(side-1), 14)
	w.put(1, 1) // alpha is used
	w.put(0, 3) // version
	w.put(0, 1) // no transform
	w.put(0, 1) // no colour cache
	w.put(0, 1) // one group of prefix codes
	// green: a simple code of two symbols (8 bits each)
	w.put(1, 1)
	w.put(1, 1)
	w.put(1, 1)
	w.put(10, 8)
	w.put(200, 8)
	// red, blue, alpha: a simple code of one symbol (8 bits)
	for _, s := range []uint64{30, 60, 255} {
		w.put(1, 1)
		w.put(0, 1)
		w.put(1, 1)
		w.put(s, 8)
	}
	// distance: a simple code of one symbol (1 bit)
	w.put(1, 1)
	w.put(0, 1)
	w.put(0, 1)
	w.put(0, 1)
	rng := xorshift(0x8BB84B93962EACC9)
	for range side * side / 64 {
		w.put(rng.next(), 64-1)
		w.put(rng.next(), 1)
	}
	if w.bits > 0 {
		w.put(0, 8-w.bits)
	}
	chunk := append([]byte("VP8L"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(chunk[4:], uint32(len(w.out)))
	chunk = append(append(chunk, w.out...), make([]byte, len(w.out)%2)...)
	b := append([]byte("RIFF"), 0, 0, 0, 0)
	b = append(b, "WEBP"...)
	b = append(b, chunk...)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	return b
}

type worstCase struct {
	name, mediaType string
	data            []byte
}

// largestProgressiveSide is the largest side of a square progressive JPEG with the given components that the memory
// estimate of Process accepts, a multiple of 8.
func largestProgressiveSide(components int) int {
	side := 2048
	for ; side > MinSide; side -= 8 {
		cfg := image.Config{Width: side, Height: side}
		if decodeCost(formatJPEG, cfg, jpegShape{progressive: true, components: components}) <= MaxDecodeBytes {
			break
		}
	}
	return side
}

// A scan of eobJPEG.
type eobScan struct {
	comps  []int // the components it carries (components counted from 0)
	refine bool  // an AC refinement scan of band 1 to 63 of comps[0]; else a DC scan, the first when no scan before it was one
}

// eobJPEG is a progressive JPEG of side x side pixels whose scans carry nothing: a DC scan costs one bit a block, an AC scan one
// end-of-band run for all the blocks of its component, a few bytes. What it costs is the decoder's walk over the blocks, once
// for every scan, which is the work MaxBlockVisits bounds. A frame with four components carries the Adobe marker the decoder
// needs for it. Go checks only that a refinement scan follows a scan with one bit less, so the scans repeat.
func eobJPEG(t testing.TB, side, components int, plan []eobScan) []byte {
	t.Helper()
	blocks := (side + 7) / 8 * ((side + 7) / 8)
	dqt := append([]byte{0}, bytes.Repeat([]byte{1}, 64)...)
	frame := []byte{8, byte(side >> 8), byte(side), byte(side >> 8), byte(side), byte(components)}
	for c := range components {
		frame = append(frame, byte(c+1), 0x11, 0)
	}
	out := append([]byte{0xFF, 0xD8}, segment(0xDB, dqt)...)
	if components == 4 {
		out = append(out, segment(0xEE, []byte("Adobe\x00\x64\x00\x00\x00\x00\x00"))...)
	}
	out = append(out, segment(0xC2, frame)...)
	// DC table: one code of one bit, the category 0. AC table: fifteen codes of four bits, the end-of-band runs r<<4.
	out = append(out, segment(0xC4, []byte{0x00, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})...)
	ac := []byte{0x10, 0, 0, 0, 15, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	for r := range 15 {
		ac = append(ac, byte(r<<4))
	}
	out = append(out, segment(0xC4, ac)...)
	firstDC := true
	for _, sc := range plan {
		var w bitWriter
		var sos []byte
		if sc.refine {
			sos = []byte{1, byte(sc.comps[0] + 1), 0x00, 1, 63, 0x10}
			for left := blocks; left > 0; {
				run := min(left, 32767)
				v := bits.Len(uint(run)) - 1
				w.put(uint32(v), 4)
				if v > 0 {
					w.put(uint32(run-1<<v), uint(v))
				}
				left -= run
			}
		} else {
			sos = []byte{byte(len(sc.comps))}
			for _, c := range sc.comps {
				sos = append(sos, byte(c+1), 0x00)
			}
			ah := byte(1)
			if firstDC {
				ah, firstDC = 0, false
			}
			sos = append(sos, 0, 0, ah<<4)
			for range blocks * len(sc.comps) {
				w.put(0, 1)
			}
		}
		out = append(out, segment(0xDA, sos)...)
		out = append(out, w.flush()...)
	}
	return append(out, 0xFF, 0xD9)
}

// eobPlan is the scans of a JPEG of side x side pixels that spend MaxBlockVisits as far as they go, without passing MaxScans:
// one interleaved DC scan over all components, then more of the same kind, or AC refinement scans over the components in turn.
func eobPlan(side, components int, refine bool) []eobScan {
	blocks := int64((side + 7) / 8 * ((side + 7) / 8))
	all := make([]int, components)
	for c := range all {
		all[c] = c
	}
	var plan []eobScan
	var visits int64
	for len(plan) < MaxScans {
		sc := eobScan{comps: all}
		cost := int64(components) * blocks
		if refine && len(plan) > 0 {
			sc = eobScan{comps: []int{len(plan) % components}, refine: true}
			cost = 2 * blocks
		}
		if visits += cost; visits > MaxBlockVisits {
			break
		}
		plan = append(plan, sc)
	}
	return plan
}

// worstCases returns the slowest accepted images per format. The densities and the quality are the largest that keep the file
// inside MaxUpload, found once by hand; TestR_API_055_TheWorstCaseInputsAreAccepted fails when one no longer fits.
func worstCases(t testing.TB) []worstCase {
	t.Helper()
	worstOnce.Do(func() { worstAll = buildWorstCases(t) })
	if len(worstAll) != worstCaseCount {
		t.Fatalf("%d inputs were built, want %d", len(worstAll), worstCaseCount)
	}
	return worstAll
}

var (
	worstOnce sync.Once
	worstAll  []worstCase
)

const worstCaseCount = 14

func buildWorstCases(t testing.TB) []worstCase {
	rgb, cmyk := largestProgressiveSide(3), largestProgressiveSide(4)
	return []worstCase{
		{"PNG 16-bit RGBA 2048x2048 interlaced Paeth", "image/png", worstPNG(t, 2048, 16, true, worstDensity16)},
		{"PNG 16-bit RGBA 2048x2048 Paeth", "image/png", worstPNG(t, 2048, 16, false, worstDensity16)},
		{"PNG 8-bit RGBA 2048x2048 interlaced Paeth", "image/png", worstPNG(t, 2048, 8, true, worstDensity8)},
		{"WebP lossless 2048x2048 one bit a pixel", "image/webp", worstWebP(2048)},
		{"JPEG baseline 2048x2048 noise", "image/jpeg", worstBaselineJPEG(t, 2048)},
		{"JPEG progressive gray 2048x2048 10 scans", "image/jpeg", worstJPEG(t, 2048, 1, 10, worstNonzeroGray10)},
		{"JPEG progressive gray 2048x2048 dense at the visit limit", "image/jpeg", worstJPEG(t, 2048, 1, worstGrayScans, worstNonzeroGray64)},
		{"JPEG progressive 3 components largest 10 scans", "image/jpeg", worstJPEG(t, rgb, 3, 10, worstNonzeroRGB10)},
		{"JPEG progressive 3 components largest 64 scans", "image/jpeg", worstJPEG(t, rgb, 3, 64, worstNonzeroRGB64)},
		{"JPEG progressive gray 2048x2048 AC refinement runs at the visit limit", "image/jpeg", eobJPEG(t, 2048, 1, eobPlan(2048, 1, true))},
		{"JPEG progressive gray 2048x2048 DC scans at the visit limit", "image/jpeg", eobJPEG(t, 2048, 1, eobPlan(2048, 1, false))},
		{"JPEG progressive 3 components largest DC scans at the visit limit", "image/jpeg", eobJPEG(t, rgb, 3, eobPlan(rgb, 3, false))},
		{"JPEG progressive 3 components largest AC refinement runs at the visit limit", "image/jpeg", eobJPEG(t, rgb, 3, eobPlan(rgb, 3, true))},
		{"JPEG progressive 4 components largest DC scans at the visit limit", "image/jpeg", eobJPEG(t, cmyk, 4, eobPlan(cmyk, 4, false))},
	}
}

// What keeps each input inside MaxUpload, and the scans of the dense gray file that spends the visits (one DC scan and 47 AC
// scans of 65,536 blocks are 48 x 65,536 = MaxBlockVisits).
const (
	worstDensity16     = 10
	worstDensity8      = 23
	worstNonzeroGray10 = 11
	worstNonzeroGray64 = 3
	worstNonzeroRGB10  = 5
	worstNonzeroRGB64  = 2
	worstGrayScans     = 48
)

func TestR_API_055_TheWorstCaseInputsAreAccepted(t *testing.T) {
	if raceDetector {
		// building them (32 MiB of noise, deflated at the best level, a 2048 x 2048 JPEG searched for its quality) takes
		// minutes under the race detector; `make test-performance` runs this test without it
		t.Skip("the inputs are built without the race detector")
	}
	for _, c := range worstCases(t) {
		t.Run("R-API-055 "+c.name, func(t *testing.T) {
			t.Logf("%d bytes of %d", len(c.data), MaxUpload)
			if len(c.data) > MaxUpload {
				t.Fatalf("%d bytes: above MaxUpload", len(c.data))
			}
			// the functional part: every check accepts the image and it decodes; its time is judged below
			p := proc()
			p.decodeTimeout = time.Minute
			out, err := p.Process(context.Background(), c.mediaType, c.data)
			if err != nil {
				t.Fatal(err)
			}
			decodeOut(t, out)
		})
	}
}

// cpuBudget is the CPU time the slowest accepted image may cost with TROVE_PERF_GATE=1 (Q-UI-174). Two decodes at a time
// (MaxConcurrent) share the CPU a pod has, so a pod needs twice the CPU one decode needs.
const cpuBudget = time.Second

// gateSamples is the number of decodes of each input under TROVE_PERF_GATE=1; the inputs are built once.
const gateSamples = 7

// TestR_API_055_TheSlowestAcceptedImagesDecodeInsideTheDeadline is the property behind DecodeTimeout: no image the checks
// accept needs more than a fraction of it. The race detector slows a decoder beyond the bound of production (Test and Release
// run 42409044656: 9.7 s for 2048 x 2048), so the per-change tier, which runs with it, judges nothing here. Without it each
// input is decoded with a deadline of a minute, so that a slow decode (a busy host) is a reading and not a refusal, and wall
// and CPU time are logged. With TROVE_PERF_GATE=1 (the nightly performance run) each input is decoded gateSamples times and
// the least CPU time must be inside cpuBudget. Only the CPU time is judged: the wall time of a decode on a shared runner is
// the runner's load (each fresh decode also pays a collection of the 40 MB it allocates), which is the lesson of Q-ARCH-220;
// the least of several CPU readings is the cost of the image, and the one that H-641 turns into the CPU a pod needs.
func TestR_API_055_TheSlowestAcceptedImagesDecodeInsideTheDeadline(t *testing.T) {
	if raceDetector {
		t.Skip("the race detector slows the decoders beyond the production bound and the inputs take minutes to build; judged without it")
	}
	gate := os.Getenv("TROVE_PERF_GATE") == "1"
	samples := 1
	if gate {
		samples = gateSamples
	}
	for _, c := range worstCases(t) {
		t.Run("R-API-055 "+c.name, func(t *testing.T) {
			wall := make([]time.Duration, 0, samples)
			leastCPU := time.Duration(1<<63 - 1)
			for range samples {
				p := proc()
				p.decodeTimeout = time.Minute
				cpu, start := cpuTime(), time.Now()
				if _, err := p.Process(context.Background(), c.mediaType, c.data); err != nil {
					t.Fatal(err)
				}
				wall = append(wall, time.Since(start))
				leastCPU = min(leastCPU, cpuTime()-cpu)
			}
			slices.Sort(wall)
			t.Logf("%d decodes: median wall %s, least CPU %s", samples, wall[len(wall)/2], leastCPU)
			if gate && leastCPU > cpuBudget {
				t.Errorf("least CPU time %s of %d decodes is above %s", leastCPU, samples, cpuBudget)
			}
		})
	}
}
