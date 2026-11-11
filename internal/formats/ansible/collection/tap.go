package collection

// Tap lets a caller see the bytes of chosen members while the archive streams through its digest (R-ANS-034): the documentation
// generator reads the plugin sources, the README and the Markdown files of docs/ this way, in the one pass the reader makes (Q-ANS-30).
// The tap never changes what the reader accepts or refuses.
type Tap interface {
	// Wants reports whether the member with the cleaned archive name path is wanted, and how many of its first bytes at most; the rest
	// of the member is streamed through the digest and dropped.
	Wants(path string) (maxBytes int64, ok bool)
	// Take receives the first bytes of a wanted member once the member has been read to its end. The slice is the tap's to keep.
	Take(path string, body []byte)
}

// Option changes how [ReadArchive] reads.
type Option func(*options)

type options struct{ tap Tap }

// WithTap gives the reader a [Tap]. A nil tap is the reader without one.
func WithTap(t Tap) Option { return func(o *options) { o.tap = t } }

// capWriter keeps the first max bytes written to it and discards the rest. It never fails, so the digest beside it is always complete.
type capWriter struct {
	buf []byte
	max int64
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - int64(len(w.buf)); room > 0 {
		w.buf = append(w.buf, p[:min(int64(len(p)), room)]...)
	}
	return len(p), nil
}
