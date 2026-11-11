package ansiblestub

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// Down modes: how the stub behaves as an upstream that is not working. Stopping the listener itself is Running.Close.
const (
	DownUnavailable = "503"   // every request is answered 503
	DownReset       = "reset" // the connection is cut without an answer
	DownHang        = "hang"  // the request is held until the client gives up or MaxHang passes
)

// MaxHang bounds how long a request is held in DownHang or by a delay, so a test that forgets to clear the mode does not leave
// connections open for good.
const MaxHang = 2 * time.Minute

// Classes of request, for Mode.DelayMS, Mode.FailStatus and Throttle.Class. ClassAll adds to the class of the request itself.
const (
	ClassRoot        = "root"         // the API root
	ClassIndex       = "index"        // the collection index
	ClassCollection  = "collection"   // a collection document
	ClassVersions    = "versions"     // a collection's version list
	ClassVersion     = "version"      // a collection version document
	ClassDocs        = "docs"         // the docs-blob document of a collection version (R-ANS-035)
	ClassArtifact    = "artifact"     // a collection archive, also behind the redirect of the public Galaxy
	ClassRole        = "role"         // the v1 role routes: lookup, role document, version lists
	ClassRoleArchive = "role-archive" // a role archive on the GitHub stand-in or the unlisted host, and on the codeload host
	ClassRealm       = "realm"        // the token endpoint of the realm
	ClassOther       = "other"
	ClassAll         = "all"
)

var classes = []string{ClassRoot, ClassIndex, ClassCollection, ClassVersions, ClassVersion, ClassDocs, ClassArtifact, ClassRole, ClassRoleArchive, ClassRealm, ClassOther, ClassAll}

// The modes of the docs-blob route (Mode.Docs): how the stub answers the documentation of a version.
const (
	DocsNotFound = "404"     // the route answers 404, as a Galaxy without documentation
	DocsError    = "500"     // the route answers 500
	DocsHuge     = "huge"    // the document is larger than the 16 MiB a remote stores
	DocsInvalid  = "invalid" // the document has an item without a content_name
)

// AllCollections in Mode.DigestChanged and Mode.WrongDigest names every collection.
const AllCollections = "all"

// Mode is the set of switches. The zero value is a healthy Galaxy: documents served at their direct paths, uncompressed, with the
// digests of the archives it serves. Setting a mode replaces the previous one. The throttle (ratelimit) is a switch of its own,
// SetThrottle.
type Mode struct {
	// Down makes the whole stub fail: DownUnavailable, DownReset or DownHang. The realm is not affected.
	Down string `json:"down,omitempty"`
	// DelayMS delays the answer by class (the Class constants). {"all": 2000} delays every response by 2 s (the slow switch).
	DelayMS map[string]int `json:"delay_ms,omitempty"`
	// FailStatus answers the requests of a class with this 5xx status instead of the content: {"artifact": 502}.
	FailStatus map[string]int `json:"fail_status,omitempty"`

	// Gzip compresses a JSON document when the client sent Accept-Encoding: gzip, as the public Galaxy does.
	Gzip bool `json:"gzip,omitempty"`
	// Redirect makes the stub answer as the public Galaxy: a request below /api/v3/collections/ is a 302 to the same path below
	// /api/v3/plugin/ansible/content/published/collections/index/, and an archive request a 302 to a signed address
	// /api/pulp/content/published/<file>?expires=...&validate_token=... on the same host.
	Redirect bool `json:"redirect,omitempty"`
	// RejectToken answers every request of the Galaxy host with 401 in the Galaxy shape, as an upstream that rejects the
	// credential it was sent.
	RejectToken bool `json:"reject_token,omitempty"`
	// NoV1 leaves "v1" out of the API root and answers the v1 routes 404, as a hub that serves collections only.
	NoV1 bool `json:"no_v1,omitempty"`
	// DigestChanged lists collections ("ns.name", or AllCollections) whose archives the upstream has republished: the stub serves
	// a second archive of each version and announces that archive's SHA-256 and size.
	DigestChanged []string `json:"digest_changed,omitempty"`
	// WrongDigest lists collections whose archives the stub serves with other bytes than their documents announce: the second
	// archive is served and the announced SHA-256 and size are those of the first. It wins over DigestChanged.
	WrongDigest []string `json:"wrong_digest,omitempty"`
	// Docs changes the answer of the docs-blob route: DocsNotFound, DocsError, DocsHuge or DocsInvalid. The other routes are unchanged.
	Docs string `json:"docs,omitempty"`
	// DocsForeignHref makes the version documents name an href on another host, so that the address of the documentation is not the
	// upstream's.
	DocsForeignHref bool `json:"docs_foreign_href,omitempty"`
	// WrongIdentity lists collections whose archives are those of another collection ("swapped.other", the same version): the
	// document announces the digest of the bytes it serves, so the digest verifies and only the identity inside the archive is
	// wrong. It wins over the digest modes.
	WrongIdentity []string `json:"wrong_identity,omitempty"`
}

// Validate rejects a mode the stub does not implement.
func (m Mode) Validate() error {
	switch m.Down {
	case "", DownUnavailable, DownReset, DownHang:
	default:
		return fmt.Errorf("down %q: want %q, %q or %q", m.Down, DownUnavailable, DownReset, DownHang)
	}
	switch m.Docs {
	case "", DocsNotFound, DocsError, DocsHuge, DocsInvalid:
	default:
		return fmt.Errorf("docs %q: want %q, %q, %q or %q", m.Docs, DocsNotFound, DocsError, DocsHuge, DocsInvalid)
	}
	for class, ms := range m.DelayMS {
		if !slices.Contains(classes, class) {
			return fmt.Errorf("delay_ms class %q: want one of %s", class, strings.Join(classes, ", "))
		}
		if ms < 0 || ms > int(MaxHang/time.Millisecond) {
			return fmt.Errorf("delay_ms %q: %d out of range", class, ms)
		}
	}
	for class, status := range m.FailStatus {
		if !slices.Contains(classes, class) {
			return fmt.Errorf("fail_status class %q: want one of %s", class, strings.Join(classes, ", "))
		}
		if status < 500 || status > 599 {
			return fmt.Errorf("fail_status %q: %d is no 5xx status", class, status)
		}
	}
	for _, list := range [][]string{m.DigestChanged, m.WrongDigest, m.WrongIdentity} {
		for _, n := range list {
			if n != AllCollections && !validFullName(n) {
				return fmt.Errorf("collection %q: want <namespace>.<name> or %q", n, AllCollections)
			}
		}
	}
	return nil
}

// validFullName reports whether n is "<namespace>.<name>" with one dot.
func validFullName(n string) bool {
	ns, name, ok := strings.Cut(n, ".")
	return ok && ns != "" && name != "" && !strings.Contains(name, ".")
}

// delay is the hold for a request of a class.
func (m Mode) delay(class string) time.Duration {
	return time.Duration(m.DelayMS[class]+m.DelayMS[ClassAll]) * time.Millisecond
}

// failStatus is the status a class fails with, or 0.
func (m Mode) failStatus(class string) int {
	if s := m.FailStatus[class]; s != 0 {
		return s
	}
	return m.FailStatus[ClassAll]
}

func listed(list []string, fullName string) bool {
	return slices.Contains(list, AllCollections) || slices.Contains(list, fullName)
}

// clone copies the maps and slices, so that a caller of Mode or SetMode cannot change the stub's mode afterwards.
func (m Mode) clone() Mode {
	m.DelayMS = maps.Clone(m.DelayMS)
	m.FailStatus = maps.Clone(m.FailStatus)
	m.DigestChanged = slices.Clone(m.DigestChanged)
	m.WrongDigest = slices.Clone(m.WrongDigest)
	m.WrongIdentity = slices.Clone(m.WrongIdentity)
	return m
}
