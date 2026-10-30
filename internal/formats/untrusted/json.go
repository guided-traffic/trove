package untrusted

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/guided-traffic/trove/internal/errs"
)

// CheckJSON scans a JSON document for its nesting depth and its number of values
// before any decoder builds a tree from it (R-ARCH-028 "JSON decoders run with
// depth limits", Q-ARCH-217). It does not validate the document: a decoder does,
// after this scan has bounded what the decoder can cost. A bound that is crossed
// is TooLarge, found at the byte that crosses it.
func CheckJSON(data []byte, l Limits) error {
	l = effective(l)
	depth, values := 0, 1
	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			switch c {
			case '\\':
				i++
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
			values++
			if depth > l.JSONDepth {
				return TooLarge(fmt.Sprintf("the document is nested deeper than %d levels", l.JSONDepth))
			}
		case '}', ']':
			depth--
		case ',':
			values++
		}
		if values > l.JSONMembers {
			return TooLarge(fmt.Sprintf("the document holds more than %d values", l.JSONMembers))
		}
	}
	return nil
}

// DecodeJSON reads one JSON document from r into v: at most the document bound
// of l in bytes (Limits.Bound), then CheckJSON, then the decoder. Content after
// the first value is refused. A document that does not decode is Refused; a bound
// crossed is TooLarge.
func DecodeJSON(r io.Reader, l Limits, v any) error {
	data, err := ReadAll(r, effective(l).Bound())
	if err != nil {
		return err
	}
	return UnmarshalJSON(data, l, v)
}

// UnmarshalJSON is DecodeJSON over bytes the caller already holds.
func UnmarshalJSON(data []byte, l Limits, v any) error {
	if err := CheckJSON(data, l); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return Refused("the document is not valid JSON of the expected shape", errs.WithCause(err))
	}
	if _, err := dec.Token(); err != io.EOF {
		return Refused("the document holds data after its first value")
	}
	return nil
}
