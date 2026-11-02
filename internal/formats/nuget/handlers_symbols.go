package nuget

import (
	"bytes"
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/id"
	"github.com/guided-traffic/trove/internal/formats/nuget/nupkg"
	"github.com/guided-traffic/trove/internal/formats/nuget/nuspec"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/symbols"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// incomingSymbols is a symbol package that passed the archive, .nuspec and symbol rules of
// R-NUGET-020: the identity of the version it belongs to, normalized, and what it holds.
type incomingSymbols struct {
	declaredID, lowerID, lowerVersion string
	summary                           symbols.Summary
}

func (in *incomingSymbols) path() string { return SnupkgPath(in.lowerID, in.lowerVersion) }

// symbolsNotDeclared is what a package without the type SymbolsPackage is told on the
// symbol road; a legacy .symbols.nupkg is one of these (R-NUGET-020).
const symbolsNotDeclared = "This is no symbol package: a .snupkg declares the package type SymbolsPackage. Legacy .symbols.nupkg packages are not accepted; build a .snupkg (dotnet pack -p:SymbolPackageFormat=snupkg)."

// readSymbols runs the archive and .nuspec rules of R-NUGET-005 steps 1 to 4 that apply to a
// symbol package, then the rules of R-NUGET-020: the package type, the entry extensions and
// the signature of every .pdb. A refusal is a 400 *errs.Error; a cancelled context returns
// its own error. The dependencies of a symbol package are not read: it is no dependency of
// anything.
func readSymbols(ctx context.Context, sp formats.Spooled, lim nupkg.Limits) (*incomingSymbols, error) {
	ra, err := sp.ReaderAt()
	if err != nil {
		return nil, err
	}
	res, err := nupkg.Inspect(ctx, ra, sp.Size(), lim)
	if err != nil {
		return nil, err
	}
	m := res.Metadata
	if err := id.ValidateLocal(m.ID); err != nil {
		return nil, err
	}
	v, err := version.Parse(m.Version)
	if err != nil {
		return nil, err
	}
	if !m.IsSymbols() {
		return nil, nuspec.Invalid(symbols.ErrNotSymbols, symbolsNotDeclared)
	}
	sum, err := symbols.Validate(ctx, res.Archive)
	if err != nil {
		return nil, err
	}
	return &incomingSymbols{declaredID: m.ID, lowerID: id.Lower(m.ID), lowerVersion: v.URLForm(), summary: sum}, nil
}

// handleSymbolPush is PUT v3/symbolpackage (R-NUGET-020, flow F1). It validates and stores
// the .snupkg of a version that is stored already, and extracts nothing: indexing the
// symbols is a job the commit enqueues (Q-NUGET-11). The matching .nupkg is looked up in
// PostgreSQL, so the package push and this one may reach different replicas.
//
// The same bytes again are 201 without change; other bytes for a version that holds a
// symbol package are 409 (symbols must match the binaries of the immutable package). The
// permission and the ownership are those of a push of the declared ID, decided before the
// package is looked up, so a caller who may not write the ID learns nothing of its versions.
func (f *Format) handleSymbolPush(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	rep := f.replicaOf(rq.Host)
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	body, length, err := packagePart(rq)
	if err != nil {
		return err
	}
	sp, err := rq.Host.Ingest().Spool(ctx, rq, body, formats.IngestOptions{MediaType: "application/octet-stream", Length: length})
	if err != nil {
		return ioError(err)
	}
	in, err := readSymbols(ctx, sp, rep.archiveLimits())
	if err != nil {
		return ioError(err)
	}
	target := formats.Target{Package: in.lowerID, Version: in.lowerVersion, Actions: []formats.Action{formats.ActionDeploy}}
	if err := rq.Host.Authorize(ctx, rq, target); err != nil {
		return err
	}
	if err := rq.Host.CheckOwnership(ctx, rq, target, formats.OwnershipWrite{Publish: true}); err != nil {
		return err
	}
	repo := rq.RepoRef()
	if _, found, err := st.VersionFacts(ctx, repo.ID, in.lowerID, in.lowerVersion); err != nil {
		return err
	} else if !found {
		return noPackageForSymbols(in)
	}
	if done, err := f.symbolsStored(ctx, rq, w, in, sp.Digests().SHA256); done || err != nil {
		return err
	}
	insp := &formats.Inspection{
		Package: in.lowerID, Version: in.lowerVersion,
		Properties: map[string]string{"nuget.id": in.lowerID, "nuget.version": in.lowerVersion},
	}
	_, err = rq.Host.Ingest().Commit(ctx, rq, sp, formats.Commit{
		Path: formats.Path{Path: in.path()}, MediaType: "application/octet-stream", Inspection: insp,
		Payload: &symbolCommit{in: in},
	})
	if err != nil {
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.Forbidden || e.Code != errs.CodeForbidden {
			return err
		}
		// The path took other content meanwhile: a concurrent push of this symbol package
		// won, and the stored bytes decide.
		if done, serr := f.symbolsStored(ctx, rq, w, in, sp.Digests().SHA256); done || serr != nil {
			return serr
		}
		return err
	}
	w.WriteHeader(http.StatusCreated)
	return nil
}

// symbolsStored applies the immutability rule to the symbol package of a version: the same
// bytes are an acknowledged retry (201, nothing changes), other bytes are refused with 409.
// done says that it answered.
func (f *Format) symbolsStored(ctx context.Context, rq *formats.Request, w http.ResponseWriter, in *incomingSymbols, sha256 []byte) (bool, error) {
	it, found, err := rq.Host.Items().Stat(ctx, rq.RepoRef(), formats.Path{Path: in.path()})
	if err != nil || !found {
		return false, err
	}
	if !bytes.Equal(it.Digests.SHA256, sha256) {
		return false, symbolsExist(in)
	}
	w.WriteHeader(http.StatusCreated)
	return true, nil
}

// symbolCommit travels from the symbol push to OnCommit as the payload of the commit of the
// .snupkg.
type symbolCommit struct{ in *incomingSymbols }

// commitSymbols runs in the transaction that stored the .snupkg: it takes the package row
// lock, finds the live version the symbols belong to, links the item to it (so that the
// removal of the version takes the symbol package with it, R-NUGET-007) and records the
// symbol package, which enqueues its indexing job (Q-NUGET-11). A version that is gone by
// now is a 404 and rolls the item back.
func (f *Format) commitSymbols(ctx context.Context, tx formats.Tx, c formats.CommitEvent, p *symbolCommit) error {
	st, err := storeOf(c.Data)
	if err != nil {
		return err
	}
	in := p.in
	pkg, _, err := c.Packages.EnsureLocked(ctx, c.Repo, in.lowerID, in.declaredID)
	if err != nil {
		return err
	}
	live, err := c.Packages.Versions(ctx, pkg)
	if err != nil {
		return err
	}
	for _, v := range live {
		if v.Version != in.lowerVersion {
			continue
		}
		if err := c.Packages.Claim(ctx, formats.Target{Package: in.lowerID, Version: in.lowerVersion, Actions: []formats.Action{formats.ActionDeploy}},
			formats.OwnershipWrite{Publish: true}); err != nil {
			return err
		}
		if err := c.Packages.LinkItem(ctx, c.Item, v.ID, false); err != nil {
			return err
		}
		if err := c.Packages.SetProperties(ctx, c.Item, map[string]string{"nuget.id": in.lowerID, "nuget.version": in.lowerVersion}); err != nil {
			return err
		}
		fresh, err := st.InsertSymbolPackage(ctx, tx, rows.SymbolPackageInput{
			VersionID: int64(v.ID), ItemID: c.Item.ID, RepositoryID: c.Repo.ID,
		})
		if err != nil {
			return err
		}
		if !fresh {
			return symbolsExist(in)
		}
		return nil
	}
	return noPackageForSymbols(in)
}

func noPackageForSymbols(in *incomingSymbols) error {
	return errs.E(errs.NotFound, errs.CodeNotFound,
		"The repository holds no package "+in.declaredID+" "+in.lowerVersion+" for these symbols; push the package first.")
}

func symbolsExist(in *incomingSymbols) error {
	return errs.E(errs.Conflict, errs.CodeNuGetSymbolsExist,
		in.declaredID+" "+in.lowerVersion+" has a symbol package already; symbols must match the binaries of the immutable package, so other bytes are never accepted.")
}
