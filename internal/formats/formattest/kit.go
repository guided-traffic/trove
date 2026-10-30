package formattest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest/crafted"
)

// Check is one of the ten checks of R-ARCH-010, numbered as the spec numbers them.
type Check int

// The checks.
const (
	CheckAmbiguity Check = iota + 1
	CheckTraversal
	CheckClassify
	CheckRewrite
	CheckMerge
	CheckInspect
	CheckHeaders
	CheckSetup
	CheckRevision
	CheckNames

	lastCheck = CheckNames
)

// name is the short name of the check.
func (c Check) name() string {
	switch c {
	case CheckAmbiguity:
		return "route ambiguity"
	case CheckTraversal:
		return "path traversal"
	case CheckClassify:
		return "classification"
	case CheckRewrite:
		return "rewrite"
	case CheckMerge:
		return "merge"
	case CheckInspect:
		return "inspect limits"
	case CheckHeaders:
		return "content headers"
	case CheckSetup:
		return "setup templates"
	case CheckRevision:
		return "revision"
	case CheckNames:
		return "package names"
	}
	return "unknown"
}

// String is the name of the subtest: "R-ARCH-010 check 7 content headers".
func (c Check) String() string {
	return fmt.Sprintf("R-ARCH-010 check %d %s", int(c), c.name())
}

// Fixture is what a format declares for the kit: the cases the checks run over.
// The kit derives everything else from the format itself (its routes, their
// examples, its descriptor). A check with nothing to run over and no entry in
// Skip fails, so a check cannot pass because the fixture is empty.
type Fixture struct {
	// Host returns the options of a fresh in-memory Host; the kit calls it once per
	// case, so no case sees another's rows. nil: the zero HostOptions. A format
	// with a data port returns the in-memory twin of the port in Data.
	Host func() HostOptions

	// Rejects are further escaped paths Match must refuse (check 2). The kit
	// mutates every route example with traversal, NUL and control characters on its
	// own.
	Rejects []string
	// Paths are further paths and what Classify must say of them (check 3).
	Paths []PathCase
	// Remote is the fixture of check 4: documents Rewrite renders.
	Remote *RemoteFixture
	// Merge is the fixture of check 5: member documents Merge combines.
	Merge *MergeFixture
	// Inspect are the crafted uploads of check 6. [InspectTarGz], [InspectZip],
	// [InspectJSON] and [InspectXML] build the standard cases of the four archive
	// and document types.
	Inspect []InspectCase
	// Content are the requests of check 7: one per Download, Metadata, Computed and
	// Volatile route at least.
	Content []ContentCase
	// Changes are the writes of check 9: every storage hook and every Upload,
	// Delete or Admin route that changes package rows.
	Changes []ChangeCase
	// NoRevision lists routes of kind Upload, Delete or Admin that change no
	// package row, with the reason; check 9 asks no change case of them.
	NoRevision map[string]string
	// Names are the sample package names of check 10.
	Names []NameCase
	// OptionNames are literal client option names a setup template may spell
	// although they contain "token", "password" or "_auth" (check 8): the option's
	// name, never a value.
	OptionNames []string
	// Skip lists the checks that do not apply to the format, each with the reason.
	// A skip without a reason fails the check, and so does a skip the format
	// contradicts (a skipped check 1 on a format that has routes).
	Skip map[Check]string
}

// PathCase is a path and what Classify says of it.
type PathCase struct {
	// Path is escaped, as a client sends it.
	Path string
	Want formats.PathInfo
}

// RemoteFixture is the fixture of check 4.
type RemoteFixture struct {
	// Upstream is the origin of the remote repository's upstream:
	// "https://registry.example.org".
	Upstream string
	// Settings is the format's member of the repository document, or nil.
	Settings json.RawMessage
	// Docs are upstream documents as the upstream serves them: at least one holds a
	// download link to the upstream.
	Docs []RemoteDoc
	// DownloadLinks returns the download URLs of a document, rewritten or not.
	DownloadLinks func(doc []byte) []string
}

// RemoteDoc is one upstream document.
type RemoteDoc struct {
	Name string
	// Path is the escaped repository path the document is served at.
	Path    string
	Variant string
	Body    []byte
}

// MergeFixture is the fixture of check 5.
type MergeFixture struct {
	Cases []MergeCase
	// Versions returns the versions a member document or a merged document holds.
	Versions func(doc []byte) []string
	// VersionSource returns which member a version entry of a merged document
	// came from. The fixture marks the entries of each member document so that this
	// can be read back; "" for a version the document does not hold.
	VersionSource func(merged []byte, version string) string
}

// MergeCase is one merge: the member documents in the core's order (R-REPO-019
// step 5) and, for each, the marker VersionSource returns for entries that came
// from it.
type MergeCase struct {
	Name string
	// Path is the escaped repository path of the merged document.
	Path    string
	Parts   []formats.MemberDocument
	Sources []string
}

// InspectCase is one crafted upload for Inspect (check 6).
type InspectCase struct {
	Name string
	// Path is the repository path the upload would be committed at; MediaType its
	// declared type.
	Path      string
	MediaType string
	Input     crafted.Input
}

// ContentCase is one request of check 7.
type ContentCase struct {
	Name string
	// Method defaults to GET; Path is escaped.
	Method string
	Path   string
	Body   []byte
	// Status is the expected status; 200 when 0.
	Status int
	// Seed stores the rows, items and remote documents the request reads.
	Seed func(h *Host) error
}

// ChangeCase is one change of check 9: after Do, the revision of Package is higher
// than before it.
type ChangeCase struct {
	Name string
	// Package is the normalized name of the package the change touches.
	Package string
	// Seed stores the state the change starts from; may be nil.
	Seed func(h *Host) error
	// Do makes the change: a hook through h.Ingest().Commit, h.Remove or h.Restore,
	// or a request through h.Serve.
	Do func(ctx context.Context, h *Host) error
}

// NameCase is one package name in its spellings (check 10).
type NameCase struct {
	// Spellings are different spellings of one package name: they must normalize to
	// one name and lead to one document path.
	Spellings []string
}

// Result is the outcome of one check.
type Result struct {
	Check Check
	// Skipped is the reason when the format's fixture skips the check.
	Skipped string
	// Failures are the violations found, each one sentence.
	Failures []string
}

// Passed reports whether the check ran and found nothing.
func (r Result) Passed() bool { return r.Skipped == "" && len(r.Failures) == 0 }

// Report holds the result of every check, indexed by check number minus one.
type Report []Result

// Result returns the result of a check.
func (r Report) Result(c Check) Result { return r[c-1] }

// Failed returns the checks with at least one failure.
func (r Report) Failed() []Check {
	var out []Check
	for _, res := range r {
		if len(res.Failures) > 0 {
			out = append(out, res.Check)
		}
	}
	return out
}

// Evaluate runs the ten checks over a format and returns what they found. It is
// what Run reports through a test; the kit's own tests use it to assert that a
// defective format fails the check the spec names.
func Evaluate(f formats.Format, fx Fixture) Report {
	out := make(Report, 0, lastCheck)
	for c := CheckAmbiguity; c <= lastCheck; c++ {
		out = append(out, runCheck(c, f, fx))
	}
	return out
}

// Run runs the conformance kit as subtests of t: one per check, named
// "R-ARCH-010 check N <name>". A format calls it from its own test:
//
//	func TestR_ARCH_010_Conformance(t *testing.T) { formattest.Run(t, npm.Format{}, npmFixture()) }
func Run(t *testing.T, f formats.Format, fx Fixture) {
	t.Helper()
	for c := CheckAmbiguity; c <= lastCheck; c++ {
		t.Run(c.String(), func(t *testing.T) {
			res := runCheck(c, f, fx)
			for _, m := range res.Failures {
				t.Error(m)
			}
			if res.Skipped != "" {
				t.Skip(res.Skipped)
			}
		})
	}
}

// runCheck runs one check, or checks its skip.
func runCheck(c Check, f formats.Format, fx Fixture) (res Result) {
	res.Check = c
	r := &run{f: f, fx: fx, d: f.Descriptor(), res: &res}
	defer func() {
		if p := recover(); p != nil {
			res.Failures = append(res.Failures, fmt.Sprintf("the format panicked: %v", p))
		}
	}()
	if reason, skipped := fx.Skip[c]; skipped {
		switch msg := r.skipProblem(c, reason); {
		case msg != "":
			r.failf("%s", msg)
		default:
			res.Skipped = reason
		}
		return res
	}
	switch c {
	case CheckAmbiguity:
		r.ambiguity()
	case CheckTraversal:
		r.traversal()
	case CheckClassify:
		r.classify()
	case CheckRewrite:
		r.rewrite()
	case CheckMerge:
		r.merge()
	case CheckInspect:
		r.inspect()
	case CheckHeaders:
		r.headers()
	case CheckSetup:
		r.setup()
	case CheckRevision:
		r.revision()
	case CheckNames:
		r.names()
	}
	return res
}

// run is the state of one check.
type run struct {
	f   formats.Format
	fx  Fixture
	d   formats.Descriptor
	res *Result
}

func (r *run) failf(format string, a ...any) {
	r.res.Failures = append(r.res.Failures, fmt.Sprintf(format, a...))
}

func (r *run) host(class formats.Class) *Host {
	var o HostOptions
	if r.fx.Host != nil {
		o = r.fx.Host()
	}
	if class != "" {
		o.Class = class
	}
	return NewHost(r.f, o)
}

// skipProblem says why a skip is not acceptable: no reason, or a format the skip
// contradicts. A skip is how a format with no routes, no clients or no remote
// class passes; it must not let a format that gained them pass vacuously.
func (r *run) skipProblem(c Check, reason string) string {
	if reason == "" {
		return fmt.Sprintf("%s is skipped without a reason", c)
	}
	routes := len(r.f.Routes())
	switch {
	case c == CheckTraversal:
		return fmt.Sprintf("%s always applies, to a format without routes as well: Match must refuse every traversal input", c)
	case (c == CheckAmbiguity || c == CheckHeaders || c == CheckClassify) && routes > 0:
		return fmt.Sprintf("%s is skipped (%s) and the format has %d routes", c, reason, routes)
	case c == CheckRewrite && r.f.Remote() != nil:
		return fmt.Sprintf("%s is skipped (%s) and the format has a remote adapter", c, reason)
	case c == CheckMerge && r.f.Virtual() != nil:
		return fmt.Sprintf("%s is skipped (%s) and the format has a virtual adapter", c, reason)
	case c == CheckSetup && (len(r.d.Clients) > 0 || len(r.f.SetupTemplates()) > 0):
		return fmt.Sprintf("%s is skipped (%s) and the format has clients or setup templates", c, reason)
	case c == CheckNames && r.d.PackageNames:
		return fmt.Sprintf("%s is skipped (%s) and the format has package names", c, reason)
	}
	return ""
}

// inputSpool serves a crafted input as the spool of an upload.
type inputSpool struct{ in crafted.Input }

func (s inputSpool) Digests() formats.Digests { return formats.Digests{} }
func (s inputSpool) Size() int64              { return s.in.Size }
func (s inputSpool) Open() (io.ReadCloser, error) {
	return s.in.Open(), nil
}

func (s inputSpool) ReaderAt() (io.ReaderAt, error) {
	if s.in.ReaderAt == nil {
		return nil, fmt.Errorf("the crafted input %q has no random access", s.in.Name)
	}
	return s.in.ReaderAt, nil
}

var _ formats.Spooled = inputSpool{}

// SpoolOf serves a crafted input as the spool of an upload, for a format's own
// tests of Inspect.
func SpoolOf(in crafted.Input) formats.Spooled { return inputSpool{in} }

// Standard crafted cases for check 6. path is the repository path the format
// accepts an upload at; entry is the path of the manifest inside the archive.

// InspectTarGz are the cases of a tar.gz upload: a 2 GiB manifest entry and more
// entries than the limit allows.
func InspectTarGz(path, manifestEntry string) []InspectCase {
	return []InspectCase{
		{Name: "tar.gz manifest of 2 GiB", Path: path, MediaType: "application/gzip", Input: crafted.TarGzEntry(manifestEntry, 2<<30)},
		{Name: "tar.gz with 100,000 entries", Path: path, MediaType: "application/gzip", Input: crafted.TarGzEntries(100_000)},
	}
}

// InspectZip are the cases of a zip upload: an entry that expands to 10 GiB and
// more entries than the limit allows.
func InspectZip(path, manifestEntry string) []InspectCase {
	return []InspectCase{
		{Name: "zip expanding to 10 GiB", Path: path, MediaType: "application/zip", Input: crafted.ZipBomb(manifestEntry, 10<<30)},
		{Name: "zip with 100,000 entries", Path: path, MediaType: "application/zip", Input: crafted.ZipEntries(100_000)},
	}
}

// InspectJSON are the cases of a JSON upload: a document nested 100,000 deep and
// one with millions of values.
func InspectJSON(path string) []InspectCase {
	return []InspectCase{
		{Name: "JSON nested 100,000 deep", Path: path, MediaType: "application/json", Input: crafted.DeepJSON(100_000)},
		{Name: "JSON with 10 million values", Path: path, MediaType: "application/json", Input: crafted.WideJSON(10_000_000)},
	}
}

// InspectXML are the cases of an XML upload: a document type declaration and a
// document nested 100,000 deep.
func InspectXML(path string) []InspectCase {
	return []InspectCase{
		{Name: "XML with a document type declaration", Path: path, MediaType: "application/xml", Input: crafted.XMLDoctype()},
		{Name: "XML nested 100,000 deep", Path: path, MediaType: "application/xml", Input: crafted.DeepXML(100_000)},
	}
}
