// Package avatar is the user avatar of R-API-055: the processing of an image a user uploads, and the store that keeps the
// result.
//
// The image is untrusted input. Process never stores or serves a byte of it: it decides the format by the content, bounds the
// dimensions before it decodes, decodes with the Go decoders, takes the centre square, scales it to a fixed size and encodes
// a PNG from the pixels alone. What leaves Process has no metadata, no appended data and no second frame, whatever the upload
// carried.
package avatar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// The bounds of R-API-055 (Q-API-208). They are constants of the product: an operator cannot widen the input of an image
// decoder by configuration.
const (
	// MaxUpload is the largest request body Process takes, in bytes.
	MaxUpload = 1 << 20
	// OutputSide is the side of the stored square, in pixels. The UI shows the avatar at 32 px and at 96 px; 256 px keeps
	// it sharp on a display of twice the density.
	OutputSide = 256
	// MinSide is the shortest side an upload may have, in pixels.
	MinSide = 32
	// MaxPixels is the largest area an upload may declare, in pixels (2048 x 2048).
	MaxPixels = 4 << 20
	// MaxDecodeBytes is the most memory one decode may need, estimated from the header (decodeCost): the decoded image
	// and the buffer of the scaler. A PNG of 2048 x 2048 pixels with 16 bits per channel just fits; a progressive JPEG,
	// which keeps the coefficients of every block, or a four-component one, is bounded to fewer pixels.
	MaxDecodeBytes = 48 << 20
	// MaxConcurrent is the number of images one replica decodes at a time. With MaxDecodeBytes it bounds what the decoding
	// holds to 96 MiB.
	MaxConcurrent = 2
	// MaxScans is the most scans (SOS segments) a JPEG may have. The decoder walks every block of the image once per
	// scan, so the number of scans multiplies the time; encoders write about ten for a progressive image.
	MaxScans = 64
	// MaxBlockVisits is the most work the scans of a JPEG may add up to, in visits of an 8 x 8 block: a scan visits every block
	// of every component it carries once, and an AC refinement scan counts twice (Q-UI-174). The number of scans alone does
	// not bound the time: with the same 64 scans, an image of 1392 x 1392 pixels with four components, whose scans carry all
	// four, costs nearly twice what a grey image of 2048 x 2048 pixels does, and a scan of end-of-block runs is a few bytes.
	// A progressive photo of 2048 x 2048 pixels with ten scans is about a fifth of it.
	MaxBlockVisits = 3 << 20
	// DecodeTimeout is how long one decode may take; the decoders read their input as they go, through reads of at most
	// readChunk bytes, so a decode that exceeds it stops at its next read. What the decoder does after its last read (the
	// reconstruction of a JPEG) is not stopped (H-640).
	DecodeTimeout = 5 * time.Second
	// BusyWait is how long a request waits for a decoding slot before it is told to try again.
	BusyWait = 2 * time.Second

	// ContentType is the media type of what Process returns, and of what the store serves.
	ContentType = "image/png"

	// sniffLen is the number of bytes http.DetectContentType reads.
	sniffLen = 512

	// readChunk is the most bytes one read of the decoder's input returns. The decoders fill a buffer of 4 KiB at a time, and
	// a progressive JPEG can carry a scan in a few bytes, so with larger reads all the scans of a small file run between two
	// reads and the deadline is never looked at.
	readChunk = 16
)

// The refusals of Process. The API maps each to the status and code of R-API-055.
var (
	// ErrEmpty is an upload without a body.
	ErrEmpty = errors.New("avatar: the body is empty")
	// ErrTooLarge is an upload above MaxUpload.
	ErrTooLarge = errors.New("avatar: the body is larger than the limit")
	// ErrUnsupportedType is an upload whose declared type, or whose content, is not PNG, JPEG or WebP.
	ErrUnsupportedType = errors.New("avatar: the type is not image/png, image/jpeg or image/webp")
	// ErrTypeMismatch is an upload whose content is an image of another format than its declared type.
	ErrTypeMismatch = errors.New("avatar: the content is not the declared type")
	// ErrAnimated is an upload with more than one frame.
	ErrAnimated = errors.New("avatar: animated images are not accepted")
	// ErrDimensions is an upload whose size in pixels is outside the bounds.
	ErrDimensions = errors.New("avatar: the dimensions are outside the bounds")
	// ErrCorrupt is an upload that does not decode.
	ErrCorrupt = errors.New("avatar: the image does not decode")
	// ErrComplex is an upload that would take too long to decode: too many scans, or a decode that exceeded DecodeTimeout.
	ErrComplex = errors.New("avatar: the image takes too long to decode")
	// ErrBusy is returned when no decoding slot became free within BusyWait.
	ErrBusy = errors.New("avatar: the replica is decoding other images")
)

// Processor turns uploads into avatars. It bounds the decoding the replica does at one time.
type Processor struct {
	slots chan struct{}
	wait  time.Duration
	// decodeTimeout is DecodeTimeout; a test of an accepted size raises it, because the race detector slows the decoder
	// beyond the bound of production (Test and Release run 42409044656: 9.7 s for 2048 x 2048).
	decodeTimeout time.Duration
}

// NewProcessor returns a Processor that decodes MaxConcurrent images at a time.
func NewProcessor() *Processor { return newProcessor(MaxConcurrent, BusyWait) }

func newProcessor(n int, wait time.Duration) *Processor {
	return &Processor{slots: make(chan struct{}, n), wait: wait, decodeTimeout: DecodeTimeout}
}

// format is one of the accepted formats.
type format string

const (
	formatPNG  format = "image/png"
	formatJPEG format = "image/jpeg"
	formatWebP format = "image/webp"
)

// formatOf maps a media type to an accepted format; the parameters are ignored.
func formatOf(mediaType string) (format, bool) {
	mt, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		return "", false
	}
	switch f := format(strings.ToLower(mt)); f {
	case formatPNG, formatJPEG, formatWebP:
		return f, true
	}
	return "", false
}

// Sniff is the format of the content, by its first bytes: the declared type is a claim, the bytes are the evidence.
func sniff(data []byte) (format, bool) {
	return formatOf(http.DetectContentType(data[:min(len(data), sniffLen)]))
}

// Process returns the avatar made of an upload: a PNG of OutputSide x OutputSide pixels. declared is the media type the
// client sent. The result is a function of the decoded pixels only (and, for a JPEG, of its orientation), so the same image
// always yields the same bytes.
func (p *Processor) Process(ctx context.Context, declared string, data []byte) ([]byte, error) {
	f, err := check(declared, data)
	if err != nil {
		return nil, err
	}
	cfg, err := config(f, data)
	if err != nil {
		return nil, err
	}
	if err := bounds(cfg); err != nil {
		return nil, err
	}
	var shape jpegShape
	if f == formatJPEG {
		if shape, err = walkJPEG(data); err != nil {
			return nil, err
		}
	}
	if decodeCost(f, cfg, shape) > MaxDecodeBytes {
		return nil, ErrDimensions
	}
	release, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	src, err := decode(ctx, f, data, p.decodeTimeout)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	square := scale(src)
	if f == formatJPEG {
		square = orient(square, exifOrientation(data))
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	var out bytes.Buffer
	if err := enc.Encode(&out, square); err != nil {
		return nil, fmt.Errorf("avatar: encode: %w", err)
	}
	return out.Bytes(), nil
}

// check decides the format and refuses what is not an accepted still image, from the bytes alone.
func check(declared string, data []byte) (format, error) {
	if len(data) == 0 {
		return "", ErrEmpty
	}
	if len(data) > MaxUpload {
		return "", ErrTooLarge
	}
	want, ok := formatOf(declared)
	if !ok {
		return "", ErrUnsupportedType
	}
	got, ok := sniff(data)
	if !ok {
		return "", ErrUnsupportedType
	}
	if got != want {
		return "", ErrTypeMismatch
	}
	if animated(got, data) {
		return "", ErrAnimated
	}
	return got, nil
}

// config reads the size from the header, before anything is allocated for the pixels.
func config(f format, data []byte) (image.Config, error) {
	var (
		cfg image.Config
		err error
	)
	switch f {
	case formatPNG:
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
	case formatJPEG:
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
	default:
		cfg, err = webp.DecodeConfig(bytes.NewReader(data))
	}
	if err != nil {
		return image.Config{}, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	return cfg, nil
}

// bounds refuses a size outside the limits. The product is taken in 64 bits: a header can declare 2^31 x 2^31.
func bounds(cfg image.Config) error {
	if cfg.Width < MinSide || cfg.Height < MinSide || int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return ErrDimensions
	}
	return nil
}

// acquire waits for a decoding slot.
func (p *Processor) acquire(ctx context.Context) (release func(), err error) {
	timer := time.NewTimer(p.wait)
	defer timer.Stop()
	select {
	case p.slots <- struct{}{}:
		return func() { <-p.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, ErrBusy
	}
}

// decode runs the decoder of the format. A decoder that panics on a crafted file is an image that does not decode.
func decode(parent context.Context, f format, data []byte, timeout time.Duration) (img image.Image, err error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			img, err = nil, fmt.Errorf("%w: decoder panic: %v", ErrCorrupt, r)
		}
	}()
	r := &ctxReader{ctx: ctx, r: bytes.NewReader(data)}
	switch f {
	case formatPNG:
		img, err = png.Decode(r)
	case formatJPEG:
		img, err = jpeg.Decode(r)
	default:
		img, err = webp.Decode(r)
	}
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, decodeStopped(parent, cerr)
		}
		return nil, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	return img, nil
}

// decodeStopped is the error of a decode that the context ended: the request's own end (the client left) is passed on, the
// deadline of the decode is ErrComplex.
func decodeStopped(parent context.Context, err error) error {
	if perr := parent.Err(); perr != nil {
		return perr
	}
	return fmt.Errorf("%w: %w", ErrComplex, err)
}

// ctxReader fails a read once its context is done, so a decoder that works through the input stops at its next read.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p[:min(len(p), readChunk)])
}

// scale takes the largest centred square of src and scales it to OutputSide.
func scale(src image.Image) *image.NRGBA {
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	x0 := b.Min.X + (b.Dx()-side)/2
	y0 := b.Min.Y + (b.Dy()-side)/2
	crop := image.Rect(x0, y0, x0+side, y0+side)
	dst := image.NewNRGBA(image.Rect(0, 0, OutputSide, OutputSide))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, draw.Src, nil)
	return dst
}

// animated reports whether the content holds more than one frame: an APNG (an acTL chunk before the image data) or a WebP
// with the animation flag. A JPEG has one image to the decoder.
func animated(f format, data []byte) bool {
	switch f {
	case formatPNG:
		return apngControl(data)
	case formatWebP:
		return webpAnimation(data)
	}
	return false
}

// apngControl walks the chunks of a PNG up to the first image data chunk and reports an animation control chunk.
func apngControl(data []byte) bool {
	const signature = 8
	for off := signature; off+8 <= len(data); {
		n := int(uint32(data[off])<<24 | uint32(data[off+1])<<16 | uint32(data[off+2])<<8 | uint32(data[off+3]))
		switch string(data[off+4 : off+8]) {
		case "acTL":
			return true
		case "IDAT", "IEND":
			return false
		}
		if n < 0 || n > len(data) {
			return false
		}
		off += 8 + n + 4
	}
	return false
}

// webpAnimation reports the animation flag of the extended header, the chunk right after the RIFF header.
func webpAnimation(data []byte) bool {
	const flags = 20
	return len(data) > flags && string(data[12:16]) == "VP8X" && data[flags]&(1<<1) != 0
}

// jpegShape is what the marker walk learns of a JPEG without decoding it.
type jpegShape struct {
	progressive bool
	components  int
	scans       int
	// visits is the work of the scans, in block visits (MaxBlockVisits).
	visits int64
}

// walkJPEG reads the markers of a JPEG, stepping over the entropy-coded data of every scan, and returns whether the frame is
// progressive, its number of components, its number of scans and the block visits they add up to. More than MaxScans scans, or
// more than MaxBlockVisits visits, is ErrComplex. A file that is
// malformed past what was seen is left to the decoder to refuse.
func walkJPEG(data []byte) (jpegShape, error) {
	var (
		sh     jpegShape
		blocks int64 // of one component, at the full resolution of the frame
	)
	for off := 2; off+2 <= len(data); {
		if data[off] != 0xFF {
			return sh, nil
		}
		m := data[off+1]
		switch {
		case m == 0xFF:
			off++
			continue
		case m == 0x00 || m == 0x01 || m >= 0xD0 && m <= 0xD8:
			off += 2
			continue
		case m == 0xD9:
			return sh, nil
		}
		if off+4 > len(data) {
			return sh, nil
		}
		n := int(data[off+2])<<8 | int(data[off+3])
		if n < 2 || off+2+n > len(data) {
			return sh, nil
		}
		seg := data[off+4 : off+2+n]
		off += 2 + n
		switch {
		case m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC:
			sh.progressive = m == 0xC2 || m == 0xCA
			if len(seg) >= 6 {
				sh.components = int(seg[5])
				h, w := int64(seg[1])<<8|int64(seg[2]), int64(seg[3])<<8|int64(seg[4])
				blocks = (h + 7) / 8 * ((w + 7) / 8)
			}
		case m == 0xDA:
			sh.scans++
			if sh.scans > MaxScans {
				return sh, ErrComplex
			}
			if sh.visits += scanVisits(seg, blocks); sh.visits > MaxBlockVisits {
				return sh, ErrComplex
			}
			off = skipEntropy(data, off)
		}
	}
	return sh, nil
}

// scanVisits is the work of one scan, from its header: the components it carries times the blocks of a component, taken at the
// full resolution of the frame (an upper bound for a subsampled one), twice for the refinement of AC coefficients, which walks
// the coded coefficients of the band in every block. A header too short to say is one component.
func scanVisits(seg []byte, blocks int64) int64 {
	n := int64(1)
	if len(seg) > 0 && seg[0] > 0 {
		n = int64(seg[0])
	}
	if i := 1 + 2*int(n); i+2 < len(seg) && seg[i] > 0 && seg[i+2]>>4 != 0 {
		n *= 2
	}
	return n * blocks
}

// skipEntropy returns the offset of the first marker after the entropy-coded data that starts at off: a 0xFF followed by
// neither 0x00 (a stuffed byte) nor a restart marker.
func skipEntropy(data []byte, off int) int {
	for off < len(data) {
		i := bytes.IndexByte(data[off:], 0xFF)
		if i < 0 || off+i+1 >= len(data) {
			return len(data)
		}
		off += i
		if n := data[off+1]; n == 0x00 || n >= 0xD0 && n <= 0xD7 {
			off += 2
			continue
		}
		return off
	}
	return off
}

// decodeCost estimates, from the header, the memory one decode needs: the decoded image, the buffers the format keeps
// beside it, and the scaler's buffer for the crop (OutputSide x side x 32 bytes). A progressive JPEG keeps four bytes of
// coefficients and about one of samples for every component of every pixel; a baseline one the samples (a CMYK or YCCK image
// has four components); a PNG four bytes a pixel, eight with 16 bits per channel; a WebP is taken at eight.
func decodeCost(f format, cfg image.Config, sh jpegShape) int64 {
	px := int64(cfg.Width) * int64(cfg.Height)
	perPixel := int64(8)
	switch f {
	case formatPNG:
		if cfg.ColorModel != color.NRGBA64Model && cfg.ColorModel != color.RGBA64Model && cfg.ColorModel != color.Gray16Model {
			perPixel = 4
		}
	case formatJPEG:
		perPixel = int64(max(sh.components, 1))
		if sh.progressive {
			perPixel *= 5
		}
	}
	return px*perPixel + int64(OutputSide)*int64(min(cfg.Width, cfg.Height))*32
}
