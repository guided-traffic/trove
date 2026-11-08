package debstub

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
	ClassRelease = "release" // InRelease, Release and Release.gpg
	ClassIndex   = "index"   // Packages, Sources and their compressed forms
	ClassDiff    = "diff"    // Packages.diff/Index and the patches
	ClassByHash  = "by-hash" // any by-hash file
	ClassPool    = "pool"    // package and source files
	ClassOther   = "other"
	ClassAll     = "all"
)

var classes = []string{ClassRelease, ClassIndex, ClassDiff, ClassByHash, ClassPool, ClassOther, ClassAll}

// Values of Mode.Date and Mode.ValidUntil.
const (
	// DateOlder dates the Release an hour before the generation's date, so it is older than the one served before.
	DateOlder = "older"
	// DateAhead dates the Release 20 minutes after the stub's clock.
	DateAhead = "ahead"
	// ValidUntilPassed sets a Valid-Until ten minutes before the stub's clock.
	ValidUntilPassed = "passed"
	// ValidUntilSixDays sets a Valid-Until six days after the Release's date.
	ValidUntilSixDays = "6d"
)

// Mode is the set of switches. The zero value is a healthy archive: by-hash on, InRelease with its Hash header, every release signed
// by the key a with SHA-512 at the generation's date, no validators withheld. Setting a mode replaces the previous one.
type Mode struct {
	// Down makes the whole archive fail: DownUnavailable, DownReset or DownHang.
	Down string `json:"down,omitempty"`
	// DelayMS delays the answer by class (the Class constants). {"all": 2000} delays every response by 2 s.
	DelayMS map[string]int `json:"delay_ms,omitempty"`
	// FailStatus answers the requests of a class with this 5xx status instead of the content: {"pool": 502}.
	FailStatus map[string]int `json:"fail_status,omitempty"`

	// NoByHash turns by-hash off: the Release leaves out Acquire-By-Hash and every by-hash path answers 404.
	NoByHash bool `json:"no_by_hash,omitempty"`
	// NoInRelease makes InRelease answer 404; Release and Release.gpg stay.
	NoInRelease bool `json:"no_inrelease,omitempty"`
	// NoHashHeader leaves the Hash armor header out of InRelease.
	NoHashHeader bool `json:"no_hash_header,omitempty"`
	// SignWith names the keys that sign (KeyNames); empty means ["a"]. Two names co-sign; "c" is a key nobody configured, "d" a
	// key that expired and signs with a time inside its validity.
	SignWith []string `json:"sign_with,omitempty"`
	// SignHash is the hash of the signatures: sha512 (default), sha256 or sha1.
	SignHash string `json:"sign_hash,omitempty"`
	// SignatureSkewSeconds is added to the creation time of every signature of a key other than "d": positive makes a signature
	// from the future, negative one older than the Release's Date.
	SignatureSkewSeconds int `json:"signature_skew_seconds,omitempty"`
	// BrokenSignature makes every signature fail: InRelease's text is changed after signing and Release.gpg signs other bytes.
	BrokenSignature bool `json:"broken_signature,omitempty"`
	// Date is "", DateOlder or DateAhead.
	Date string `json:"date,omitempty"`
	// ValidUntil is "", ValidUntilPassed or ValidUntilSixDays.
	ValidUntil string `json:"valid_until,omitempty"`

	// NotFound lists archives (suite ids and codenames, "flat", "flatroot") whose every path answers 404.
	NotFound []string `json:"not_found,omitempty"`
	// WrongPackage lists package file names (or "all") whose bytes do not match their index entry.
	WrongPackage []string `json:"wrong_package,omitempty"`
	// IndexMismatch serves the plain index paths (Packages, Sources, with and without .gz) with content that differs from what the
	// Release lists, while the by-hash paths stay right: an index that matches no release.
	IndexMismatch bool `json:"index_mismatch,omitempty"`
	// NoValidators leaves ETag and Last-Modified out and ignores conditional requests: an upstream that cannot answer 304.
	NoValidators bool `json:"no_validators,omitempty"`
}

// Validate rejects a mode the stub does not implement.
func (m Mode) Validate() error {
	switch m.Down {
	case "", DownUnavailable, DownReset, DownHang:
	default:
		return fmt.Errorf("down %q: want %q, %q or %q", m.Down, DownUnavailable, DownReset, DownHang)
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
	for _, k := range m.SignWith {
		if !slices.Contains(KeyNames(), k) {
			return fmt.Errorf("sign_with %q: want keys from %s", k, strings.Join(KeyNames(), ", "))
		}
	}
	if _, _, err := hashByName(m.SignHash); err != nil {
		return err
	}
	if m.SignatureSkewSeconds < -86400 || m.SignatureSkewSeconds > 86400 {
		return fmt.Errorf("signature_skew_seconds %d out of range", m.SignatureSkewSeconds)
	}
	switch m.Date {
	case "", DateOlder, DateAhead:
	default:
		return fmt.Errorf("date %q: want %q or %q", m.Date, DateOlder, DateAhead)
	}
	switch m.ValidUntil {
	case "", ValidUntilPassed, ValidUntilSixDays:
	default:
		return fmt.Errorf("valid_until %q: want %q or %q", m.ValidUntil, ValidUntilPassed, ValidUntilSixDays)
	}
	return nil
}

// signKeys are the keys that sign; "a" when none are named.
func (m Mode) signKeys() []string {
	if len(m.SignWith) == 0 {
		return []string{"a"}
	}
	return m.SignWith
}

// signingKey is the part of the mode that decides the bytes of the signed documents; two modes with equal keys share them.
func (m Mode) signingKey(archiveID string, gen int) string {
	return fmt.Sprintf("%s/%d/%v/%s/%d/%t/%t/%s/%s/%t", archiveID, gen, m.signKeys(), m.SignHash, m.SignatureSkewSeconds, m.BrokenSignature, m.NoHashHeader, m.Date, m.ValidUntil, m.NoByHash)
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

// clone copies the maps and slices, so that a caller of Mode or SetMode cannot change the stub's mode afterwards.
func (m Mode) clone() Mode {
	m.DelayMS = maps.Clone(m.DelayMS)
	m.FailStatus = maps.Clone(m.FailStatus)
	m.SignWith = slices.Clone(m.SignWith)
	m.NotFound = slices.Clone(m.NotFound)
	m.WrongPackage = slices.Clone(m.WrongPackage)
	return m
}
