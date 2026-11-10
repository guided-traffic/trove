package importer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// Journal is the state file of an import (R-IMP-007): one JSON document per line, a header that names the import and one line
// per package that is complete. A line is written whole and flushed to disk before the next package starts, so a run that is
// killed leaves a file that holds every package it reported as done, and at most one cut line at its end, which is ignored.
// The file holds names, versions and outcomes. It holds no credential and no address with a secret in it.
type Journal struct {
	mu   sync.Mutex
	f    *os.File
	done map[string]PackageResult
}

type journalLine struct {
	Kind    string         `json:"kind"` // "header" or "package"
	Format  string         `json:"format,omitempty"`
	Source  string         `json:"source,omitempty"`
	Target  string         `json:"target,omitempty"`
	Package *PackageResult `json:"package,omitempty"`
}

// OpenJournal opens the state file at path, creating it when it does not exist. A file that belongs to another import (format,
// source or target differ) is refused, so a state file cannot hide packages of one import from another.
func OpenJournal(path, format, source, target string) (*Journal, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // G304: the path is the operator's flag
	if err != nil {
		return nil, err
	}
	j := &Journal{f: f, done: map[string]PackageResult{}}
	data, err := io.ReadAll(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	header := journalLine{Kind: "header", Format: format, Source: redact(source), Target: redact(target)}
	if len(bytes.TrimSpace(data)) == 0 {
		if err := j.write(header); err != nil {
			_ = f.Close()
			return nil, err
		}
		return j, nil
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	first := true
	for sc.Scan() {
		var l journalLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			if first {
				_ = f.Close()
				return nil, fmt.Errorf("%s is not a state file of an import", path)
			}
			continue // the cut last line of a killed run
		}
		if first {
			if l.Kind != "header" || l.Format != header.Format || l.Source != header.Source || l.Target != header.Target {
				_ = f.Close()
				return nil, fmt.Errorf("%s belongs to another import (%s from %s into %s)", path, l.Format, l.Source, l.Target)
			}
			first = false
			continue
		}
		if l.Kind == "package" && l.Package != nil {
			j.done[l.Package.Name] = *l.Package
		}
	}
	if first {
		_ = f.Close()
		return nil, errors.New(path + " is not a state file of an import")
	}
	// Append after a cut last line on a line of its own.
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		_ = f.Close()
		return nil, err
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		if _, err := f.WriteString("\n"); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return j, nil
}

func (j *Journal) write(l journalLine) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if _, err := j.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return j.f.Sync()
}

// Done returns the complete package of an earlier run. A nil journal holds none.
func (j *Journal) Done(name string) (PackageResult, bool) {
	if j == nil {
		return PackageResult{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	p, ok := j.done[name]
	return p, ok
}

// Record appends a complete package.
func (j *Journal) Record(p PackageResult) error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.done[p.Name] = p
	return j.write(journalLine{Kind: "package", Package: &p})
}

// Close closes the file.
func (j *Journal) Close() error {
	if j == nil {
		return nil
	}
	return j.f.Close()
}
