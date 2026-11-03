package osvstub

import (
	"embed"
	"encoding/json"
	"io/fs"
	"path"
	"sort"
)

// realFS holds the real OSV records frozen in testdata/real (see its README.md): 13 npm and 9 NuGet records of the
// GitHub Advisory Database and the OpenSSF malicious-packages project.
//
//go:embed testdata/real
var realFS embed.FS

// RealRecord is one frozen real record.
type RealRecord struct {
	Ecosystem string
	ID        string
	// Body is the record as taken (indented JSON).
	Body []byte
}

// Real returns the frozen real records, by ecosystem and id.
func Real() []RealRecord {
	var out []RealRecord
	for _, eco := range []string{"NuGet", "npm"} {
		entries, err := fs.ReadDir(realFS, path.Join("testdata/real", eco))
		if err != nil {
			continue
		}
		for _, e := range entries {
			body, err := realFS.ReadFile(path.Join("testdata/real", eco, e.Name()))
			if err != nil {
				continue
			}
			var probe struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(body, &probe) != nil || probe.ID == "" {
				continue
			}
			out = append(out, RealRecord{Ecosystem: eco, ID: probe.ID, Body: body})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ecosystem != out[j].Ecosystem {
			return out[i].Ecosystem < out[j].Ecosystem
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func realGroups() map[string][]Record {
	g := map[string][]Record{}
	for _, r := range Real() {
		g[r.Ecosystem] = append(g[r.Ecosystem], Record{ID: r.ID, Body: r.Body})
	}
	return g
}
