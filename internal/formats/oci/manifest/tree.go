package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// The strict JSON reader of a manifest (R-OCI-013 step 3, R-OCI-020). The standard decoder is no safe reader of an untrusted
// document that another reader will interpret too: it keeps the last of two equal keys, matches a key against a field name
// without regard to case, replaces an invalid byte by U+FFFD and accepts a number where an integer is meant. Two readers that
// disagree about one document is how a registry stores what a client then reads differently, so this reader builds the whole
// tree itself and refuses what could be read two ways:
//
//   - a document that is not UTF-8, or that holds data after its first value;
//   - an object with the same key twice (compared exactly, after the escapes were resolved);
//   - a number that is not read back to the integer it was written as (interpretation is the caller's: it receives the text).
//
// The scan of [untrusted.CheckJSON] has run before, so the nesting and the number of values are bounded already; the reader
// counts again, because it is the one that allocates.

type nodeKind uint8

const (
	kindNull nodeKind = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

// node is one JSON value. s holds a string or the text of a number.
type node struct {
	kind nodeKind
	s    string
	b    bool
	arr  []node
	obj  map[string]node
}

func (k nodeKind) String() string {
	switch k {
	case kindNull:
		return "null"
	case kindBool:
		return "a boolean"
	case kindNumber:
		return "a number"
	case kindString:
		return "a string"
	case kindArray:
		return "an array"
	case kindObject:
		return "an object"
	}
	return "a value"
}

type reader struct {
	dec       *json.Decoder
	values    int
	maxValues int
	depth     int
	maxDepth  int
}

// readTree reads the one JSON value of data.
func readTree(data []byte, maxDepth, maxValues int) (node, error) {
	if !utf8.Valid(data) {
		return node{}, invalidf("the manifest is not valid UTF-8")
	}
	if err := checkSurrogates(data); err != nil {
		return node{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	r := &reader{dec: dec, maxDepth: maxDepth, maxValues: maxValues}
	n, err := r.value()
	if err != nil {
		return node{}, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return node{}, invalidf("the manifest holds data after its first value")
	}
	return n, nil
}

func (r *reader) value() (node, error) {
	tok, err := r.dec.Token()
	if err != nil {
		return node{}, invalidf("the manifest is not valid JSON")
	}
	r.values++
	if r.values > r.maxValues {
		return node{}, tooLarge(fmt.Sprintf("the manifest holds more than %d JSON values", r.maxValues))
	}
	switch t := tok.(type) {
	case json.Delim:
		r.depth++
		defer func() { r.depth-- }()
		if r.depth > r.maxDepth {
			return node{}, tooLarge(fmt.Sprintf("the manifest is nested deeper than %d levels", r.maxDepth))
		}
		if t == '{' {
			return r.object()
		}
		return r.array()
	case string:
		return node{kind: kindString, s: t}, nil
	case json.Number:
		return node{kind: kindNumber, s: string(t)}, nil
	case bool:
		return node{kind: kindBool, b: t}, nil
	case nil:
		return node{kind: kindNull}, nil
	}
	return node{}, invalidf("the manifest is not valid JSON")
}

func (r *reader) object() (node, error) {
	obj := map[string]node{}
	for r.dec.More() {
		kt, err := r.dec.Token()
		if err != nil {
			return node{}, invalidf("the manifest is not valid JSON")
		}
		key, ok := kt.(string)
		if !ok {
			return node{}, invalidf("the manifest is not valid JSON")
		}
		if _, dup := obj[key]; dup {
			return node{}, invalidf("the manifest names the key %s twice", quote(key))
		}
		v, err := r.value()
		if err != nil {
			return node{}, err
		}
		obj[key] = v
	}
	if _, err := r.dec.Token(); err != nil { // the closing brace
		return node{}, invalidf("the manifest is not valid JSON")
	}
	return node{kind: kindObject, obj: obj}, nil
}

func (r *reader) array() (node, error) {
	var arr []node
	for r.dec.More() {
		v, err := r.value()
		if err != nil {
			return node{}, err
		}
		arr = append(arr, v)
	}
	if _, err := r.dec.Token(); err != nil { // the closing bracket
		return node{}, invalidf("the manifest is not valid JSON")
	}
	return node{kind: kindArray, arr: arr}, nil
}

// checkSurrogates refuses an escape that names half of a surrogate pair without the other half: the standard decoder turns it into
// U+FFFD, so the registry would hold a string that is not the one the bytes spell, and another reader may read it differently.
func checkSurrogates(data []byte) error {
	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			continue
		}
		switch c {
		case '"':
			inString = false
		case '\\':
			if i+1 >= len(data) {
				return nil
			}
			if data[i+1] != 'u' {
				i++
				continue
			}
			r, ok := hex4(data, i+2)
			if !ok {
				return nil // the decoder refuses it
			}
			i += 5
			switch {
			case r >= 0xD800 && r < 0xDC00:
				lo, ok := uint32(0), false
				if i+6 < len(data) && data[i+1] == '\\' && data[i+2] == 'u' {
					lo, ok = hex4(data, i+3)
				}
				if !ok || lo < 0xDC00 || lo > 0xDFFF {
					return invalidf("the manifest holds an unpaired surrogate escape")
				}
				i += 6
			case r >= 0xDC00 && r <= 0xDFFF:
				return invalidf("the manifest holds an unpaired surrogate escape")
			}
		}
	}
	return nil
}

func hex4(data []byte, at int) (uint32, bool) {
	if at+4 > len(data) {
		return 0, false
	}
	var r uint32
	for _, c := range data[at : at+4] {
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | uint32(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | uint32(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | uint32(c-'A'+10)
		default:
			return 0, false
		}
	}
	return r, true
}
