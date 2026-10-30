package npmstub

import (
	"fmt"
	"net/url"
	"slices"
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

// Mode is the set of failure switches. The zero value is a healthy registry. Setting a mode
// replaces the previous one.
type Mode struct {
	// Down makes the whole registry fail: DownUnavailable, DownReset or DownHang.
	Down string `json:"down,omitempty"`
	// DelayMS delays the answer by class: "document", "tarball" or "other" (also "ping").
	DelayMS map[string]int `json:"delay_ms,omitempty"`
	// NotFound lists package names whose documents and tarballs answer 404.
	NotFound []string `json:"not_found,omitempty"`
	// Gone lists package names that answer 410.
	Gone []string `json:"gone,omitempty"`
	// BadIntegrity lists packages whose documents promise an integrity the tarballs do not have.
	BadIntegrity []string `json:"bad_integrity,omitempty"`
	// WrongName lists packages whose documents carry another name than the one requested.
	WrongName []string `json:"wrong_name,omitempty"`
	// DropTarballs makes the tarballs of dropped versions answer 404, as a registry that unpublished
	// them would; by default only the document loses the version.
	DropTarballs bool `json:"drop_tarballs,omitempty"`
	// RequireAuthorization, when set, answers 401 to every request whose Authorization header is
	// not exactly this value.
	RequireAuthorization string `json:"require_authorization,omitempty"`
	// StaleCopy lists package names whose document is answered once with an older copy: the
	// package without its newest version and with a time.modified an hour earlier, as a lagging
	// mirror or CDN edge serves it (R-NPM-024). The copy is served whatever validators the request
	// carries. StaleCopySkip document requests of the name are answered normally first, and the
	// copy is held StaleCopyDelayMS before its status line, so a test can make it the answer that
	// arrives last. Only packages of the corpus have an older copy.
	StaleCopy        []string `json:"stale_copy,omitempty"`
	StaleCopySkip    int      `json:"stale_copy_skip,omitempty"`
	StaleCopyDelayMS int      `json:"stale_copy_delay_ms,omitempty"`
	// HTMLContentType lists package names whose documents and tarballs answer 200 with an HTML
	// page, as a captive portal or a login redirect target does (R-REPO-042).
	HTMLContentType []string `json:"html_content_type,omitempty"`
	// RedirectTo answers every tarball request with a 302 to this base URL and the same path,
	// as a registry does that serves its files from another host (R-REPO-039, R-REPO-043).
	RedirectTo string `json:"redirect_to,omitempty"`
	// ForeignTarballHost is the base URL the dist.tarball addresses of documents name instead of
	// the stub's own: a document that points to a host the remote repository was not given
	// (foreign_host_denied, R-REPO-043).
	ForeignTarballHost string `json:"foreign_tarball_host,omitempty"`
}

// Validate rejects a mode the stub does not implement.
func (m Mode) Validate() error {
	switch m.Down {
	case "", DownUnavailable, DownReset, DownHang:
	default:
		return fmt.Errorf("down %q: want %q, %q or %q", m.Down, DownUnavailable, DownReset, DownHang)
	}
	if m.StaleCopySkip < 0 || m.StaleCopyDelayMS < 0 || m.StaleCopyDelayMS > int(MaxHang/time.Millisecond) {
		return fmt.Errorf("stale_copy_skip %d, stale_copy_delay_ms %d: out of range", m.StaleCopySkip, m.StaleCopyDelayMS)
	}
	for _, u := range []string{m.RedirectTo, m.ForeignTarballHost} {
		if u == "" {
			continue
		}
		if parsed, err := url.Parse(u); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("%q is no absolute http or https URL", u)
		}
	}
	for class, ms := range m.DelayMS {
		if !slices.Contains([]string{KindDocument, KindTarball, KindOther, KindPing}, class) {
			return fmt.Errorf("delay_ms class %q: want document, tarball or other", class)
		}
		if ms < 0 || ms > int(MaxHang/time.Millisecond) {
			return fmt.Errorf("delay_ms %q: %d out of range", class, ms)
		}
	}
	return nil
}

func (m Mode) delay(kind string) time.Duration {
	if kind == KindPing {
		kind = KindOther
	}
	return time.Duration(m.DelayMS[kind]) * time.Millisecond
}

func (m Mode) has(list []string, name string) bool { return slices.Contains(list, name) }
