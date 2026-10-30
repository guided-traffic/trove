package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// JSON Merge Patch (RFC 7396, R-API-013). A PATCH body is an object: a present
// member replaces, null removes the member or resets it to its default, an
// absent member stays, an array is replaced as a whole.

// ReadMergePatch reads the body of a PATCH: Content-Type
// application/merge-patch+json, at most limit bytes, valid JSON without
// duplicate members, and an object (a patch that is not an object would replace
// the whole resource, which PUT is for).
func ReadMergePatch(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	body, err := ReadBody(w, r, MediaMergePatch, limit)
	if err != nil {
		return nil, err
	}
	if err := checkSyntax(body); err != nil {
		return nil, err
	}
	// null decodes into a nil map without an error, and as a patch it would replace
	// the whole resource.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil || probe == nil {
		return nil, malformed("", "type", "a merge patch must be a JSON object", err)
	}
	return body, nil
}

// MergePatch applies patch to original per RFC 7396 and returns the result as
// JSON. Both are JSON documents. It is the one implementation of the merge: the
// handlers of the API call it, and an area that stores the resource receives it
// as a function (Q-API-157). The patch is read strictly, as every request body is
// (a duplicate member and a nesting beyond maxBodyDepth are 400), and numbers
// keep their literal, so an integer beyond 2^53 survives the merge. Member order
// of the result is not preserved.
func MergePatch(original, patch []byte) ([]byte, error) {
	if err := checkSyntax(patch); err != nil {
		return nil, err
	}
	p, err := decodeNumbers(patch)
	if err != nil {
		return nil, malformed("", "syntax", "the patch is not valid JSON", err)
	}
	var o any
	if len(original) > 0 {
		if o, err = decodeNumbers(original); err != nil {
			return nil, errs.Wrap(err, "api.mergePatch")
		}
	}
	return json.Marshal(mergeNode(o, p))
}

// MergePatcher is the patch as the repository service applies it: the stored
// document in, the merged document out, inside the service's row lock
// (R-REPO-011, Q-API-157).
func MergePatcher(patch []byte) repository.Patcher {
	return func(current []byte) ([]byte, error) { return MergePatch(current, patch) }
}

// decodeNumbers decodes one JSON value and keeps numbers as [json.Number], the
// literal the document holds: float64 would round an integer beyond 2^53 and
// would make two different values equal.
func decodeNumbers(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("the document holds more than one JSON value")
	}
	return v, nil
}

// mergeNode is the MergePatch function of RFC 7396 section 2 on decoded values.
func mergeNode(target, patch any) any {
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
		tm[k] = mergeNode(tm[k], v)
	}
	return tm
}

// ApplyMergePatch applies a patch body (from ReadMergePatch) to the current
// representation of a resource and returns the patched representation, decoded
// strictly: a member the type does not declare answers 400 with its pointer. T
// is the type the resource is served as, so a body obtained by GET and patched
// parses (read-only members are part of it; the caller ignores them).
//
// The caller then compares immutable members with [CheckImmutable], validates
// the values, and writes with the revision from If-Match.
func ApplyMergePatch[T any](current T, patch []byte) (T, error) {
	var zero T
	cur, err := json.Marshal(current)
	if err != nil {
		return zero, errs.Wrap(err, "api.applyMergePatch")
	}
	merged, err := MergePatch(cur, patch)
	if err != nil {
		return zero, err
	}
	// The unknown-member check looks at the patch itself, so a pointer names a
	// member the client sent.
	var patchDoc any
	if err := json.Unmarshal(patch, &patchDoc); err == nil {
		if p, ok := unknownMember(patchDoc, reflect.TypeFor[T](), ""); ok {
			return zero, malformed(p, "unknown", "the member is not part of the resource", nil)
		}
	}
	var out T
	if err := StrictUnmarshal(merged, &out); err != nil {
		return zero, err
	}
	return out, nil
}

// CheckImmutable answers 422 validation_failed when a member the owner declares
// immutable (key, format, class, ...) differs between the stored resource and
// the patched one. An equal value is ignored, so a body obtained by GET can be
// patched back (R-API-013). names are JSON member names of the top level.
func CheckImmutable(before, after any, names ...string) error {
	b, err := members(before)
	if err != nil {
		return errs.Wrap(err, "api.checkImmutable")
	}
	a, err := members(after)
	if err != nil {
		return errs.Wrap(err, "api.checkImmutable")
	}
	var fields []errs.FieldError
	for _, n := range names {
		if !reflect.DeepEqual(b[n], a[n]) {
			fields = append(fields, errs.FieldError{Pointer: "/" + pointerEscape(n), Code: "immutable", Detail: "the member cannot be changed after the resource was created"})
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return errs.E(errs.Invalid, errs.CodeValidation, "An immutable member was changed.", errs.WithFields(fields...))
}

// members is the top-level members of v as JSON; numbers keep their literal, so
// two different large integers are different members.
func members(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	doc, err := decodeNumbers(raw)
	if err != nil {
		return nil, err
	}
	m, _ := doc.(map[string]any)
	return m, nil
}
