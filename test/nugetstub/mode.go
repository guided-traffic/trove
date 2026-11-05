package nugetstub

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Down modes: how the stub behaves as an upstream that is not working.
const (
	DownUnavailable = "503"   // every request is answered 503
	DownReset       = "reset" // the connection is cut without an answer
	DownHang        = "hang"  // the request is held until the client gives up or MaxHang passes
)

// MaxHang bounds how long a request is held in DownHang, so a test that forgets to clear the mode
// does not leave connections open for good.
const MaxHang = 2 * time.Minute

// Delay classes of Mode.DelayMS: the kinds of request, grouped, and "all" for every request.
const (
	ClassIndex        = "index"        // the service index
	ClassRegistration = "registration" // registration indexes, pages and leaves
	ClassFlat         = "flat"         // version lists and .nuspec files
	ClassNupkg        = "nupkg"        // .nupkg files
	ClassSearch       = "search"       // the search resource
	ClassCatalog      = "catalog"      // the catalog index, pages and leaves
	ClassOther        = "other"        // everything else
	ClassAll          = "all"          // added to the delay of the request's own class
)

// Mode is the set of failure switches. The zero value is a healthy registry. Setting a mode
// replaces the previous one. Package IDs in the lists match in any case.
type Mode struct {
	// Down makes the whole registry fail: DownUnavailable, DownReset or DownHang.
	Down string `json:"down,omitempty"`
	// DelayMS delays the answer by class (the Class constants). {"all": 2000} is the upstream that
	// delays every response by 2 s.
	DelayMS map[string]int `json:"delay_ms,omitempty"`
	// NotFound lists package IDs whose version list, registrations, .nuspec and .nupkg answer 404:
	// a package the upstream does not have (any more).
	NotFound []string `json:"not_found,omitempty"`
	// Gone lists package IDs that answer 410 everywhere.
	Gone []string `json:"gone,omitempty"`
	// BadDigest lists package IDs whose registrations announce a packageHash that the .nupkg
	// bytes do not have (R-NUGET-029).
	BadDigest []string `json:"bad_digest,omitempty"`
	// WrongContent lists package IDs whose .nupkg URLs serve the package of OtherID instead: the
	// content of another identity at the URL of this one (upstream_package_mismatch).
	WrongContent []string `json:"wrong_content,omitempty"`
	// OmitResources removes resources from the service index: every entry whose @type starts with
	// one of these values (for example "PackageBaseAddress").
	OmitResources []string `json:"omit_resources,omitempty"`
	// StrictHosts answers 404 to a request that reaches a resource on the wrong host of the two
	// the service index spreads them over: the index and the registrations belong to the API host,
	// the flat container and the search to the content host. It has an effect only when the two
	// hosts differ.
	StrictHosts bool `json:"strict_hosts,omitempty"`
	// RequireAuthorization, when set, answers 401 to every request whose Authorization header is
	// not exactly this value.
	RequireAuthorization string `json:"require_authorization,omitempty"`
	// GzipAlways answers the version lists and the documents of both registration hives
	// gzip-coded whatever the request's Accept-Encoding says, as an upstream does whose stored
	// blobs carry the coding themselves (nuget.org's gzip hive answers gzip to a client that asks
	// for identity). The service index and the search are not affected.
	GzipAlways bool `json:"gzip_always,omitempty"`
	// Catalog lists Catalog/3.0.0 in the service index and serves the catalog (catalog.go); without it the service index
	// does not list it and every catalog path answers 404, like an upstream that does not implement it.
	Catalog bool `json:"catalog,omitempty"`
	// SearchSkipCap makes the search refuse a skip of this many rows or more with 400, as an upstream does that limits how deep
	// a listing may page; 0: no limit. The hit count stays the real one.
	SearchSkipCap int `json:"search_skip_cap,omitempty"`
}

// Validate rejects a mode the stub does not implement.
func (m Mode) Validate() error {
	switch m.Down {
	case "", DownUnavailable, DownReset, DownHang:
	default:
		return fmt.Errorf("down %q: want %q, %q or %q", m.Down, DownUnavailable, DownReset, DownHang)
	}
	if m.SearchSkipCap < 0 {
		return fmt.Errorf("search_skip_cap %d: not negative", m.SearchSkipCap)
	}
	for class, ms := range m.DelayMS {
		if !slices.Contains([]string{ClassIndex, ClassRegistration, ClassFlat, ClassNupkg, ClassSearch, ClassCatalog, ClassOther, ClassAll}, class) {
			return fmt.Errorf("delay_ms class %q: want index, registration, flat, nupkg, search, catalog, other or all", class)
		}
		if ms < 0 || ms > int(MaxHang/time.Millisecond) {
			return fmt.Errorf("delay_ms %q: %d out of range", class, ms)
		}
	}
	return nil
}

// delay is the hold for a request of a kind.
func (m Mode) delay(kind string) time.Duration {
	class := ClassOther
	switch kind {
	case KindIndex:
		class = ClassIndex
	case KindRegIndex, KindRegPage, KindRegLeaf:
		class = ClassRegistration
	case KindVersions, KindNuspec:
		class = ClassFlat
	case KindNupkg:
		class = ClassNupkg
	case KindSearch:
		class = ClassSearch
	case KindCatalogIndex, KindCatalogPage, KindCatalogLeaf:
		class = ClassCatalog
	}
	return time.Duration(m.DelayMS[class]+m.DelayMS[ClassAll]) * time.Millisecond
}

func (m Mode) has(list []string, id string) bool {
	return id != "" && slices.ContainsFunc(list, func(s string) bool { return strings.EqualFold(s, id) })
}
