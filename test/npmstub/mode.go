package npmstub

import (
	"fmt"
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
}

// Validate rejects a mode the stub does not implement.
func (m Mode) Validate() error {
	switch m.Down {
	case "", DownUnavailable, DownReset, DownHang:
	default:
		return fmt.Errorf("down %q: want %q, %q or %q", m.Down, DownUnavailable, DownReset, DownHang)
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
