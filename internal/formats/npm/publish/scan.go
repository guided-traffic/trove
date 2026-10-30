// Package publish reads the request body of an npm publish (R-NPM-007): a JSON
// object whose `_attachments` member carries the tarball as a base64 string of up
// to hundreds of megabytes.
//
// encoding/json hands out a string as one value, so a decoder would hold the
// whole attachment in memory, which R-NPM-007 step 2 forbids (Q-NPM-20, plan
// D-25). The [Scan] function is a small JSON scanner over the byte stream
// instead: every member except the attachment data is read into memory within a
// byte budget and a nesting depth, and the value of `data` is decoded from base64
// while it streams, straight into the writer of the caller. Member order is not
// assumed: the metadata may come before or after the attachments.
//
// Everything in the body is publisher-controlled. The scanner never allocates in
// proportion to a length it has not yet read, bounds every key, and refuses what
// two JSON readers could read differently: a duplicate member, data after the
// document, a string with a control character.
//
// It is a leaf of the npm format: standard library, internal/errs and the
// manifest package for its error constructors.
package publish

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/npm/manifest"
)

// Limits bound what a publish body may cost. A zero field takes its default.
type Limits struct {
	// MaxMetadataBytes bounds the body outside the attachments' data: the
	// manifest, the dist-tags, the attachment descriptors and the whitespace.
	MaxMetadataBytes int64
	// MaxDepth is the JSON nesting depth the scanner follows; the top-level
	// object has depth 1.
	MaxDepth int
	// MaxDecoded bounds the decoded bytes of one attachment.
	MaxDecoded int64
	// MaxAttachments bounds the number of attachments.
	MaxAttachments int
	// MaxKeyBytes bounds a member name, which includes an attachment's file name.
	MaxKeyBytes int
}

// The defaults. The metadata budget leaves room for the 1 MiB of body that
// R-NPM-007 adds to the base64 of the tarball: a manifest with a README, a
// descriptor and the provenance bundle of an attachment.
const (
	DefaultMaxMetadataBytes = 768 << 10
	DefaultMaxDepth         = 64
	DefaultMaxDecoded       = 256 << 20
	DefaultMaxAttachments   = 4
	DefaultMaxKeyBytes      = 1024
)

// DefaultLimits returns the defaults.
func DefaultLimits() Limits { return Limits{}.WithDefaults() }

// WithDefaults replaces zero fields by the defaults.
func (l Limits) WithDefaults() Limits {
	if l.MaxMetadataBytes <= 0 {
		l.MaxMetadataBytes = DefaultMaxMetadataBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	if l.MaxDecoded <= 0 {
		l.MaxDecoded = DefaultMaxDecoded
	}
	if l.MaxAttachments <= 0 {
		l.MaxAttachments = DefaultMaxAttachments
	}
	if l.MaxKeyBytes <= 0 {
		l.MaxKeyBytes = DefaultMaxKeyBytes
	}
	return l
}

// Envelope is the metadata of a publish body. Versions and DistTags are what the
// body held, unvalidated: [Envelope.Request] checks them.
type Envelope struct {
	// ID and Name are the `_id` and `name` members; "" when absent.
	ID, Name string
	// Versions maps each version key to the raw JSON of its manifest.
	Versions map[string]json.RawMessage
	// DistTags maps each tag to a version.
	DistTags map[string]string
	// Attachments are the attachments in the order of the body.
	Attachments []Attachment

	has map[string]bool
}

// Attachment is one entry of `_attachments`.
type Attachment struct {
	// Name is the key of the entry, the file name the client gave.
	Name string
	// ContentType and Length are the declared members; Length is -1 when absent.
	ContentType string
	Length      int64
	// Decoded is the number of bytes the base64 data decoded to.
	Decoded int64
	// Result is what the sink returned for the data.
	Result any
}

// Sink receives the decoded data of one attachment as a reader and returns what it
// made of it (a spooled file). It should read to the end; whatever it leaves is
// read and discarded by the scanner within the same limit. An error stops the scan.
type Sink func(name string, data io.Reader) (any, error)

// Scan reads the publish body from r. A sink that is nil refuses every
// attachment.
//
// Errors are *errs.Error where the body is at fault: 400 npm_bad_publish for
// malformed JSON, a duplicate member, an invalid base64 string or a body that is
// not what a publish looks like, and 413 npm_too_large for a limit or a body
// that http.MaxBytesReader cut. An error from r that is not one of those, and a
// cancelled context, are returned as they are.
func Scan(ctx context.Context, r io.Reader, lim Limits, sink Sink) (*Envelope, error) {
	lim = lim.WithDefaults()
	s := &scanner{ctx: ctx, br: bufio.NewReaderSize(r, 32<<10), lim: lim, budget: lim.MaxMetadataBytes, sink: sink}
	env, err := s.document()
	if err != nil {
		return nil, classify(err)
	}
	return env, nil
}

// classify turns an error of the byte stream into the error the caller returns.
func classify(err error) error {
	if _, ok := errs.As(err); ok {
		return err
	}
	var mb *http.MaxBytesError
	switch {
	case errors.As(err, &mb):
		return manifest.TooLarge(fmt.Sprintf("the request body is larger than %d bytes", mb.Limit))
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return manifest.BadPublish("the request body ends inside the JSON document")
	}
	return err
}

type scanner struct {
	ctx    context.Context
	br     *bufio.Reader
	lim    Limits
	budget int64
	sink   Sink
}

func bad(format string, a ...any) error {
	return manifest.BadPublish(fmt.Sprintf(format, a...))
}

// byte reads one byte of the metadata and charges it to the budget.
func (s *scanner) byte() (byte, error) {
	c, err := s.br.ReadByte()
	if err != nil {
		return 0, err
	}
	if s.budget--; s.budget < 0 {
		return 0, manifest.TooLarge(fmt.Sprintf("the publish request holds more than %d bytes besides the attachment data", s.lim.MaxMetadataBytes))
	}
	return c, nil
}

func (s *scanner) unread() {
	_ = s.br.UnreadByte()
	s.budget++
}

// next returns the next byte that is not JSON whitespace.
func (s *scanner) next() (byte, error) {
	for {
		c, err := s.byte()
		if err != nil {
			return 0, err
		}
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return c, nil
	}
}

func (s *scanner) expect(want byte) error {
	c, err := s.next()
	if err != nil {
		return err
	}
	if c != want {
		return bad("the request body is not valid JSON: expected %q", want)
	}
	return nil
}

// document reads the top-level object and what may follow it.
func (s *scanner) document() (*Envelope, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	env := &Envelope{has: map[string]bool{}}
	c, err := s.next()
	if err != nil {
		return nil, err
	}
	if c != '{' {
		return nil, bad("the publish request is not a JSON object")
	}
	if err := s.members(env.member(s)); err != nil {
		return nil, err
	}
	// Nothing but whitespace may follow; reading to the end also lets a size cap on
	// the body surface.
	for {
		c, err := s.br.ReadByte()
		if errors.Is(err, io.EOF) {
			return env, nil
		}
		if err != nil {
			return nil, err
		}
		switch c {
		case ' ', '\t', '\r', '\n':
			if s.budget--; s.budget < 0 {
				return nil, manifest.TooLarge("the publish request holds more whitespace than a request needs")
			}
		default:
			return nil, bad("the request body holds data after the JSON document")
		}
	}
}

// members reads the members of an object whose '{' was consumed, calling fn for
// each name; fn consumes the value.
func (s *scanner) members(fn func(key string) error) error {
	c, err := s.next()
	if err != nil {
		return err
	}
	if c == '}' {
		return nil
	}
	s.unread()
	for {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		if err := s.expect('"'); err != nil {
			return err
		}
		key, err := s.key()
		if err != nil {
			return err
		}
		if err := s.expect(':'); err != nil {
			return err
		}
		if err := fn(key); err != nil {
			return err
		}
		c, err := s.next()
		if err != nil {
			return err
		}
		switch c {
		case ',':
		case '}':
			return nil
		default:
			return bad("the request body is not valid JSON: expected ',' or '}'")
		}
	}
}

// key reads the rest of a member name whose opening quote was consumed.
func (s *scanner) key() (string, error) {
	var b bytes.Buffer
	if err := s.stringBody(&b, int64(s.lim.MaxKeyBytes)); err != nil {
		return "", err
	}
	var key string
	if err := json.Unmarshal(append(append([]byte{'"'}, b.Bytes()...), '"'), &key); err != nil || !utf8.ValidString(key) {
		return "", bad("a member name of the request is not valid text")
	}
	return key, nil
}

// stringBody copies the raw text of a string whose opening quote was consumed, up
// to and excluding the closing quote, validating the escapes. max bounds its
// length in bytes.
func (s *scanner) stringBody(dst *bytes.Buffer, max int64) error {
	var n int64
	for {
		c, err := s.byte()
		if err != nil {
			return err
		}
		switch {
		case c == '"':
			return nil
		case c < 0x20:
			return bad("the request body holds a control character inside a string")
		}
		if n++; n > max {
			return manifest.TooLarge(fmt.Sprintf("a string of the publish request is longer than %d bytes", max))
		}
		dst.WriteByte(c)
		if c != '\\' {
			continue
		}
		e, err := s.byte()
		if err != nil {
			return err
		}
		dst.WriteByte(e)
		switch e {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		case 'u':
			for range 4 {
				h, err := s.byte()
				if err != nil {
					return err
				}
				if !isHex(h) {
					return bad("the request body holds an invalid \\u escape")
				}
				dst.WriteByte(h)
			}
		default:
			return bad("the request body holds an invalid escape in a string")
		}
	}
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// value reads one JSON value, validating its structure, and appends its compact
// form to dst when dst is not nil. depth is the depth of the value's container.
func (s *scanner) value(dst *bytes.Buffer, depth int) error {
	c, err := s.next()
	if err != nil {
		return err
	}
	switch c {
	case '{':
		return s.container(dst, depth, '{', '}')
	case '[':
		return s.container(dst, depth, '[', ']')
	case '"':
		if dst != nil {
			dst.WriteByte('"')
		}
		var raw bytes.Buffer
		if err := s.stringBody(&raw, s.budget); err != nil {
			return err
		}
		if dst != nil {
			dst.Write(raw.Bytes())
			dst.WriteByte('"')
		}
		return nil
	}
	if c == '-' || c >= '0' && c <= '9' || c == 't' || c == 'f' || c == 'n' {
		return s.scalar(dst, c)
	}
	return bad("the request body is not valid JSON: unexpected %q", c)
}

// scalar reads a number or a literal that starts with c.
func (s *scanner) scalar(dst *bytes.Buffer, c byte) error {
	tok := []byte{c}
	for len(tok) <= 64 {
		n, err := s.br.Peek(1)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		d := n[0]
		if d >= '0' && d <= '9' || d >= 'a' && d <= 'z' || d == '+' || d == '-' || d == '.' || d == 'E' {
			tok = append(tok, d)
			if _, err := s.byte(); err != nil {
				return err
			}
			continue
		}
		break
	}
	if !json.Valid(tok) {
		return bad("the request body is not valid JSON: bad number or literal")
	}
	if dst != nil {
		dst.Write(tok)
	}
	return nil
}

func (s *scanner) container(dst *bytes.Buffer, depth int, open, closeCh byte) error {
	if depth++; depth > s.lim.MaxDepth {
		return manifest.TooLarge(fmt.Sprintf("the publish request is nested deeper than %d levels", s.lim.MaxDepth))
	}
	if dst != nil {
		dst.WriteByte(open)
	}
	c, err := s.next()
	if err != nil {
		return err
	}
	if c == closeCh {
		if dst != nil {
			dst.WriteByte(closeCh)
		}
		return nil
	}
	s.unread()
	for first := true; ; first = false {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		if !first && dst != nil {
			dst.WriteByte(',')
		}
		if open == '{' {
			if err := s.expect('"'); err != nil {
				return err
			}
			if dst != nil {
				dst.WriteByte('"')
			}
			var raw bytes.Buffer
			if err := s.stringBody(&raw, s.budget); err != nil {
				return err
			}
			if dst != nil {
				dst.Write(raw.Bytes())
				dst.WriteString(`":`)
			}
			if err := s.expect(':'); err != nil {
				return err
			}
		}
		if err := s.value(dst, depth); err != nil {
			return err
		}
		c, err := s.next()
		if err != nil {
			return err
		}
		switch c {
		case ',':
		case closeCh:
			if dst != nil {
				dst.WriteByte(closeCh)
			}
			return nil
		default:
			return bad("the request body is not valid JSON: expected ',' or %q", closeCh)
		}
	}
}

// member returns the callback for the members of the top-level object.
func (e *Envelope) member(s *scanner) func(key string) error {
	return func(key string) error {
		if e.has[key] {
			return bad("the publish request holds the member %q twice", key)
		}
		e.has[key] = true
		switch key {
		case "name", "_id":
			var raw bytes.Buffer
			if err := s.value(&raw, 1); err != nil {
				return err
			}
			var v string
			if err := json.Unmarshal(raw.Bytes(), &v); err != nil {
				return bad("the member %q of the publish request is not a string", key)
			}
			if key == "name" {
				e.Name = v
			} else {
				e.ID = v
			}
		case "versions":
			var raw bytes.Buffer
			if err := s.value(&raw, 1); err != nil {
				return err
			}
			if err := json.Unmarshal(raw.Bytes(), &e.Versions); err != nil {
				return bad("the member \"versions\" of the publish request is not an object of manifests")
			}
		case "dist-tags":
			var raw bytes.Buffer
			if err := s.value(&raw, 1); err != nil {
				return err
			}
			if err := json.Unmarshal(raw.Bytes(), &e.DistTags); err != nil {
				return bad("the member \"dist-tags\" of the publish request maps tag names to version strings")
			}
		case "_attachments":
			return s.attachments(e)
		default:
			return s.value(nil, 1)
		}
		return nil
	}
}

// attachments reads the value of `_attachments`.
func (s *scanner) attachments(env *Envelope) error {
	if err := s.expect('{'); err != nil {
		return err
	}
	seen := map[string]bool{}
	return s.members(func(file string) error {
		if seen[file] {
			return bad("the publish request holds the attachment %q twice", file)
		}
		seen[file] = true
		if len(env.Attachments) >= s.lim.MaxAttachments {
			return bad("the publish request holds more than %d attachments", s.lim.MaxAttachments)
		}
		if err := s.expect('{'); err != nil {
			return err
		}
		att := Attachment{Name: file, Length: -1}
		got := map[string]bool{}
		err := s.members(func(k string) error {
			if got[k] {
				return bad("the attachment %q holds the member %q twice", file, k)
			}
			got[k] = true
			switch k {
			case "content_type":
				var raw bytes.Buffer
				if err := s.value(&raw, 3); err != nil {
					return err
				}
				if err := json.Unmarshal(raw.Bytes(), &att.ContentType); err != nil {
					return bad("the content_type of the attachment %q is not a string", file)
				}
			case "length":
				var raw bytes.Buffer
				if err := s.value(&raw, 3); err != nil {
					return err
				}
				var n json.Number
				if c := raw.Bytes(); len(c) == 0 || c[0] != '-' && (c[0] < '0' || c[0] > '9') {
					return bad("the length of the attachment %q is not a number", file)
				}
				if err := json.Unmarshal(raw.Bytes(), &n); err != nil {
					return bad("the length of the attachment %q is not a number", file)
				}
				v, err := n.Int64()
				if err != nil || v < 0 {
					return bad("the length of the attachment %q is not a non-negative integer", file)
				}
				att.Length = v
			case "data":
				return s.data(&att)
			default:
				return s.value(nil, 3)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if !got["data"] {
			return bad("the attachment %q has no data", file)
		}
		env.Attachments = append(env.Attachments, att)
		return nil
	})
}

// data streams the value of an attachment's `data` member into the sink.
func (s *scanner) data(att *Attachment) error {
	if err := s.expect('"'); err != nil {
		return bad("the data of the attachment %q is not a base64 string", att.Name)
	}
	if s.sink == nil {
		return bad("the publish request carries the attachment %q, which this request cannot take", att.Name)
	}
	sr := &stringReader{s: s}
	cr := &countReader{r: &cappedReader{r: base64.NewDecoder(base64.StdEncoding, sr), max: s.lim.MaxDecoded}}
	res, err := s.sink(att.Name, cr)
	if err != nil {
		return sinkError(err, sr)
	}
	// Whatever the sink left unread is read to the closing quote and dropped, so
	// the next member starts where it should.
	if _, err := io.Copy(io.Discard, cr); err != nil {
		return sinkError(err, sr)
	}
	att.Decoded, att.Result = cr.n, res
	return nil
}

// sinkError classifies an error of the decoding chain: a base64 fault is the
// request's. A decoder that runs into the end of a string that held no whole
// number of base64 groups reports an unexpected end; once the closing quote has
// been read that is the data's fault, not the body's.
func sinkError(err error, sr *stringReader) error {
	var corrupt base64.CorruptInputError
	if errors.As(err, &corrupt) || errors.Is(err, io.ErrUnexpectedEOF) && sr.done {
		return bad("an attachment is not valid base64")
	}
	return err
}

// stringReader yields the characters of a JSON string, whose opening quote was
// consumed, up to the closing quote, where it reports io.EOF. The only escapes it
// resolves are those a base64 string can carry: \/ and \u00XX for an ASCII
// character. The bytes are not charged to the metadata budget.
type stringReader struct {
	s    *scanner
	done bool
	err  error
}

func (r *stringReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.done {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if err := r.s.ctx.Err(); err != nil {
		r.err = err
		return 0, err
	}
	br := r.s.br
	if _, err := br.Peek(1); err != nil {
		r.err = err
		return 0, err
	}
	chunk, _ := br.Peek(min(len(p), br.Buffered()))
	n := 0
	for n < len(chunk) {
		c := chunk[n]
		if c == '"' || c == '\\' || c < 0x20 || c >= 0x80 {
			break
		}
		n++
	}
	if n > 0 {
		copy(p, chunk[:n])
		_, _ = br.Discard(n)
		return n, nil
	}
	c, _ := br.ReadByte()
	switch c {
	case '"':
		r.done = true
		return 0, io.EOF
	case '\\':
		ch, err := r.escape()
		if err != nil {
			r.err = err
			return 0, err
		}
		p[0] = ch
		return 1, nil
	}
	r.err = bad("an attachment holds a character that base64 does not")
	return 0, r.err
}

func (r *stringReader) escape() (byte, error) {
	br := r.s.br
	e, err := br.ReadByte()
	if err != nil {
		return 0, err
	}
	switch e {
	case '/':
		return '/', nil
	case 'u':
		var v byte
		for i := range 4 {
			h, err := br.ReadByte()
			if err != nil {
				return 0, err
			}
			d, ok := hexVal(h)
			if !ok || i < 2 && d != 0 || i == 2 && d > 7 {
				return 0, bad("an attachment holds an escape that base64 does not")
			}
			if i >= 2 {
				v = v<<4 | d
			}
		}
		return v, nil
	}
	return 0, bad("an attachment holds an escape that base64 does not")
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// cappedReader refuses more than max bytes with 413 npm_too_large.
type cappedReader struct {
	r   io.Reader
	n   int64
	max int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > c.max {
		return n, manifest.TooLarge(fmt.Sprintf("an attachment is larger than %d bytes", c.max))
	}
	return n, err
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
