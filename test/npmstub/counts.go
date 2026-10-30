package npmstub

import (
	"fmt"
	"sort"
	"strings"
)

// Kinds of request the stub tells apart.
const (
	KindDocument = "document"
	KindTarball  = "tarball"
	KindPing     = "ping"
	KindOther    = "other"
)

// Entry is the number of requests that had the same method, path, conditionality and credential
// scheme. The path is the escaped path as the client sent it, so a scoped name written with %2f
// and one written with a slash are different entries; Name is the same for both.
type Entry struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Conditional bool   `json:"conditional"`
	Auth        string `json:"auth"` // none, bearer, basic or other
	Kind        string `json:"kind"`
	Name        string `json:"name,omitempty"`
	File        string `json:"file,omitempty"`
	Accept      string `json:"accept,omitempty"` // the Accept header of the last request of the entry
	Count       int    `json:"count"`
}

// Counts is a snapshot of the counters. Control requests are not counted.
type Counts struct {
	Entries []Entry `json:"entries"`
}

// Total is the number of requests.
func (c Counts) Total() int {
	n := 0
	for _, e := range c.Entries {
		n += e.Count
	}
	return n
}

// Documents is the number of GET and HEAD requests for the document of a package, however the name
// was written and whether conditional or not.
func (c Counts) Documents(name string) int { return c.count(KindDocument, name, "") }

// ConditionalDocuments is the number of those that carried If-None-Match or If-Modified-Since.
func (c Counts) ConditionalDocuments(name string) int {
	n := 0
	for _, e := range c.Entries {
		if e.Kind == KindDocument && e.Name == name && e.Conditional {
			n += e.Count
		}
	}
	return n
}

// Tarballs is the number of requests for the tarballs of a package, of every version.
func (c Counts) Tarballs(name string) int { return c.count(KindTarball, name, "") }

// Tarball is the number of requests for one tarball file of a package.
func (c Counts) Tarball(name, file string) int { return c.count(KindTarball, name, file) }

// Requests is the number of requests with a method for a path as the client wrote it.
func (c Counts) Requests(method, path string) int {
	n := 0
	for _, e := range c.Entries {
		if e.Method == method && e.Path == path {
			n += e.Count
		}
	}
	return n
}

func (c Counts) count(kind, name, file string) int {
	n := 0
	for _, e := range c.Entries {
		if e.Kind == kind && e.Name == name && (file == "" || e.File == file) {
			n += e.Count
		}
	}
	return n
}

// Violations lists what breaks "at most maxDocuments requests per package document and at most
// maxTarballs per tarball file" (the criterion E2.2 states with 1 and 1), one line each, sorted.
func (c Counts) Violations(maxDocuments, maxTarballs int) []string {
	docs := map[string]int{}
	tarballs := map[string]int{}
	for _, e := range c.Entries {
		switch e.Kind {
		case KindDocument:
			docs[e.Name] += e.Count
		case KindTarball:
			tarballs[e.Name+"/"+e.File] += e.Count
		}
	}
	var out []string
	for name, n := range docs {
		if n > maxDocuments {
			out = append(out, fmt.Sprintf("document %s requested %d times, at most %d", name, n, maxDocuments))
		}
	}
	for file, n := range tarballs {
		if n > maxTarballs {
			out = append(out, fmt.Sprintf("tarball %s requested %d times, at most %d", file, n, maxTarballs))
		}
	}
	sort.Strings(out)
	return out
}

// Summary is a one-line description for test failures.
func (c Counts) Summary() string {
	docs, tarballs := 0, 0
	for _, e := range c.Entries {
		switch e.Kind {
		case KindDocument:
			docs += e.Count
		case KindTarball:
			tarballs += e.Count
		}
	}
	return fmt.Sprintf("%d requests: %d documents, %d tarballs, %d other", c.Total(), docs, tarballs, c.Total()-docs-tarballs)
}

type countKey struct {
	method, path, auth string
	conditional        bool
}

// authScheme classifies an Authorization header without keeping the credential.
func authScheme(header string) string {
	scheme, _, _ := strings.Cut(header, " ")
	switch strings.ToLower(scheme) {
	case "":
		return "none"
	case "bearer":
		return "bearer"
	case "basic":
		return "basic"
	}
	return "other"
}
