package osvstub

import (
	"embed"
	"encoding/json"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// referenceFS holds the reference data of R-VULN-017 frozen in testdata/reference (see its README.md): the public
// registry's bulk answers and the OSV records behind them, taken on one day.
//
//go:embed testdata/reference
var referenceFS embed.FS

// ReferenceData is what `make osv-reference-refresh` recorded.
type ReferenceData struct {
	// Taken is the day the answers and the records were taken, as in SOURCE.json.
	Taken string
	// PublicRequest is the bulk body of the reference lockfile (perf/npm/reference/package-lock.json) and PublicAnswer the
	// public registry's answer to it.
	PublicRequest, PublicAnswer []byte
	// ControlRequest is the bulk body of real packages with known advisories and ControlAnswer the public answer to it.
	ControlRequest, ControlAnswer []byte
}

// ReferenceAnswers returns the recorded bulk bodies and answers. A missing file is a build error of the repository, so it panics.
func ReferenceAnswers() ReferenceData {
	read := func(name string) []byte {
		b, err := referenceFS.ReadFile(path.Join("testdata/reference", name))
		if err != nil {
			panic("osvstub: " + err.Error())
		}
		return b
	}
	var src struct {
		Taken string `json:"taken"`
	}
	if err := json.Unmarshal(read("SOURCE.json"), &src); err != nil {
		panic("osvstub: SOURCE.json: " + err.Error())
	}
	return ReferenceData{
		Taken:          src.Taken,
		PublicRequest:  read("public-request.json"),
		PublicAnswer:   read("public-bulk.json"),
		ControlRequest: read("control-request.json"),
		ControlAnswer:  read("control-bulk.json"),
	}
}

// ReferenceRecords returns the OSV records of the reference data (GHSA- and MAL-), by id. They are npm records.
func ReferenceRecords() []Record {
	entries, err := fs.ReadDir(referenceFS, "testdata/reference/records")
	if err != nil {
		return nil
	}
	var out []Record
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		body, err := referenceFS.ReadFile(path.Join("testdata/reference/records", e.Name()))
		if err != nil {
			continue
		}
		out = append(out, Record{ID: strings.TrimSuffix(e.Name(), ".json"), Body: body})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func referenceGroups() map[string][]Record {
	return map[string][]Record{"npm": ReferenceRecords()}
}
