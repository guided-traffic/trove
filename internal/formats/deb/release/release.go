// Package release reads the text of a Release document of an APT archive (Debian Repository Format, R-DEB-019, R-DEB-021): the Date,
// the Valid-Until, the names of the suite and the checksum section that lists the files of the release directory. It is a leaf, like
// the deb822 reader it is built on, so that the remote's format, its jobs and the builds that merge remote members read one document
// one way.
//
// A Release is untrusted input (R-DEB-034): one paragraph of at most 16 MiB, whose checksum lines must all parse. The signature is
// not this package's business; the caller verifies it (internal/openpgp) before it believes a byte of what is read here.
package release

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/formats/deb/control"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/openpgp"
)

// MaxBytes bounds a release document (R-DEB-019, R-DEB-034).
const MaxBytes = 16 << 20

// MaxFiles bounds the files a release lists; the Ubuntu archive lists a few thousand.
const MaxFiles = 200_000

// Release is a parsed Release document.
type Release struct {
	// Date is the Date field; a document without a Date that parses is refused.
	Date time.Time
	// ValidUntil is the Valid-Until field, the zero time when there is none.
	ValidUntil time.Time
	Suite      string
	Codename   string
	// ByHash is Acquire-By-Hash: the archive serves every index file by its hash as well.
	ByHash bool
	// Files are the files of the SHA256 section, in the order they are listed.
	Files []rows.ReleaseFile
	// SHA512 holds the digest of each file of the SHA512 section, as lower-case hex, by path; empty when the release has none. APT
	// takes the strongest hash a release lists, so a client of a release with a SHA512 section asks for by-hash/SHA512/<digest>.
	SHA512 map[string]string
}

// Error is a document that is not a Release the remote accepts; Reason is a sentence for the answer's detail.
type Error struct{ Reason string }

func (e *Error) Error() string { return "release: " + e.Reason }

func refuse(format string, args ...any) error { return &Error{Reason: fmt.Sprintf(format, args...)} }

// Parse reads the text of a Release (the signed text of an InRelease, or a Release file).
func Parse(text []byte) (*Release, error) {
	if len(text) > MaxBytes {
		return nil, refuse("the document is longer than 16 MiB")
	}
	p, err := control.Parse(text)
	if err != nil {
		if e, ok := err.(*control.Error); ok { //nolint:errorlint // the parser returns *Error itself
			return nil, refuse("%s", e.Error())
		}
		return nil, err
	}
	r := &Release{Suite: p.Value("Suite"), Codename: p.Value("Codename")}
	date := strings.TrimSpace(p.Value("Date"))
	if date == "" {
		return nil, refuse("the document has no Date")
	}
	if r.Date, err = ParseTime(date); err != nil {
		return nil, refuse("the Date %q is not a date", shorten(date))
	}
	if until := strings.TrimSpace(p.Value("Valid-Until")); until != "" {
		if r.ValidUntil, err = ParseTime(until); err != nil {
			return nil, refuse("the Valid-Until %q is not a date", shorten(until))
		}
	}
	r.ByHash = strings.EqualFold(strings.TrimSpace(p.Value("Acquire-By-Hash")), "yes")
	section, ok := p.Get("SHA256")
	if !ok {
		return nil, refuse("the document has no SHA256 section")
	}
	if r.Files, err = ParseSection(section); err != nil {
		return nil, err
	}
	if v, ok := p.Get("SHA512"); ok {
		if r.SHA512, err = parseSection512(v); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// ParseSection reads the lines of a checksum field: " <hex digest> <size> <path>" after the first (empty) line of the value.
func ParseSection(value string) ([]rows.ReleaseFile, error) {
	var out []rows.ReleaseFile
	for line := range strings.SplitSeq(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(out) >= MaxFiles {
			return nil, refuse("the checksum section lists more than %d files", MaxFiles)
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, refuse("a checksum line does not have the form digest, size, path")
		}
		sum, err := hex.DecodeString(fields[0])
		if err != nil || len(sum) != 32 {
			return nil, refuse("a checksum line holds a digest that is no SHA-256")
		}
		size, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || size < 0 {
			return nil, refuse("a checksum line holds a size that is no number")
		}
		if !validPath(fields[2]) {
			return nil, refuse("a checksum line names the path %q, which is no path of the release directory", shorten(fields[2]))
		}
		f := rows.ReleaseFile{Path: fields[2], Size: size}
		copy(f.SHA256[:], sum)
		out = append(out, f)
	}
	return out, nil
}

// parseSection512 reads the lines of the SHA512 section: " <128 hex digits> <size> <path>".
func parseSection512(value string) (map[string]string, error) {
	out := map[string]string{}
	for line := range strings.SplitSeq(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 || len(f[0]) != 128 {
			return nil, refuse("a SHA512 line does not have the form digest, size, path")
		}
		if sum, err := hex.DecodeString(f[0]); err != nil || len(sum) != 64 || !validPath(f[2]) {
			return nil, refuse("a SHA512 line holds a digest or a path that is none")
		}
		out[f[2]] = strings.ToLower(f[0])
	}
	return out, nil
}

// validPath admits a relative path without an empty, "." or ".." segment: the release lists files below its own directory.
func validPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || len(p) > 1024 {
		return false
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// ParseTime reads the date of a Release: RFC 2822 as the archives write it, with a zone name or a numeric zone, and a day with or
// without a leading zero.
func ParseTime(s string) (time.Time, error) {
	var last error
	for _, layout := range timeLayouts() {
		t, err := time.Parse(layout, s)
		if err == nil {
			return t.UTC(), nil
		}
		last = err
	}
	return time.Time{}, last
}

func timeLayouts() []string {
	return []string{
		"Mon, 02 Jan 2006 15:04:05 MST", "Mon, 2 Jan 2006 15:04:05 MST",
		"Mon, 02 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 -0700",
	}
}

// Format writes a time the way the archives and the counting stub write a Date.
func Format(t time.Time) string { return t.UTC().Format("Mon, 02 Jan 2006 15:04:05 MST") }

// Signed reads and checks the InRelease of a remote. With certs the document must carry a signature that verifies (R-DEB-020): the
// error is then an *openpgp.VerifyError, and signer is the fingerprint of the certificate that verified. Without certs only the
// structure is checked (openpgp.ClearText) and signer is empty. The signed text is parsed as a Release; a text that is none is a
// *Error.
func Signed(doc []byte, certs []openpgp.Certificate, now time.Time) (r *Release, signer string, err error) {
	var text []byte
	if len(certs) > 0 {
		text, signer, err = openpgp.VerifyClear(doc, certs, now, openpgp.Limits{})
	} else {
		text, err = openpgp.ClearText(doc, openpgp.Limits{})
	}
	if err != nil {
		return nil, "", err
	}
	r, err = Parse(text)
	return r, signer, err
}

// CheckSuite is the check of a keyed remote (R-DEB-019): the document names the suite the request asked for, in Suite or Codename,
// and its Valid-Until has not passed at now. suite is the name below dists/; "" skips the name check (a flat directory has no suite
// name to compare).
func (r *Release) CheckSuite(suite string, now time.Time) error {
	if suite != "" && r.Suite != suite && r.Codename != suite {
		return refuse("the document is the release of %q and not of %q", shorten(r.Suite), shorten(suite))
	}
	if !r.ValidUntil.IsZero() && now.After(r.ValidUntil) {
		return refuse("the document was valid until %s", r.ValidUntil.UTC().Format(time.RFC3339))
	}
	return nil
}

// Record is the row a stored document of this release is recorded as (rows.ReleaseRecord): document is rows.DocInRelease or
// rows.DocRelease, sum the SHA-256 of the stored bytes, verifiedBy the fingerprint that verified it ("" when none did).
func (r *Release) Record(repoID int64, dir, document string, sum [32]byte, verifiedBy string, keysSet bool) rows.ReleaseRecord {
	return rows.ReleaseRecord{
		RepoID: repoID, Dir: dir, Document: document, SHA256: sum, ReleaseDate: r.Date, ByHash: r.ByHash, KeysSet: keysSet,
		VerifiedBy: verifiedBy, Files: r.Files,
	}
}

func shorten(s string) string {
	if len(s) > 64 {
		return s[:64] + "..."
	}
	return s
}
