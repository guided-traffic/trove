package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
)

// The request body rules of R-API-003 and R-API-017: JSON only, bounded in
// size, strict. An unknown member, a duplicate member or a value of the wrong
// type answers 400 `malformed_request` and names the member as a JSON pointer
// (RFC 6901) in errors[0], so a client sees which field of a nested body is
// wrong.

// Media types of request bodies.
const (
	MediaJSON       = "application/json"
	MediaMergePatch = "application/merge-patch+json"
)

// ReadBody reads the request body of an operation that takes mediaType (for
// example MediaJSON), bounded by limit bytes. A body above the limit answers
// 413 payload_too_large, another Content-Type 415 unsupported_media_type, and
// an empty body 400 malformed_request. The charset parameter, when present,
// must be utf-8.
func ReadBody(w http.ResponseWriter, r *http.Request, mediaType string, limit int64) ([]byte, error) {
	if err := requireJSON(r, mediaType); err != nil {
		return nil, err
	}
	body, err := readOptionalBody(w, r, limit)
	if err != nil {
		return nil, err
	}
	if body == nil {
		return nil, errs.E(errs.Invalid, errs.CodeInvalid, "The request body is empty.")
	}
	return body, nil
}

// requireJSON checks the Content-Type of a request: the media type, and a utf-8
// charset when one is given (415 unsupported_media_type otherwise).
func requireJSON(r *http.Request, mediaType string) error {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mt, mediaType) {
		return errs.E(errs.Unsupported, errs.CodeUnsupported, "The request body must be "+mediaType+".")
	}
	if cs, ok := params["charset"]; ok && !strings.EqualFold(cs, "utf-8") {
		return errs.E(errs.Unsupported, errs.CodeUnsupported, "The charset of the request body must be utf-8.")
	}
	return nil
}

// DecodeJSON reads a JSON request body into dst (a pointer to a struct or a
// map) with the strict rules above.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	body, err := ReadBody(w, r, MediaJSON, limit)
	if err != nil {
		return err
	}
	return StrictUnmarshal(body, dst)
}

// StrictUnmarshal decodes data into dst. It rejects, each with the JSON pointer
// of the offending member: a duplicate key, a member that dst has no field for
// (names are compared exactly, unlike encoding/json), and a value of the wrong
// type. Trailing data after the document is rejected.
func StrictUnmarshal(data []byte, dst any) error {
	if err := checkSyntax(data); err != nil {
		return err
	}
	if err := checkUnknown(data, reflect.TypeOf(dst)); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(dst); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return malformed("/"+strings.ReplaceAll(te.Field, ".", "/"), "type",
				"the value must be "+jsonKind(te.Type), nil)
		}
		return malformed("", "syntax", "the body is not valid JSON", err)
	}
	return nil
}

func jsonKind(t reflect.Type) string {
	if t == nil {
		return "of another type"
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Map, reflect.Struct:
		return "an object"
	default:
		return "a number"
	}
}

// malformed builds the 400 of an unreadable body. pointer is empty for the
// document as a whole.
func malformed(pointer, code, detail string, cause error) *errs.Error {
	opts := []errs.Option{errs.WithBadRequest(), errs.WithFields(errs.FieldError{Pointer: pointer, Code: code, Detail: detail})}
	if cause != nil {
		opts = append(opts, errs.WithCause(cause))
	}
	msg := "The request body is malformed."
	if pointer != "" {
		msg = "The request body is malformed at " + pointer + "."
	}
	return errs.E(errs.Invalid, errs.CodeInvalid, msg, opts...)
}

// pointerEscape escapes one reference token (RFC 6901 section 3).
func pointerEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// checkSyntax validates the JSON syntax, finds duplicate keys and rejects
// trailing data.
func checkSyntax(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := scanValue(dec, ""); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return malformed("", "syntax", "the body holds more than one JSON document", nil)
	}
	return nil
}

func scanValue(dec *json.Decoder, ptr string) error {
	tok, err := dec.Token()
	if err != nil {
		return malformed(ptr, "syntax", "the body is not valid JSON", err)
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return malformed(ptr, "syntax", "the body is not valid JSON", err)
			}
			key, _ := kt.(string)
			p := ptr + "/" + pointerEscape(key)
			if seen[key] {
				return malformed(p, "duplicate", "the member appears twice", nil)
			}
			seen[key] = true
			if err := scanValue(dec, p); err != nil {
				return err
			}
		}
	case '[':
		for i := 0; dec.More(); i++ {
			if err := scanValue(dec, ptr+"/"+strconv.Itoa(i)); err != nil {
				return err
			}
		}
	}
	if _, err := dec.Token(); err != nil {
		return malformed(ptr, "syntax", "the body is not valid JSON", err)
	}
	return nil
}

func unmarshalerType() reflect.Type { return reflect.TypeFor[json.Unmarshaler]() }

// checkUnknown walks the document next to the type that will receive it and
// reports the first member (in sorted order) that the type has no field for.
func checkUnknown(data []byte, t reflect.Type) error {
	if t == nil {
		return nil
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return malformed("", "syntax", "the body is not valid JSON", err)
	}
	if p, ok := unknownMember(doc, t, ""); ok {
		return malformed(p, "unknown", "the member is not part of the resource", nil)
	}
	return nil
}

// unknownMember returns the pointer of the first member of v that t does not
// declare.
func unknownMember(v any, t reflect.Type, ptr string) (string, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if reflect.PointerTo(t).Implements(unmarshalerType()) || t.Kind() == reflect.Interface {
		return "", false
	}
	switch val := v.(type) {
	case map[string]any:
		return unknownInObject(val, t, ptr)
	case []any:
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			return "", false
		}
		for i, e := range val {
			if p, ok := unknownMember(e, t.Elem(), ptr+"/"+strconv.Itoa(i)); ok {
				return p, true
			}
		}
	}
	return "", false
}

func unknownInObject(val map[string]any, t reflect.Type, ptr string) (string, bool) {
	keys := make([]string, 0, len(val))
	for k := range val {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	switch t.Kind() {
	case reflect.Map:
		for _, k := range keys {
			if p, ok := unknownMember(val[k], t.Elem(), ptr+"/"+pointerEscape(k)); ok {
				return p, true
			}
		}
	case reflect.Struct:
		fields := jsonFields(t)
		for _, k := range keys {
			p := ptr + "/" + pointerEscape(k)
			ft, known := fields[k]
			if !known {
				return p, true
			}
			if q, ok := unknownMember(val[k], ft, p); ok {
				return q, true
			}
		}
	}
	return "", false
}

// jsonFields maps the JSON member names of a struct type to the types of their
// fields, flattening embedded structs as encoding/json does.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				for k, v := range jsonFields(ft) {
					out[k] = v
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}
