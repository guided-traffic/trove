package nuget

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/id"
	"github.com/guided-traffic/trove/internal/formats/nuget/nupkg"
	"github.com/guided-traffic/trove/internal/formats/nuget/nuspec"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
)

// incoming is a package that passed steps 1 to 4 of R-NUGET-005: the archive, the
// .nuspec and the identity it declares, normalized. It is the payload that travels from
// the push handler (or from Inspect) to OnCommit.
type incoming struct {
	result *nupkg.Result
	meta   *nuspec.Metadata
	// declaredID is the ID as the .nuspec spells it, lowerID the package name.
	declaredID, lowerID string
	version             version.Version
	// lowerVersion is the URL form, the identity of the version.
	lowerVersion string
}

// nupkgPath and nuspecPath are the canonical paths of the lead item and of the generated
// .nuspec of the incoming version (R-NUGET-005).
func (in *incoming) nupkgPath() string  { return NupkgPath(in.lowerID, in.lowerVersion) }
func (in *incoming) nuspecPath() string { return NuspecPath(in.lowerID, in.lowerVersion) }

// symbolPackageMessage is what a package with the type SymbolsPackage is told on the
// package road (R-NUGET-005 step 4).
const symbolPackageMessage = "This is a symbol package: push it to the symbol publish resource (PUT v3/symbolpackage), after its package was pushed."

// readIncoming runs steps 1 to 4 of R-NUGET-005 on a spooled package: the archive and
// the .nuspec within the limits (nupkg.Inspect), then the identity - the ID by the rule of
// a local repository, the version, every dependency ID by the upstream rule and every
// dependency range - and the refusal of a symbol package. A refusal is a 400 *errs.Error;
// a cancelled context returns its own error.
func readIncoming(ctx context.Context, sp formats.Spooled, lim nupkg.Limits) (*incoming, error) {
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
	if m.IsSymbols() {
		return nil, nuspec.Invalid(errSymbolPackage, symbolPackageMessage)
	}
	for _, g := range m.DependencyGroups {
		for _, d := range g.Dependencies {
			if err := id.ValidateUpstream(d.ID); err != nil {
				return nil, dependencyError(err, "the ID of a dependency")
			}
			if d.Range == "" {
				continue
			}
			if _, err := version.ParseRange(d.Range); err != nil {
				return nil, dependencyError(err, "the version range of the dependency "+d.ID)
			}
		}
	}
	return &incoming{
		result: res, meta: m, declaredID: m.ID, lowerID: id.Lower(m.ID), version: v, lowerVersion: v.URLForm(),
	}, nil
}

// errSymbolPackage is the cause of the refusal of a symbol package on the package road.
var errSymbolPackage = errors.New("the package declares the package type SymbolsPackage")

// dependencyError names what is wrong with a dependency in the detail of the refusal,
// keeping the code and the cause of the leaf that refused it.
func dependencyError(err error, what string) error {
	var e *errs.Error
	if errors.As(err, &e) {
		return errs.E(e.Kind, e.Code, what+" is refused: "+e.Detail, errs.WithCause(err))
	}
	return err
}

// metadataJSON is nuget_version.metadata of an incoming package (R-NUGET-005).
func (in *incoming) metadataJSON() ([]byte, error) {
	m := in.meta
	out := rows.Metadata{
		Title: m.Title, Summary: m.Summary, Description: m.Description, ReleaseNotes: m.ReleaseNotes,
		Copyright: m.Copyright, Language: m.Language, Authors: m.Authors, Tags: m.Tags,
		ProjectURL: m.ProjectURL, IconURL: m.IconURL, LicenseURL: m.LicenseURL, Icon: m.Icon, Readme: m.Readme,
		RequireLicenseAcceptance: m.RequireLicenseAcceptance, DevelopmentDependency: m.DevelopmentDependency,
		Serviceable: m.Serviceable, MinClientVersion: m.MinClientVersion,
	}
	if l := m.License; l != nil {
		out.License = &rows.License{Type: l.Type, Version: l.Version, Value: l.Value}
	}
	if r := m.Repository; r != nil {
		out.Repository = &rows.Repository{Type: r.Type, URL: r.URL, Branch: r.Branch, Commit: r.Commit}
	}
	for _, g := range m.DependencyGroups {
		group := rows.DependencyGroup{TargetFramework: g.TargetFramework}
		for _, d := range g.Dependencies {
			group.Dependencies = append(group.Dependencies, rows.Dependency{ID: d.ID, Version: d.Range, Include: d.Include, Exclude: d.Exclude})
		}
		out.DependencyGroups = append(out.DependencyGroups, group)
	}
	for _, t := range m.PackageTypes {
		out.PackageTypes = append(out.PackageTypes, rows.PackageType{Name: t.Name, Version: t.Version})
	}
	return json.Marshal(out)
}

// packageTypes are the lowercased names of the declared package types; a package that
// declares none has the type Dependency (R-NUGET-005).
func (in *incoming) packageTypes() []string {
	types := in.meta.Types()
	out := make([]string, len(types))
	for i, t := range types {
		out[i] = strings.ToLower(t.Name)
	}
	return out
}

// idTokens is the ID split at its separators ".", "-" and "_", lowercased and separated by
// spaces: the weight A part of the search document (R-NUGET-010).
func idTokens(lowerID string) string {
	return strings.Join(strings.FieldsFunc(lowerID, func(r rune) bool { return r == '.' || r == '-' || r == '_' }), " ")
}

// versionInput is the NuGet row of the incoming version. versionID is the id the core
// writer gave the version row.
func (in *incoming) versionInput(repo formats.RepoRef, versionID int64, c *pushCommit) (rows.VersionInput, error) {
	meta, err := in.metadataJSON()
	if err != nil {
		return rows.VersionInput{}, err
	}
	m := in.meta
	return rows.VersionInput{
		RepoID: repo.ID, VersionID: versionID, LowerID: in.lowerID, DeclaredID: in.declaredID,
		OriginalVersion: in.version.Original(), LowerVersion: in.lowerVersion, FullVersion: in.version.FullNormalized(), SortKey: in.version.SortKey(),
		IsPrerelease: in.version.IsPrerelease(), IsSemver2: in.version.IsSemver2(),
		PackageTypes: in.packageTypes(), Metadata: meta,
		IDTokens: idTokens(in.lowerID), Title: m.Title, Tags: strings.Join(m.Tags, " "), Authors: m.Authors,
		Summary: m.Summary, Description: m.Description, Origin: c.origin,
		SHA512: c.digests.SHA512,
		Files: []rows.File{{
			Path: in.nupkgPath(), SHA256: hexOf(c.digests.SHA256), Size: c.size,
		}},
		Audit: c.audit, Shadowing: c.shadowing,
	}, nil
}
