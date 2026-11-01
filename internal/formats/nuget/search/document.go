package search

import (
	"encoding/json"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// The registration hives a search result links into (R-NUGET-010): the SemVer 2.0.0 hive
// for a request that asked for SemVer 2.0.0-specific versions, the legacy one otherwise.
const (
	hiveLegacy  = "registration/"
	hiveSemVer2 = "registration-semver2/"
)

type versionRef struct {
	ID        string `json:"@id"`
	Version   string `json:"version"`
	Downloads int64  `json:"downloads"`
}

type packageType struct {
	Name string `json:"name"`
}

// result is one package of the data array. Owners, verified, deprecation and vulnerabilities
// are not members: a local package has none (R-NUGET-010).
type result struct {
	ID             string        `json:"@id"`
	Type           string        `json:"@type"`
	Registration   string        `json:"registration"`
	Name           string        `json:"id"`
	Version        string        `json:"version"`
	Description    string        `json:"description"`
	Summary        string        `json:"summary"`
	Title          string        `json:"title"`
	IconURL        string        `json:"iconUrl"`
	LicenseURL     string        `json:"licenseUrl"`
	ProjectURL     string        `json:"projectUrl"`
	Tags           []string      `json:"tags"`
	Authors        string        `json:"authors"`
	TotalDownloads int64         `json:"totalDownloads"`
	PackageTypes   []packageType `json:"packageTypes"`
	Versions       []versionRef  `json:"versions"`
}

type document struct {
	TotalHits int      `json:"totalHits"`
	Data      []result `json:"data"`
}

// metadata is what a result reads of nuget_version.metadata (R-NUGET-005). Tags are a list
// of words, and a text is read as the words it holds.
type metadata struct {
	Title       string          `json:"title"`
	Summary     string          `json:"summary"`
	Description string          `json:"description"`
	Authors     json.RawMessage `json:"authors"`
	Tags        json.RawMessage `json:"tags"`
	Types       []struct {
		Name string `json:"name"`
	} `json:"packageTypes"`
	IconURL    string `json:"iconUrl"`
	LicenseURL string `json:"licenseUrl"`
	ProjectURL string `json:"projectUrl"`
}

// words reads a member that is a list of strings or one string.
func words(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.Fields(text)
	}
	return nil
}

func (m metadata) tags() []string { return append([]string{}, words(m.Tags)...) }

// authors is the text of the authors member: kept as one string by the ingestion, a list
// is joined the way the .nuspec writes it.
func (m metadata) authors() string {
	var text string
	if json.Unmarshal(m.Authors, &text) == nil {
		return text
	}
	var list []string
	if json.Unmarshal(m.Authors, &list) == nil {
		return strings.Join(list, ", ")
	}
	return ""
}

// typeNames are the package type names with the casing the .nuspec declares; the stored
// lower-case names serve a row whose metadata lists none.
func (m metadata) typeNames(stored []string) []packageType {
	out := make([]packageType, 0, max(len(m.Types), len(stored), 1))
	for _, t := range m.Types {
		out = append(out, packageType{Name: t.Name})
	}
	if len(out) == 0 {
		for _, n := range stored {
			out = append(out, packageType{Name: n})
		}
	}
	if len(out) == 0 {
		out = append(out, packageType{Name: "Dependency"})
	}
	return out
}

// Render renders the search document (R-NUGET-010): totalHits and one object per hit. feed
// is the v3 root of the repository the client addressed, with its trailing slash. A hit
// carries its latest visible version and the listed visible versions, ascending; semVer2
// says which registration hive the links point into. Downloads are 0 until R-STOR-015
// ships; the protocol needs the property, not a value.
func Render(feed string, semVer2 bool, total int, hits []rows.SearchHit) ([]byte, error) {
	hive := feed + hiveLegacy
	if semVer2 {
		hive = feed + hiveSemVer2
	}
	doc := document{TotalHits: total, Data: make([]result, 0, len(hits))}
	for _, h := range hits {
		var md metadata
		if len(h.Latest.Metadata) > 0 {
			if err := json.Unmarshal(h.Latest.Metadata, &md); err != nil {
				return nil, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
			}
		}
		index := hive + h.LowerID + "/index.json"
		r := result{
			ID: index, Type: "Package", Registration: index, Name: h.DisplayName, Version: h.Latest.FullVersion,
			Description: md.Description, Summary: md.Summary, Title: md.Title,
			IconURL: md.IconURL, LicenseURL: md.LicenseURL, ProjectURL: md.ProjectURL,
			Tags: md.tags(), Authors: md.authors(),
			PackageTypes: md.typeNames(h.Latest.PackageTypes),
			Versions:     make([]versionRef, 0, len(h.Listed)),
		}
		for _, v := range h.Listed {
			r.Versions = append(r.Versions, versionRef{ID: hive + h.LowerID + "/" + v.LowerVersion + ".json", Version: v.FullVersion})
		}
		doc.Data = append(doc.Data, r)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	return b, nil
}
