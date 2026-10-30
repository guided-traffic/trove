package npm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/manifest"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// Unpublish (R-NPM-017, flow F6). npm removes a version in three requests: the
// document without the version (PUT {name}/-rev/{rev}), then the version's tarball
// (DELETE .../-rev/{rev}), which this answers as the no-op it is; the removal of a
// whole package is a DELETE of the document. Each needs delete and the ownership of
// the name, and the current revision (R-NPM-009), all checked in the transaction under
// the package row lock.
//
// The files of the removed versions go through the package writer (R-STOR-018,
// permanently while the trash can does not exist), and OnItemsRemoved does the rest,
// so this route and a raw delete through /files/ unpublish the same way (Q-NPM-27).

// handleUnpublishPut is PUT {name}/-rev/{rev} with the document as the client wants it
// to be: every live version absent from `versions` is removed. Nothing else of the body
// is applied (dist-tags are recomputed, R-NPM-016), and a version the body names that
// the package does not hold is a 400.
func (f *Format) handleUnpublishPut(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	n, err := name.Parse(rq.Params.Get("name"))
	if err != nil {
		return err
	}
	keep, err := versionsOfBody(w, rq)
	if err != nil {
		return err
	}
	return f.unpublish(ctx, rq, w, n, rq.Params.Get("rev"), func(live []rows.LiveVersion) ([]string, error) {
		var remove []string
		for _, v := range live {
			if !slices.Contains(keep, v.Version) {
				remove = append(remove, v.Version)
			}
		}
		for _, k := range keep {
			if !slices.ContainsFunc(live, func(v rows.LiveVersion) bool { return v.Version == k }) {
				return nil, manifest.BadPublish("the document names the version " + k + ", which the package does not hold")
			}
		}
		return remove, nil
	})
}

// handleUnpublishPackage is DELETE {name}/-rev/{rev}: every live version is removed
// and with them all dist-tags. The packages row stays, and rev_seq never decreases.
func (f *Format) handleUnpublishPackage(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	n, err := name.Parse(rq.Params.Get("name"))
	if err != nil {
		return err
	}
	return f.unpublish(ctx, rq, w, n, rq.Params.Get("rev"), func(live []rows.LiveVersion) ([]string, error) {
		out := make([]string, len(live))
		for i, v := range live {
			out[i] = v.Version
		}
		return out, nil
	})
}

// handleUnpublishTarball is DELETE {name}/-/{file}.tgz/-rev/{rev}, which npm sends after
// the PUT. The PUT removed the version with its tarball, so there is nothing to do: a
// version that is gone answers 200, one that is still published 400. The revision is
// not compared, because nothing changes.
func (f *Format) handleUnpublishTarball(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	n, err := name.Parse(rq.Params.Get("name"))
	if err != nil {
		return err
	}
	version, ok := VersionOfFile(n, rq.Params.Get("file"), tarballSuffix)
	if !ok {
		return notFound()
	}
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: n.String(), Version: version, Actions: []formats.Action{formats.ActionDelete}}); err != nil {
		return err
	}
	st, repo, err := f.writeTarget(ctx, rq, n)
	if err != nil {
		return err
	}
	facts, err := st.VersionFacts(ctx, repo.ID, n.String(), version)
	if err != nil {
		return err
	}
	if facts.Live && !facts.Tombstoned {
		return manifest.BadPublish("the version " + version + " is still published; remove it with the package document first")
	}
	row, _, err := rq.Host.Packages().Package(ctx, repo, n.String())
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rev": Rev(repo.ID, row.Ref.ID, row.Ref.RevSeq)})
}

// unpublish is the transaction of both writes of the document. pick chooses the
// versions to remove among those the document holds. A request whose rev is not the
// current one still succeeds when it removes nothing (R-NPM-009): npm retries a
// request whose answer was lost, and the retry carries the revision of the attempt
// that already committed.
func (f *Format) unpublish(ctx context.Context, rq *formats.Request, w http.ResponseWriter, n name.Name, rev string,
	pick func(live []rows.LiveVersion) ([]string, error),
) error {
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: n.String(), Actions: []formats.Action{formats.ActionDelete}}); err != nil {
		return err
	}
	st, repo, err := f.writeTarget(ctx, rq, n)
	if err != nil {
		return err
	}
	var newRev string
	rm := removal{source: "unpublish", purge: true, by: rq.Subject.ID, audit: auditOf(rq)}
	if rm.by == "" {
		rm.by = noActor
	}
	err = rq.Host.Write(ctx, rq, func(tx formats.Tx, pw formats.PackageWriter) error {
		pkg, _, err := pw.EnsureLocked(ctx, repo, n.String(), n.String())
		if err != nil {
			return err
		}
		if err := f.claim(ctx, pw, opUnpublish, n.String(), "", false); err != nil {
			return err
		}
		live, err := st.LiveVersions(ctx, tx, int64(pkg.ID))
		if err != nil {
			return err
		}
		remove, err := pick(live)
		if err != nil {
			return err
		}
		current := Rev(repo.ID, pkg.ID, pkg.RevSeq)
		if len(remove) == 0 {
			newRev = current // nothing to remove: the request changes nothing, whatever its rev
			return nil
		}
		if rev != current {
			return errs.E(errs.Conflict, errs.CodeNPMStaleRev, "The package changed after the document was read; read it again with ?write=true.")
		}
		paths := make([]string, 0, 2*len(remove))
		for _, v := range remove {
			paths = append(paths, TarballPath(n, v), SigstorePath(n, v))
		}
		if err := pw.DeleteItems(ctx, repo, paths...); err != nil {
			return err
		}
		// A version whose files were not there to remove (rows without an item) is
		// removed all the same.
		left, err := st.LiveVersions(ctx, tx, int64(pkg.ID))
		if err != nil {
			return err
		}
		var rest []string
		for _, v := range left {
			if slices.Contains(remove, v.Version) {
				rest = append(rest, v.Version)
			}
		}
		if _, err := f.removeVersions(ctx, tx, st, pw, repo, pkg, rest, rm); err != nil {
			return err
		}
		after, _, err := pw.BumpRevision(ctx, pkg)
		if err != nil {
			return err
		}
		newRev = Rev(repo.ID, pkg.ID, after)
		return nil
	})
	if err != nil {
		return err
	}
	return writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rev": newRev})
}

// versionsOfBody reads the version numbers of the `versions` member of the document
// an unpublish sends back. The document may be megabytes (npm sends the whole of it),
// so it is read as a stream and only the keys are kept.
func versionsOfBody(w http.ResponseWriter, rq *formats.Request) ([]string, error) {
	dec := json.NewDecoder(http.MaxBytesReader(w, rq.Body, MaxDocumentBytes))
	bad := func(err error) error {
		var big *http.MaxBytesError
		if errors.As(err, &big) {
			return manifest.TooLarge("the document is larger than the limit of documents held in memory")
		}
		return manifest.BadPublish("the document is not valid JSON or holds no versions object" + causeText(err))
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, bad(err)
	}
	var keys []string
	seen := false
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, bad(err)
		}
		if t != "versions" {
			if err := skipValue(dec); err != nil {
				return nil, bad(err)
			}
			continue
		}
		if seen {
			return nil, manifest.BadPublish("the document holds two versions members")
		}
		seen = true
		t, err = dec.Token()
		if err != nil || t != json.Delim('{') {
			return nil, bad(err)
		}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, bad(err)
			}
			keys = append(keys, k.(string)) //nolint:forcetypeassert // an object key is a string
			if err := skipValue(dec); err != nil {
				return nil, bad(err)
			}
		}
		if _, err := dec.Token(); err != nil {
			return nil, bad(err)
		}
	}
	if !seen {
		return nil, manifest.BadPublish("the document holds no versions object")
	}
	if _, err := dec.Token(); err != nil && !errors.Is(err, io.EOF) {
		return nil, bad(err)
	}
	return keys, nil
}

func skipValue(dec *json.Decoder) error {
	var skip json.RawMessage
	return dec.Decode(&skip)
}

func causeText(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}
