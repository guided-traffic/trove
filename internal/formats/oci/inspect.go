package oci

import (
	"context"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// Inspect reads an upload that enters the repository (R-OCI-020): a manifest, at the path of a manifest or a tag item, is
// untrusted input and is scanned within the limits of the descriptor (4 MiB, 32 levels of nesting, 100,000 values) before
// anything decodes it. A document outside them or that is not JSON answers 400 MANIFEST_INVALID, a bound crossed 413
// SIZE_INVALID. A blob is not read: layers are never parsed or unpacked.
//
// The manifest parser of R-OCI-013 (the media types, the references, the subject) is the work package of the manifest push; it
// replaces the generic decode below with the parse of the four manifest types.
func (f *Format) Inspect(_ context.Context, in formats.InspectInput) (*formats.Inspection, error) {
	if !isManifestPath(in.Path.Path) {
		return nil, nil
	}
	r, err := in.Body.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	var doc map[string]any
	if err := untrusted.DecodeJSON(r, in.Limits, &doc); err != nil {
		if untrusted.IsTooLarge(err) {
			return nil, errs.E(errs.TooLarge, errs.CodeOCISizeInvalid, "The manifest exceeds a limit of the registry.", errs.WithCause(err))
		}
		return nil, errs.E(errs.Invalid, errs.CodeOCIManifestInvalid, "The manifest is not a JSON object the registry accepts.", errs.WithCause(err))
	}
	name, _, _, _ := splitLayout(in.Path.Path)
	return &formats.Inspection{Package: name}, nil
}

// isManifestPath reports whether a canonical path is the item of a manifest by digest or of a tag.
func isManifestPath(p string) bool {
	_, kind, _, ok := splitLayout(p)
	return ok && (kind == dirManifests || kind == dirTags)
}
