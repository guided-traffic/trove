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
	"image/jpeg"
	"image/png"
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
	// MaxPixels is the largest area an upload may declare, in pixels (2048 x 2048). The decoders allocate by it; at the
	// widest pixel they hold, 8 bytes, it is 32 MiB.
	MaxPixels = 4 << 20
	// MaxConcurrent is the number of images one replica decodes at a time. With MaxPixels it bounds what the decoding
	// holds to 64 MiB.
	MaxConcurrent = 2
	// BusyWait is how long a request waits for a decoding slot before it is told to try again.
	BusyWait = 2 * time.Second

	// ContentType is the media type of what Process returns, and of what the store serves.
	ContentType = "image/png"

	// sniffLen is the number of bytes http.DetectContentType reads.
	sniffLen = 512
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
	// ErrBusy is returned when no decoding slot became free within BusyWait.
	ErrBusy = errors.New("avatar: the replica is decoding other images")
)

// Processor turns uploads into avatars. It bounds the decoding the replica does at one time.
type Processor struct {
	slots chan struct{}
	wait  time.Duration
}

// NewProcessor returns a Processor that decodes MaxConcurrent images at a time.
func NewProcessor() *Processor { return newProcessor(MaxConcurrent, BusyWait) }

func newProcessor(n int, wait time.Duration) *Processor {
	return &Processor{slots: make(chan struct{}, n), wait: wait}
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
	release, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	src, err := decode(f, data)
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
func decode(f format, data []byte) (img image.Image, err error) {
	defer func() {
		if r := recover(); r != nil {
			img, err = nil, fmt.Errorf("%w: decoder panic: %v", ErrCorrupt, r)
		}
	}()
	r := bytes.NewReader(data)
	switch f {
	case formatPNG:
		img, err = png.Decode(r)
	case formatJPEG:
		img, err = jpeg.Decode(r)
	default:
		img, err = webp.Decode(r)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	return img, nil
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
