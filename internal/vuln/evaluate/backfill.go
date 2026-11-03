package evaluate

import (
	"context"
	"strings"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// ArtifactClassifier is an optional capability of an ecosystem's adapter: it names the package and the version a cached file of a
// remote repository is by its path. The format's fill records that version when the file is cached (Q-VULN-37); a file cached by
// an earlier release has none, and the backfill gives it one (Q-VULN-67). An adapter without it has nothing to backfill: NuGet's
// fill always wrote the row.
type ArtifactClassifier interface {
	// ArtifactSuffix is the file name suffix of the artifacts (".tgz"): the lookup of unversioned files narrows to it.
	ArtifactSuffix() string
	// ArtifactVersion returns the normalized package name and version of the artifact at the path, false for a path that is no
	// artifact of a package version.
	ArtifactVersion(path string) (name, version string, ok bool)
}

// backfillBatch is the number of cached files one transaction of the backfill looks at.
const backfillBatch = 500

// backfillVersions gives the cached artifacts of the ecosystem's remote repositories that belong to no version their package and
// version rows (Q-VULN-67), in batches of one transaction each with a cursor over the item ids; it returns how many it linked. It
// is idempotent: a file that has a version is not read, and a version that exists is linked, not created. It runs before the full
// evaluation of the ecosystem, which then evaluates the versions it made.
func (s *Service) backfillVersions(ctx context.Context, eco Ecosystem) (int, error) {
	cl, ok := eco.Adapter.(ArtifactClassifier)
	if !ok {
		return 0, nil
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(cl.ArtifactSuffix())
	after, linked := int64(0), 0
	for {
		if err := ctx.Err(); err != nil {
			return linked, context.Cause(ctx)
		}
		var n, count int
		next := after
		err := s.o.Runner.InTx(ctx, func(tx meta.Tx) error {
			n, count, next = 0, 0, after // a retried attempt starts its batch over
			ev, err := store.EvalOf(tx)
			if err != nil {
				return err
			}
			items, err := ev.UnversionedItems(ctx, eco.Formats, like, after, backfillBatch)
			if err != nil {
				return err
			}
			count = len(items)
			if count == 0 {
				return nil
			}
			w, err := packages.NewWriter(tx, "")
			if err != nil {
				return err
			}
			for _, it := range items {
				next = max(next, it.ItemID)
				name, version, ok := cl.ArtifactVersion(it.Path)
				if !ok {
					continue
				}
				if err := linkArtifact(ctx, w, it, name, version); err != nil {
					return err
				}
				n++
			}
			return nil
		})
		if err != nil {
			return linked, err
		}
		after, linked = next, linked+n
		if count < backfillBatch {
			return linked, nil
		}
	}
}

// linkArtifact makes the item the lead item of the version of the package, as the format's fill does.
func linkArtifact(ctx context.Context, w *packages.Writer, it store.UnversionedItem, name, version string) error {
	pkg, _, err := w.EnsureLocked(ctx, it.RepoID, name, name)
	if err != nil {
		return err
	}
	live, err := w.Versions(ctx, pkg)
	if err != nil {
		return err
	}
	for _, v := range live {
		if v.Version == version {
			return w.LinkItem(ctx, it.ItemID, v.ID, true)
		}
	}
	if _, err := w.InsertVersion(ctx, pkg, packages.NewVersion{Version: version, DisplayVersion: version, LeadItem: it.ItemID}); err != nil {
		return err
	}
	_, _, err = w.BumpRevision(ctx, pkg)
	return err
}
