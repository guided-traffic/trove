package ansible

import (
	"context"
	"errors"
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

// inspected is the Payload of the Inspection of an archive: what the reader made of it and the documentation generated from the same
// pass (R-ANS-034). docs is nil until the generator is wired into Inspect.
type inspected struct {
	archive *collection.Archive
	docs    *rows.DocSet
}

// Inspect reads an upload that enters the repository (R-ANS-010, FMT-008): a collection archive is untrusted input and is read within
// the bounds of R-ANS-031 by internal/formats/ansible/collection before anything uses it. The identity the manifest declares is the
// package and the version of the upload; a path that names another collection or version than the archive declares is refused
// (R-ANS-008 step 2, `path_mismatch`). An archive that cannot be a collection archive answers 422 `ansible_invalid_archive` (the
// publish route records it as a failed import task, R-ANS-009), a bound crossed 413.
func (f *Format) Inspect(ctx context.Context, in formats.InspectInput) (*formats.Inspection, error) {
	body, err := in.Body.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	a, err := collection.ReadArchive(ctx, body, in.Limits)
	if err != nil {
		return nil, archiveError(err)
	}
	name := a.Namespace + "." + a.Name
	if info := f.Classify(in.Path); info.Kind == formats.Content && info.Package != "" && (info.Package != name || info.Version != a.Version) {
		return nil, errs.E(errs.Unprocessable, errs.CodeAnsiblePathMismatch,
			"The path names "+info.Package+" "+info.Version+", the archive declares "+name+" "+a.Version+".")
	}
	return &formats.Inspection{
		Package: name, Version: a.Version,
		Properties: map[string]string{
			"ansible.namespace": a.Namespace, "ansible.name": a.Name, "ansible.requires_ansible": a.RequiresAnsible,
			"ansible.dependency_count": strconv.Itoa(len(a.Dependencies)),
		},
		Payload: &inspected{archive: a},
	}, nil
}

// archiveError maps the refusal of the archive reader to the error of the format: the name and the version have codes of their own
// (R-ANS-007), a bound crossed is 413 and every other reason is 422 ansible_invalid_archive with the reason in the detail. An error
// that is not a refusal (a failed read, an ended context) passes unchanged.
func archiveError(err error) error {
	var ce *collection.Error
	if !errors.As(err, &ce) {
		return err
	}
	switch {
	case ce.Bound:
		return errs.E(errs.TooLarge, errs.CodeTooLarge, ce.Detail, errs.WithCause(err))
	case ce.Reason == collection.ReasonInvalidName:
		return errs.E(errs.Unprocessable, errs.CodeAnsibleInvalidName, ce.Detail, errs.WithCause(err))
	case ce.Reason == collection.ReasonInvalidVersion:
		return errs.E(errs.Unprocessable, errs.CodeAnsibleInvalidVersion, ce.Detail, errs.WithCause(err))
	}
	return errs.E(errs.Unprocessable, errs.CodeAnsibleInvalidArchive, ce.Detail, errs.WithCause(err))
}
