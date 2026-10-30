package view

import (
	"context"
	"time"
)

// Adapter is the format's part of the view (formats.ViewAdapter), in the terms of
// this package: a version is named by its row id and its repository key.
type Adapter interface {
	// Compare orders two normalized versions by the format's precedence.
	Compare(a, b string) int
	// Prerelease reports whether a normalized version is a pre-release.
	Prerelease(version string) bool
	// Describe reads what the screens show of versions the caller may read. visible
	// says which versions of the package the caller may read.
	Describe(ctx context.Context, vs []Version, want Fields, visible func(version string) bool) (map[int64]Data, error)
	// Find returns the ids of at most limit+1 versions whose description or keywords
	// match; the view intersects them with what the caller may read.
	Find(ctx context.Context, f Find, limit int) ([]int64, error)
	// Withheld returns the ids of stored versions the format does not serve.
	Withheld(ctx context.Context, repos []string) ([]int64, error)
}

// Adapters finds the adapter of a format; ok is false for a format without
// package screens.
type Adapters interface {
	For(format string) (Adapter, bool)
	// Formats lists the formats that have an adapter, sorted.
	Formats() []string
}

// Version names a stored version for the adapter.
type Version struct {
	ID      int64
	Format  string
	Repo    string
	Package string
	Version string
}

// Fields says how much of a version the adapter reads.
type Fields uint8

// The fields, as in formats.ViewFields.
const (
	FieldSummary Fields = 1 << iota
	FieldDetails
	FieldReadme
)

// Data is what the adapter knows of a version.
type Data struct {
	Description string
	Keywords    []string
	Licenses    []string
	HasReadme   bool
	Metadata    map[string]any
	Readme      string
	ReadmeFile  string
}

// Find is a search the adapter answers: one of Text and Keyword is set.
type Find struct {
	Repos   []string
	Text    string
	Keyword string
}

// Package is a package as the caller sees it: every member is computed from the
// versions the caller may read.
type Package struct {
	ID     string
	Format string
	// Name is the display name; Key the normalized name the list statement sorts on and the
	// cursor carries (R-API-029).
	Name          string
	Key           string
	Description   *string
	LatestVersion string
	Keywords      []string
	Repositories  []string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	VersionCount  int64
	DownloadCount int64
}

// VersionSummary is one entry of the version list.
type VersionSummary struct {
	Version       string
	Prerelease    bool
	PublishedAt   time.Time
	SizeBytes     int64
	Repositories  []string
	DownloadCount int64
}

// RepositoryEntry is a repository that holds a version.
type RepositoryEntry struct {
	Key         string
	Class       string
	PublishedAt time.Time
}

// File is a file of a version.
type File struct {
	Path      string
	SizeBytes int64
	SHA256    string
	// Repo is the repository whose copy the file is.
	Repo string
}

// VersionDetail is a version with everything the details page shows.
type VersionDetail struct {
	PackageID     string
	Format        string
	Name          string
	Version       string
	Prerelease    bool
	PublishedAt   time.Time
	Licenses      []string
	SizeBytes     int64
	DownloadCount int64
	HasReadme     bool
	Metadata      map[string]any
	Repositories  []RepositoryEntry
	Files         []File
	// Repo is the repository the files and the metadata were read from: the one
	// that stored the version first.
	Repo string
}

// Readme is the README source of a version.
type Readme struct {
	Source   string
	FileName string
}
