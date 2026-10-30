package corpus

import (
	"encoding/json"
	"strings"
)

// AbbreviatedType is the media type of the install document (the abbreviated packument).
const AbbreviatedType = "application/vnd.npm.install-v1+json"

// timeLayout is how npm documents write instants.
const timeLayout = "2006-01-02T15:04:05.000Z"

type distJSON struct {
	Integrity    string `json:"integrity"`
	Shasum       string `json:"shasum"`
	Tarball      string `json:"tarball"`
	FileCount    int    `json:"fileCount"`
	UnpackedSize int    `json:"unpackedSize"`
}

type fullVersionJSON struct {
	ID           string            `json:"_id"`
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Description  string            `json:"description"`
	Main         string            `json:"main"`
	License      string            `json:"license"`
	Dependencies map[string]string `json:"dependencies,omitempty"`
	Deprecated   string            `json:"deprecated,omitempty"`
	Dist         distJSON          `json:"dist"`
}

type abbreviatedVersionJSON struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Dependencies map[string]string `json:"dependencies,omitempty"`
	Deprecated   string            `json:"deprecated,omitempty"`
	Dist         distJSON          `json:"dist"`
}

type fullJSON struct {
	ID          string                     `json:"_id"`
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	DistTags    map[string]string          `json:"dist-tags"`
	Versions    map[string]fullVersionJSON `json:"versions"`
	Time        map[string]string          `json:"time"`
	Readme      string                     `json:"readme"`
	License     string                     `json:"license"`
}

type abbreviatedJSON struct {
	Name     string                            `json:"name"`
	Modified string                            `json:"modified"`
	DistTags map[string]string                 `json:"dist-tags"`
	Versions map[string]abbreviatedVersionJSON `json:"versions"`
}

// TarballURL is the address of a version's tarball below the registry base URL (no trailing slash),
// in the form registries use: "<base>/<name>/-/<unscoped>-<version>.tgz".
func TarballURL(base, name, version string) string {
	return strings.TrimSuffix(base, "/") + "/" + name + "/-/" + TarballFile(name, version)
}

// Document renders the package document for a registry served at base: the full document, or with
// abbreviated the install document. Tarball addresses point below base. The output depends only on
// the package and base, so equal inputs give equal bytes. Maps are written with sorted keys.
func (p *Package) Document(base string, abbreviated bool) []byte {
	var v any
	if abbreviated {
		v = p.abbreviated(base)
	} else {
		v = p.full(base)
	}
	out, err := json.Marshal(v)
	if err != nil {
		panic(err) // strings and integers only
	}
	return out
}

func (p *Package) dist(base string, v Version) distJSON {
	return distJSON{
		Integrity:    v.Dist.Integrity,
		Shasum:       v.Dist.Shasum,
		Tarball:      TarballURL(base, p.Name, v.Version),
		FileCount:    v.Dist.FileCount,
		UnpackedSize: v.Dist.UnpackedSize,
	}
}

func (p *Package) abbreviated(base string) abbreviatedJSON {
	doc := abbreviatedJSON{
		Name:     p.Name,
		Modified: p.Modified.UTC().Format(timeLayout),
		DistTags: p.DistTags,
		Versions: make(map[string]abbreviatedVersionJSON, len(p.Versions)),
	}
	for _, v := range p.Versions {
		doc.Versions[v.Version] = abbreviatedVersionJSON{
			Name: p.Name, Version: v.Version, Dependencies: v.Dependencies, Deprecated: v.Deprecated,
			Dist: p.dist(base, v),
		}
	}
	return doc
}

func (p *Package) full(base string) fullJSON {
	doc := fullJSON{
		ID:          p.Name,
		Name:        p.Name,
		Description: p.Description,
		DistTags:    p.DistTags,
		Versions:    make(map[string]fullVersionJSON, len(p.Versions)),
		Time: map[string]string{
			"created":  p.Created.UTC().Format(timeLayout),
			"modified": p.Modified.UTC().Format(timeLayout),
		},
		Readme:  "# " + p.Name + "\n\n" + p.Description + "\n",
		License: "MIT",
	}
	for _, v := range p.Versions {
		doc.Versions[v.Version] = fullVersionJSON{
			ID: p.Name + "@" + v.Version, Name: p.Name, Version: v.Version, Description: p.Description,
			Main: "index.js", License: "MIT", Dependencies: v.Dependencies, Deprecated: v.Deprecated,
			Dist: p.dist(base, v),
		}
		doc.Time[v.Version] = v.Time.UTC().Format(timeLayout)
	}
	return doc
}
