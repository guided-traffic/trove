package untrusted

import (
	"context"
	"fmt"
	"io"
)

// effective is l with every unset field at its default: a reader never runs
// unbounded because a caller passed a zero Limits.
func effective(l Limits) Limits { return DefaultLimits().Tighten(l) }

// Context returns ctx with the deadline of l (R-ARCH-028: a 30 s context
// deadline covers the whole inspection). The caller cancels it.
func (l Limits) Context(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, effective(l).Deadline)
}

// Limit wraps r so that reading more than max bytes fails with TooLarge instead
// of ending quietly the way io.LimitReader does: a truncated document must not
// parse as a shorter one.
func Limit(r io.Reader, max int64) io.Reader { return &limitReader{r: r, left: max, max: max} }

type limitReader struct {
	r    io.Reader
	left int64
	max  int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.left < 0 {
		return 0, l.over()
	}
	if int64(len(p)) > l.left+1 {
		p = p[:l.left+1] // one byte over is how an excess is noticed
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	if l.left < 0 {
		return n - 1, l.over()
	}
	return n, err
}

func (l *limitReader) over() error {
	return TooLarge(fmt.Sprintf("the content is larger than %d bytes", l.max))
}

// ReadAll reads r to its end and fails with TooLarge when it holds more than max
// bytes. It never holds more than max plus one read.
func ReadAll(r io.Reader, max int64) ([]byte, error) { return io.ReadAll(Limit(r, max)) }

// WithContext wraps r so that a read after ctx ended fails with its error: a
// reader of a spool or of a generator has no connection that would notice.
func WithContext(ctx context.Context, r io.Reader) io.Reader { return &ctxReader{ctx: ctx, r: r} }

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
