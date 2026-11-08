package deb

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

func init() {
	registerHandler(routeUpload, func(f *Format) formats.HandlerFunc { return f.handleUpload })
}

// The bounds on the coordinates of a request (R-DEB-006, R-DEB-009).
const (
	maxValuesPerParameter = 16
	maxCoordinates        = 32
)

// parseCoordinates reads the suites and components of an upload: `distribution` is required and `component` defaults to main; both may
// repeat and may hold comma-separated values, at most 16 of each, and the file gets every combination as a coordinate.
func parseCoordinates(q url.Values) ([]rows.Coordinate, error) {
	dists, err := values(q["distribution"], "distribution")
	if err != nil {
		return nil, err
	}
	if len(dists) == 0 {
		return nil, errs.E(errs.Invalid, errs.CodeDebDistributionRequired, "An upload names the suite it is listed in with the parameter distribution.")
	}
	comps, err := values(q["component"], "component")
	if err != nil {
		return nil, err
	}
	if len(comps) == 0 {
		comps = []string{"main"}
	}
	if len(dists)*len(comps) > maxCoordinates {
		return nil, OutOfRange("/coordinates", "A file has at most 32 coordinates.")
	}
	var out []rows.Coordinate
	for _, d := range dists {
		for _, c := range comps {
			out = append(out, rows.Coordinate{Suite: d, Component: c})
		}
	}
	return out, nil
}

func values(raw []string, name string) ([]string, error) {
	var out []string
	for _, r := range raw {
		for _, v := range strings.Split(r, ",") {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if !ValidSuite(v) {
				return nil, errs.E(errs.Invalid, errs.CodeDebInvalidCoordinate,
					"The "+name+" "+quote(v)+" is outside the grammar ^[a-z0-9][a-z0-9._+-]{0,63}$.")
			}
			if !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	if len(out) > maxValuesPerParameter {
		return nil, errs.E(errs.Invalid, errs.CodeDebInvalidCoordinate, "A request names at most 16 values of "+name+".")
	}
	return out, nil
}

func quote(s string) string {
	if len(s) > 64 {
		s = s[:64] + "..."
	}
	return "\"" + s + "\""
}

// uploadCommit travels from the upload handler through the item store to OnCommit as the payload of the commit, and carries the result
// back.
type uploadCommit struct {
	in     *incoming
	coords []rows.Coordinate
	// shadowSkipped says the repository held a live version when the shadowing check ran, so nothing was asked: the commit must not
	// find the upload to be the first one then.
	shadowSkipped bool
	result        rows.UploadResult
}

func coordStrings(cs []rows.Coordinate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Suite+"/"+c.Component)
	}
	return out
}

// handleUpload is PUT upload/<file>?distribution=&component= (R-DEB-006, Q-DEB-3). The body is read once, as a stream into the spool;
// nothing is held in a transaction while bytes arrive (R-ARCH-019). What the package declares decides next, outside any transaction:
// the permission on the name and version, the ownership, whether the file is stored, withheld or free, and the shadowing check. The
// rules that make the commit safe (the package row lock, the ownership, the unique key of package, version and architecture) run again
// inside the commit transaction. The answer is sent when the commit is durable and never waits for an index (Q-DEB-25).
//
// The route is an upload to a local repository; a remote repository answers 405 before this is reached, and a virtual one is routed to
// its deployment target (R-DEB-028).
func (f *Format) handleUpload(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if !strings.HasSuffix(rq.Params.Get("file"), ".deb") {
		return errs.E(errs.Invalid, errs.CodeDebInvalidFileName, "The file name of an upload must end in .deb.")
	}
	coords, err := parseCoordinates(rq.URL.Query())
	if err != nil {
		return err
	}
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	sp, err := rq.Host.Ingest().Spool(ctx, rq, rq.Body, formats.IngestOptions{MediaType: MediaType, Length: rq.ContentLength})
	if err != nil {
		return ioError(err)
	}
	ra, err := sp.ReaderAt()
	if err != nil {
		return err
	}
	in, err := readPackage(ra, sp.Size(), f.Descriptor().Limits)
	if err != nil {
		return err
	}
	target := formats.Target{Package: in.name(), Version: in.version(), Path: in.path, Actions: []formats.Action{formats.ActionDeploy}}
	if err := rq.Host.Authorize(ctx, rq, target); err != nil {
		return err
	}
	repo := rq.RepoRef()
	live, _, err := rq.Host.Packages().Versions(ctx, repo, in.name())
	if err != nil {
		return err
	}
	if err := rq.Host.CheckOwnership(ctx, rq, target, formats.OwnershipWrite{Publish: true, FirstVersion: len(live) == 0}); err != nil {
		return err
	}
	sum := sp.Digests().SHA256
	if err := f.checkWithheld(ctx, st, repo, in.path, sum); err != nil {
		return err
	}
	existing, found, err := st.BinaryOf(ctx, nil, repo.ID, in.name(), in.version(), in.architecture())
	if err != nil {
		return err
	}
	switch {
	case found && string(existing.SHA256[:]) == string(sum):
		return f.addCoordinates(ctx, rq, w, st, in, existing, coords)
	case found:
		return versionExists(in)
	}
	if _, taken, err := rq.Host.Items().Stat(ctx, repo, formats.Path{Path: in.path}); err != nil {
		return err
	} else if taken {
		return filenameConflict(in)
	}
	skipped, err := f.shadowCheck(ctx, rq, in.name(), len(live))
	if err != nil {
		return err
	}
	pc := &uploadCommit{in: in, coords: coords, shadowSkipped: skipped}
	insp := &formats.Inspection{Package: in.name(), Version: in.version(), Properties: properties(in.pkg, coordStrings(coords)), Payload: in}
	if _, err := rq.Host.Ingest().Commit(ctx, rq, sp, formats.Commit{
		Path: formats.Path{Path: in.path}, MediaType: MediaType, CreateOnly: true, Inspection: insp, Payload: pc,
	}); err != nil {
		return err
	}
	return writeAnswer(w, r201, in, sum, pc.result)
}

const (
	r201 = http.StatusCreated
	r200 = http.StatusOK
)

// addCoordinates is the upload of bytes the repository stores already for the package, version and architecture: the coordinates of
// the request are added to the stored ones, nothing else changes, and the answer is 200 (Q-DEB-4).
func (f *Format) addCoordinates(ctx context.Context, rq *formats.Request, w http.ResponseWriter, st Store, in *incoming, existing rows.Binary, coords []rows.Coordinate) error {
	repo := rq.RepoRef()
	target := formats.Target{Package: in.name(), Version: in.version(), Path: in.path, Actions: []formats.Action{formats.ActionDeploy}}
	var res rows.UploadResult
	err := rq.Host.Write(ctx, rq, func(tx formats.Tx, pw formats.PackageWriter) error {
		pkg, _, err := pw.EnsureLocked(ctx, repo, in.name(), in.name())
		if err != nil {
			return err
		}
		if err := pw.Claim(ctx, target, formats.OwnershipWrite{Publish: true}); err != nil {
			return err
		}
		b := existing
		b.Coordinates = coords
		res, err = st.RecordUpload(ctx, tx, rows.UploadInput{Binary: b})
		if err != nil {
			return err
		}
		if len(res.Added) == 0 {
			return nil
		}
		if _, _, err := pw.BumpRevision(ctx, pkg); err != nil {
			return err
		}
		return pw.SetProperties(ctx, formats.ItemRef{ID: existing.ItemID, Repo: repo, Path: existing.Path}, properties(in.pkg, coordStrings(res.Coordinates)))
	})
	if err != nil {
		return err
	}
	return writeAnswer(w, r200, in, existing.SHA256[:], res)
}

// checkWithheld refuses the bytes a restore's replay withheld (R-DEB-032).
func (f *Format) checkWithheld(ctx context.Context, st Store, repo formats.RepoRef, path string, sum []byte) error {
	t, found, err := st.Tombstone(ctx, repo.ID, path)
	if err != nil {
		return err
	}
	if found && string(t.SHA256[:]) == string(sum) {
		return errs.E(errs.Conflict, errs.CodeDebWithheld,
			"These bytes were deleted before a restore and are withheld; an administrator reinstates them with trove admin deb reinstate.")
	}
	return nil
}

func versionExists(in *incoming) error {
	return errs.E(errs.Conflict, errs.CodeDebVersionExists,
		in.name()+" "+in.version()+" "+in.architecture()+" is stored already with other bytes; delete the file first, or upload the changed package under a new version.")
}

func filenameConflict(in *incoming) error {
	return errs.E(errs.Conflict, errs.CodeDebFilenameConflict,
		"Another version of "+in.name()+" has the file name "+in.path[strings.LastIndexByte(in.path, '/')+1:]+" (versions that differ only by their epoch); upload it under a new version.")
}

// shadowCheck is the shadowing check of a first upload of a name (R-DEB-006, R-REPO-048). It runs with no transaction open. A refusal
// is the 403 or 503 the check built. skipped says that the repository held a live version, so nothing was asked.
func (f *Format) shadowCheck(ctx context.Context, rq *formats.Request, name string, live int) (skipped bool, err error) {
	if live > 0 {
		return true, nil
	}
	out, err := rq.Host.Resolver().ShadowCheck(ctx, rq, name)
	if err != nil {
		return false, err
	}
	if !out.Proceed {
		if out.Refusal != nil {
			return false, out.Refusal
		}
		return false, errs.E(errs.Unavailable, errs.CodeShadowCheckUnavailable,
			"Whether a remote repository serves the package name could not be determined; try again.", errs.WithRetryAfter(out.RetryAfter))
	}
	return false, nil
}

func ioError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errs.E(errs.Unavailable, errs.CodeUnavailable, "The request ended before the upload was complete.", errs.WithCause(err))
	}
	return err
}

func storeOf(data any) (Store, error) {
	st, ok := data.(Store)
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The Debian data port is not bound.")
	}
	return st, nil
}
