package v1

import (
	"encoding/json"
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
)

// The merge patches of the identity resources (R-API-013). A patch body is
// decoded into a struct whose members are json.RawMessage, so an absent member
// (nil), a null (reset to the default) and a value are three different things; the
// members the resource shows read only are part of the struct and ignored, so a
// body obtained by GET can be sent back.

func itoa(n int) string { return strconv.Itoa(n) }

// patchString reads an optional string member: absent is nil, null is the empty
// string (the default), a string is itself.
func patchString(raw json.RawMessage, pointer string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	if string(raw) == "null" {
		return refTo(""), nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, malformed(pointer, "type", "the value must be a string or null", err)
	}
	return &v, nil
}

// patchBool reads an optional boolean member: null is false.
func patchBool(raw json.RawMessage, pointer string) (*bool, error) {
	if raw == nil {
		return nil, nil
	}
	if string(raw) == "null" {
		return refTo(false), nil
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, malformed(pointer, "type", "the value must be a boolean or null", err)
	}
	return &v, nil
}

// patchRequiredString reads a member that has no default: null is refused.
func patchRequiredString(raw json.RawMessage, pointer string) (*string, error) {
	if raw != nil && string(raw) == "null" {
		return nil, errs.E(errs.Invalid, errs.CodeValidation, "The member cannot be removed.",
			errs.WithFields(errs.FieldError{Pointer: pointer, Code: "required", Detail: "the member has no default"}))
	}
	return patchString(raw, pointer)
}

// immutableString answers 422 when a patch names a different value for a member
// that cannot change. An equal value is ignored (R-API-013).
func immutableString(raw json.RawMessage, pointer, current string) error {
	if raw == nil {
		return nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil || v != current {
		return errs.E(errs.Invalid, errs.CodeValidation, "An immutable member was changed.",
			errs.WithFields(errs.FieldError{Pointer: pointer, Code: "immutable", Detail: "the member cannot be changed after the resource was created"}))
	}
	return nil
}

// errorsFirst runs the steps in order and returns the first error.
func errorsFirst(steps ...func() error) error {
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}
