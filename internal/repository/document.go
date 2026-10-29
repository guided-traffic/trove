package repository

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
)

// Hide-existence values of the document (R-AUTHZ-017).
const (
	HideInherit = "inherit"
	HideTrue    = "true"
	HideFalse   = "false"
)

// Limits of the free-text members (R-REPO-004), in characters.
const (
	MaxDescription = 2000
	MaxAdminNotes  = 8000
)

// Settings are the members of the configuration document that the database keeps
// in repositories.config: everything but the identity (key, class, format) and the
// derived layout. A member that does not apply to the class or format is absent
// (R-REPO-010).
type Settings struct {
	Description           string   `json:"description"`
	AdminNotes            string   `json:"adminNotes"`
	IncludePatterns       []string `json:"includePatterns"`
	ExcludePatterns       []string `json:"excludePatterns"`
	Disabled              bool     `json:"disabled"`
	RequireAuthentication bool     `json:"requireAuthentication"`
	// PriorityMember applies to local and remote repositories of a format with
	// priority members (R-REPO-022); absent otherwise.
	PriorityMember bool `json:"priorityMember,omitempty"`
	// HideExistence is "inherit", "true" or "false".
	HideExistence string         `json:"hideExistence"`
	Local         *LocalSettings `json:"local,omitempty"`
	// Remote and Virtual are the class sections of R-REPO-024 and R-REPO-017. They
	// are carried as written until a format supports the class; the generic format
	// supports only local, so a repository never holds one in milestone M1.
	Remote  json.RawMessage `json:"remote,omitempty"`
	Virtual json.RawMessage `json:"virtual,omitempty"`
	// Storage is the member owned by storage.md (upload size, quota, presigned
	// downloads, checksum policy). The model keeps it as an opaque JSON object.
	Storage json.RawMessage `json:"storage,omitempty"`
}

// LocalSettings is the `local` section of a document. Which members apply
// depends on the format (R-REPO-004, R-GENERIC-008): none of them applies to a
// format without package names.
type LocalSettings struct {
	PackageOwnership *string         `json:"packageOwnership,omitempty"`
	OwnershipDomain  *string         `json:"ownershipDomain,omitempty"`
	AutoClaim        *bool           `json:"autoClaim,omitempty"`
	Caps             json.RawMessage `json:"caps,omitempty"`
}

// Document is the configuration document of a repository (R-REPO-004): the body
// of a create, the response of a read and the object of configuration as code.
type Document struct {
	Key    string `json:"key"`
	Class  Class  `json:"class"`
	Format string `json:"format"`
	// Layout is read-only and equals the format's layout (R-REPO-006).
	Layout string `json:"layout,omitempty"`
	Settings
}

// Warning is a finding that does not block a save (R-REPO-008).
type Warning struct {
	Code    string `json:"code"`
	Pointer string `json:"pointer,omitempty"`
	Detail  string `json:"detail"`
}

// ConsumerView is what a caller who may read a repository but not its
// configuration sees (R-REPO-010): no notes, patterns, upstream settings or
// members.
type ConsumerView struct {
	Key         string   `json:"key"`
	Class       Class    `json:"class"`
	Format      string   `json:"format"`
	Description string   `json:"description"`
	Layout      string   `json:"layout"`
	Addresses   []string `json:"addresses"`
}

// WithoutAdminNotes returns the document as a caller without
// repositories:read-config sees the shared members (R-REPO-004: the notes are for
// holders of that capability).
func (d Document) WithoutAdminNotes() Document {
	d.AdminNotes = ""
	return d
}

// ConsumerView renders the consumer view. protocolBase is the format's
// ProtocolBasePath template ("/files/{key}/"); the raw file address is always
// listed (R-REPO-002).
func (d Document) ConsumerView(protocolBase string) ConsumerView {
	v := ConsumerView{Key: d.Key, Class: d.Class, Format: d.Format, Description: d.Description, Layout: d.Layout}
	seen := map[string]bool{}
	for _, tmpl := range []string{protocolBase, "/files/{key}/"} {
		if tmpl == "" {
			continue
		}
		a := strings.ReplaceAll(tmpl, "{key}", d.Key)
		if !seen[a] {
			seen[a] = true
			v.Addresses = append(v.Addresses, a)
		}
	}
	return v
}

// normalize fills the defaults of omitted members, so that what is stored and
// what is read back is one canonical form.
func (s *Settings) normalize() {
	if s.IncludePatterns == nil {
		s.IncludePatterns = []string{}
	}
	if s.ExcludePatterns == nil {
		s.ExcludePatterns = []string{}
	}
	if s.HideExistence == "" {
		s.HideExistence = HideInherit
	}
	if s.Local != nil && s.Local.PackageOwnership == nil && s.Local.OwnershipDomain == nil &&
		s.Local.AutoClaim == nil && len(s.Local.Caps) == 0 {
		s.Local = nil
	}
	s.Remote = emptyToNil(s.Remote)
	s.Virtual = emptyToNil(s.Virtual)
	s.Storage = emptyToNil(s.Storage)
}

// emptyToNil treats a JSON null like an absent member.
func emptyToNil(m json.RawMessage) json.RawMessage {
	if len(m) == 0 || bytes.Equal(bytes.TrimSpace(m), []byte("null")) {
		return nil
	}
	return m
}

// marshalConfig is the content of repositories.config.
func (s Settings) marshalConfig() ([]byte, error) {
	s.normalize()
	return json.Marshal(s)
}

func invalidDocument(format string, a ...any) *errs.Error {
	return errs.E(errs.Invalid, errs.CodeInvalidDocument, fmt.Sprintf(format, a...))
}

// DecodeDocument reads a configuration document strictly: it must be one JSON
// object, without an unknown or a duplicate member and without trailing data
// (R-REPO-008: a configuration API that silently drops a misspelt setting leaves
// the operator believing it is in effect). The error is 400 invalid_document.
func DecodeDocument(raw []byte) (Document, error) {
	var d Document
	if err := decodeStrict(raw, &d); err != nil {
		return Document{}, err
	}
	d.normalize()
	return d, nil
}

// decodeStrict decodes raw into v, refusing unknown and duplicate members.
func decodeStrict(raw []byte, v any) error {
	if err := checkDuplicates(raw); err != nil {
		return invalidDocument("%s", err.Error())
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalidDocument("%s", describeJSONError(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return invalidDocument("the body holds more than one JSON value")
	}
	return nil
}

func describeJSONError(err error) string {
	var ute *json.UnmarshalTypeError
	var se *json.SyntaxError
	switch {
	case errors.As(err, &ute):
		return fmt.Sprintf("member %q has the wrong type (%s expected)", ute.Field, ute.Type)
	case errors.As(err, &se):
		return "the body is not valid JSON"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "the body is empty or incomplete"
	}
	return strings.TrimPrefix(err.Error(), "json: ")
}

// checkDuplicates walks the token stream and reports an object that names a
// member twice.
func checkDuplicates(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	type frame struct {
		keys   map[string]bool
		object bool
		wantK  bool
	}
	var st []frame
	for {
		tok, err := dec.Token()
		if err != nil {
			// The end, or a syntax error, which the decoder reports with a better
			// message: this check is only about duplicate members.
			return nil //nolint:nilerr // see above
		}
		if t, ok := tok.(json.Delim); ok {
			switch t {
			case '{':
				st = append(st, frame{keys: map[string]bool{}, object: true, wantK: true})
			case '[':
				st = append(st, frame{})
			default:
				st = st[:len(st)-1]
				if n := len(st) - 1; n >= 0 && st[n].object {
					st[n].wantK = true
				}
			}
			continue
		}
		top := len(st) - 1
		if top < 0 || !st[top].object {
			continue
		}
		if st[top].wantK {
			key, _ := tok.(string)
			if st[top].keys[key] {
				return fmt.Errorf("member %q appears twice", key)
			}
			st[top].keys[key] = true
			st[top].wantK = false
			continue
		}
		st[top].wantK = true // a scalar value ends the member
	}
}

// MergePatch applies an RFC 7396 merge patch to a JSON document. An object member
// of the patch replaces, null removes, an absent member stays, and an array is
// replaced as a whole (R-REPO-011). The patch must be a JSON object.
func MergePatch(doc, patch []byte) ([]byte, error) {
	if err := checkDuplicates(patch); err != nil {
		return nil, invalidDocument("%s", err.Error())
	}
	p, err := decodeAny(patch)
	if err != nil {
		return nil, invalidDocument("%s", describeJSONError(err))
	}
	if _, ok := p.(map[string]any); !ok {
		return nil, invalidDocument("a merge patch must be a JSON object")
	}
	d, err := decodeAny(doc)
	if err != nil {
		return nil, fmt.Errorf("repository: stored document: %w", err)
	}
	return json.Marshal(mergeValue(d, p))
}

func decodeAny(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("the body holds more than one JSON value")
	}
	return v, nil
}

func mergeValue(target, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	tm, ok := target.(map[string]any)
	if !ok {
		tm = map[string]any{}
	}
	for k, v := range pm {
		if v == nil {
			delete(tm, k)
			continue
		}
		tm[k] = mergeValue(tm[k], v)
	}
	return tm
}
