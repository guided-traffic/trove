package deb

import (
	"context"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

// ViewStore is the part of the data port that the package screens read (R-DEB-030). Built by the work package of the local class.
type ViewStore interface {
	// Describe reads, for the versions with the given ids (of any number of packages), what a package screen shows: the
	// architectures, the coordinates, the description and the control fields of R-DEB-030. A version that is not a stored package
	// file of a local repository (or a parsed row of a remote's cached file) is absent.
	Describe(ctx context.Context, versionIDs []int64) (map[int64]rows.VersionView, error)
	// Stored lists the versions of a package a repository holds, newest first by the order of the caller's choosing, at most limit.
	Stored(ctx context.Context, repoID int64, pkg string, limit int) ([]string, error)
	// Find returns the ids of at most limit versions of the repositories (by "<project>/<repository>") whose control data contains the
	// text, compared without case (R-DEB-030). Debian versions have no keywords.
	Find(ctx context.Context, repos []string, text string, limit int) ([]int64, error)
	// Withheld returns the ids of the live versions of the repositories (by "<project>/<repository>") all of whose live files the
	// journal replay withheld (R-DEB-032).
	Withheld(ctx context.Context, repos []string) ([]int64, error)
}
