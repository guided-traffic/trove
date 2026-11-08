package oci

import (
	"context"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci/manifest"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// Inspect reads an upload that enters the repository (R-OCI-020): a manifest, at the path of a manifest or a tag item, is untrusted
// input and is read by the manifest parser within the limits of the descriptor (4 MiB, 32 levels of nesting, 100,000 values, 10,000
// descriptors) before anything uses it. A document outside them or that is no manifest of the four types answers 400
// MANIFEST_INVALID, a bound crossed 413 SIZE_INVALID. A blob is not read: layers are never parsed or unpacked.
//
// The push handler parses the manifest itself, before the commit; Inspect is the same parser for an upload that reaches the pipeline
// another way, and the one the conformance kit and the fuzz target of the format drive.
func (f *Format) Inspect(_ context.Context, in formats.InspectInput) (*formats.Inspection, error) {
	if !isManifestPath(in.Path.Path) {
		return nil, nil
	}
	r, err := in.Body.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	lim := f.parseLimits().Tighten(in.Limits)
	data, err := untrusted.ReadAll(r, lim.Bound()+1)
	if err != nil {
		if untrusted.IsTooLarge(err) {
			return nil, errs.E(errs.TooLarge, errs.CodeOCISizeInvalid, "The manifest exceeds a limit of the registry.", errs.WithCause(err))
		}
		return nil, err
	}
	mt := in.MediaType
	if _, err := manifest.MediaTypeOf(mt); err != nil {
		// The pipeline's type is the client's Content-Type; a document that names its own type is read by it.
		mt = documentMediaType(data)
	}
	if _, err := manifest.Parse(data, mt, lim); err != nil {
		return nil, err
	}
	name, _, _, _ := splitLayout(in.Path.Path)
	return &formats.Inspection{Package: name}, nil
}

// documentMediaType is the mediaType member of a document, or "" for one without a readable member; the parser decides what it is
// worth.
func documentMediaType(data []byte) string {
	var doc struct {
		MediaType string `json:"mediaType"`
	}
	if untrusted.UnmarshalJSON(data, untrusted.Limits{}, &doc) != nil {
		return ""
	}
	return doc.MediaType
}

// isManifestPath reports whether a canonical path is the item of a manifest by digest or of a tag.
func isManifestPath(p string) bool {
	_, kind, _, ok := splitLayout(p)
	return ok && (kind == dirManifests || kind == dirTags)
}
