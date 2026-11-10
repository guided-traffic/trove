package importer

import (
	"context"
	"os"
	"strconv"
	"time"
)

// Outcomes of one version (R-IMP-006).
const (
	OutcomeImported = "imported"
	OutcomeSkipped  = "skipped"      // the target holds the identical content
	OutcomeConflict = "conflict"     // the target holds the version with other content; nothing was written
	OutcomeFailed   = "failed"       // the version was refused or could not be read or written; Code says why
	OutcomeWouldAdd = "would_import" // dry run
)

// Codes of a failure or a note, the stable words of the report (R-IMP-006).
const (
	CodeDigestMismatch   = "digest_mismatch"   // the bytes are not the ones the source announced
	CodeNoDigest         = "no_digest"         // the source announced no digest to verify against
	CodeForeignHost      = "foreign_host"      // the download address is not the source's origin or an allowed extra host
	CodeIdentityMismatch = "identity_mismatch" // the package says it is another name or version than the source listed
	CodeTooLarge         = "too_large"         // over the limit of the importer
	CodeSourceError      = "source_error"      // the source did not answer as a registry does
	CodeTargetRefused    = "target_refused"    // the target refused the publish; Detail has its status and code
	CodeForbidden        = "forbidden"         // the token may not publish this name
	CodeInvalidName      = "invalid_name"      // the name is not one the importer puts into an address
	CodeStateNotSet      = "state_not_set"     // a dist-tag or the listing could not be set
)

// Digest is the strongest digest the source announced for a file.
type Digest struct {
	Alg string // sha512, sha384, sha256 or sha1
	Sum []byte
}

// Version is one version of a package as the source lists it.
type Version struct {
	Version    string
	Published  time.Time
	Listed     bool   // NuGet: false for an unlisted version
	Deprecated string // npm: the deprecation message of the source
	URL        string // where the file is
	Want       Digest // the strongest digest the source announced; zero when it announced none
	Meta       []byte // format-private: the source's version document
}

// Plan is what the source holds of one package, in the order the versions are imported.
type Plan struct {
	Name     string
	Versions []Version
	Tags     map[string]string // npm dist-tags
	NotKept  []string          // states of the source that the target cannot hold (R-IMP-005)
}

// Held is what the target holds of one version.
type Held struct {
	Digests map[string][]byte // algorithm -> digest of the stored file
	Listed  bool
}

// Sums are the digests of a downloaded file by algorithm.
type Sums map[string][]byte

// File is a downloaded and verified file in the importer's spool.
type File struct {
	*os.File
	Size int64
	Sums Sums
}

// Remove closes and deletes the spool file.
func (f *File) Remove() {
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}

// Format is the one seam a package format adds (R-IMP-009).
type Format interface {
	// ID is the format's name on the command line.
	ID() string
	// ValidName reports whether a name may be put into an address.
	ValidName(name string) bool
	// Preflight checks that the target is a repository of this format that the token can use, before any package is read.
	Preflight(ctx context.Context, dst *TargetClient) error
	// Search lists the names the source's search finds for q (R-IMP-003).
	Search(ctx context.Context, src *SourceClient, q string) ([]string, error)
	// Plan reads one package of the source. A package the source does not hold has no versions.
	Plan(ctx context.Context, src *SourceClient, name string) (*Plan, error)
	// Held reads what the target holds of the package and its dist-tags; an unknown package holds nothing.
	Held(ctx context.Context, dst *TargetClient, name string) (versions map[string]Held, tags map[string]string, err error)
	// Publish sends one verified file through the target's publish protocol and reports the target's answer as an *Refusal
	// when it refused.
	Publish(ctx context.Context, dst *TargetClient, p *Plan, v Version, f *File, tag string) error
	// Finish applies what a publish cannot carry (dist-tags, listing) once the versions are there. held is what the
	// target holds of the package after the pass. It returns notes for the report.
	Finish(ctx context.Context, dst *TargetClient, p *Plan, held map[string]Held, tags map[string]string) ([]string, error)
}

// Refusal is the target's refusal of a request.
type Refusal struct {
	Status int
	Code   string
	Msg    string
}

func (r *Refusal) Error() string {
	if r.Code != "" {
		return "the target answered " + strconv.Itoa(r.Status) + " " + r.Code
	}
	return "the target answered " + strconv.Itoa(r.Status)
}
