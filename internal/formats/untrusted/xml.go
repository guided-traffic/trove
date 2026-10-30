package untrusted

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
)

var encodingDecl = regexp.MustCompile(`encoding\s*=\s*["']([^"']*)["']`)

// CheckXML walks the tokens of an XML document and refuses what R-ARCH-028
// forbids: any declaration (a document type declaration among them, so no entity
// is ever defined and none is expanded), an element nesting deeper than
// Limits.XMLDepth, and a document that is not well-formed. An encoding other than
// UTF-8 is refused, because no charset reader is installed.
func CheckXML(data []byte, l Limits) error {
	l = effective(l)
	dec := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	for {
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Refused("the document is not well-formed XML", errs.WithCause(err))
		}
		switch t := tok.(type) {
		case xml.Directive:
			return Refused("the document holds a declaration; a document type declaration is not accepted")
		case xml.StartElement:
			depth++
			if depth > l.XMLDepth {
				return TooLarge(fmt.Sprintf("the document is nested deeper than %d elements", l.XMLDepth))
			}
		case xml.EndElement:
			depth--
		case xml.ProcInst:
			if m := encodingDecl.FindSubmatch(t.Inst); t.Target == "xml" && m != nil && !strings.EqualFold(string(m[1]), "utf-8") {
				return Refused("the document is not UTF-8")
			}
		}
	}
	if depth != 0 {
		return Refused("the document is not well-formed XML")
	}
	return nil
}

// DecodeXML reads one XML document from r into v within the document bound,
// after CheckXML.
func DecodeXML(r io.Reader, l Limits, v any) error {
	data, err := ReadAll(r, effective(l).Bound())
	if err != nil {
		return err
	}
	return UnmarshalXML(data, l, v)
}

// UnmarshalXML is DecodeXML over bytes the caller already holds.
func UnmarshalXML(data []byte, l Limits, v any) error {
	if err := CheckXML(data, l); err != nil {
		return err
	}
	if err := xml.Unmarshal(data, v); err != nil {
		return Refused("the document is not XML of the expected shape", errs.WithCause(err))
	}
	return nil
}
