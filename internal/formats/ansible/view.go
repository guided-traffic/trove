package ansible

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
)

// viewAdapter is the format's part of the package screens (R-ANS-026, R-API-029): the description, the tags, the licenses, the README and
// the Galaxy metadata of a version of a local repository, read through the data port. The local class sets it as the format's View.
type viewAdapter struct{}

var _ formats.ViewAdapter = viewAdapter{}

func viewOf(ports formats.DataPorts) (ViewStore, error) {
	if ports != nil {
		if p, ok := ports.FormatData(ID).(ViewStore); ok {
			return p, nil
		}
	}
	return nil, errs.E(errs.Internal, errs.CodeInternal, "ansible: the data port has no package view")
}

// Compare orders versions by semantic-version precedence (R-ANS-002), build metadata ignored. Two spellings that differ only in build
// metadata still compare unequal, so that the order is total over the strings the format stores; a string that is no version sorts
// before every version.
func (viewAdapter) Compare(a, b string) int {
	va, ea := version.Parse(a)
	vb, eb := version.Parse(b)
	switch {
	case ea != nil && eb != nil:
		return strings.Compare(a, b)
	case ea != nil:
		return -1
	case eb != nil:
		return 1
	}
	if c := version.Compare(va, vb); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// Stored lists what a local repository serves of the collection, newest first by precedence. A remote repository's cache is listed by
// the generic rows alone, and a virtual repository stores nothing of its own.
func (viewAdapter) Stored(ctx context.Context, ports formats.DataPorts, q formats.StoredQuery) (formats.Stored, error) {
	if q.Repo.Class != formats.Local {
		return formats.Stored{}, nil
	}
	p, err := viewOf(ports)
	if err != nil {
		return formats.Stored{}, err
	}
	vs, err := p.ViewStored(ctx, q.Repo.ID, q.Package, q.Limit)
	return formats.Stored{Versions: vs}, err
}

// Withheld lists the versions a tombstone withholds although the version is still stored: what a restore's replay leaves (R-ANS-029).
func (viewAdapter) Withheld(ctx context.Context, ports formats.DataPorts, repos []string) ([]formats.VersionID, error) {
	p, err := viewOf(ports)
	if err != nil {
		return nil, err
	}
	ids, err := p.ViewWithheld(ctx, repos)
	return versionIDs(ids), err
}

// Find asks the data port for the versions whose description or tags match.
func (viewAdapter) Find(ctx context.Context, ports formats.DataPorts, f formats.ViewFind, limit int) ([]formats.VersionID, error) {
	p, err := viewOf(ports)
	if err != nil {
		return nil, err
	}
	ids, err := p.ViewFind(ctx, f.Repos, f.Text, f.Keyword, limit+1)
	return versionIDs(ids), err
}

func versionIDs(ids []int64) []formats.VersionID {
	out := make([]formats.VersionID, len(ids))
	for i, id := range ids {
		out[i] = formats.VersionID(id)
	}
	return out
}

// Describe reads the description and the tags of the versions and, with ViewDetails, the licenses, the README flag and the Galaxy
// metadata; the README text only when asked for. The links carry the names the other formats use (homepage, repositoryUrl, bugsUrl, and
// documentationUrl in the same style), and the licenses and tags travel in the generic fields (R-ANS-026). A collection has no member that names other versions, so the visibility function is
// not needed.
func (viewAdapter) Describe(ctx context.Context, ports formats.DataPorts, vs []formats.ViewVersion, want formats.ViewFields, _ func(string) bool) (map[formats.VersionID]formats.ViewData, error) {
	p, err := viewOf(ports)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(vs))
	for i, v := range vs {
		ids[i] = int64(v.ID)
	}
	out := make(map[formats.VersionID]formats.ViewData, len(vs))
	if !want.Has(formats.ViewDetails) {
		ss, err := p.ViewSummaries(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, s := range ss {
			out[formats.VersionID(s.VersionID)] = formats.ViewData{Description: s.Description, Keywords: s.Tags}
		}
		return out, nil
	}
	ds, err := p.ViewDetails(ctx, ids, want.Has(formats.ViewReadme))
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		out[formats.VersionID(d.VersionID)] = describe(d)
	}
	return out, nil
}

// infoView is the part of the stored collection_info the screens read (R-ANS-010).
type infoView struct {
	Authors       []string        `json:"authors"`
	License       []string        `json:"license"`
	Readme        string          `json:"readme"`
	Repository    string          `json:"repository"`
	Homepage      string          `json:"homepage"`
	Documentation string          `json:"documentation"`
	Issues        string          `json:"issues"`
	LicenseFile   json.RawMessage `json:"license_file"`
}

func describe(d rows.ViewDetail) formats.ViewData {
	var info infoView
	_ = json.Unmarshal(d.CollectionInfo, &info) //nolint:errcheck // a column this format wrote; an unreadable one shows nothing
	md := map[string]any{"sha256": hex.EncodeToString(d.SHA256[:])}
	if d.RequiresAnsible != "" {
		md["requiresAnsible"] = d.RequiresAnsible
	}
	if len(d.Dependencies) > 0 {
		md["dependencies"] = d.Dependencies
	}
	if len(info.Authors) > 0 {
		md["authors"] = info.Authors
	}
	for k, v := range map[string]string{"repositoryUrl": info.Repository, "homepage": info.Homepage, "documentationUrl": info.Documentation, "bugsUrl": info.Issues} {
		if v != "" {
			md[k] = v
		}
	}
	return formats.ViewData{
		Description: d.Description, Keywords: d.Tags, Licenses: info.License, HasReadme: info.Readme != "",
		Metadata: md, Readme: d.Readme, ReadmeFile: d.ReadmeFile,
	}
}
