// Package osvstub is the test advisory source of R-VULN-029: an HTTPS server that serves, in the URL layout of
// osv.dev's bucket, the per-ecosystem archive (<ecosystem>/all.zip), the change file (<ecosystem>/modified_id.csv,
// rows "<RFC 3339 modified>,<id>", newest first), the single records (<ecosystem>/<id>.json) and the list of
// ecosystems (ecosystems.txt), and the known-exploited catalog in the layout of the CISA feed. It answers with the
// bucket's headers (ETag as the MD5 of the body, Last-Modified, x-goog-hash on archives, 304 for conditional
// requests, 206 for Range), counts every request per path, and is driven through a control API under /-/stub/: a
// mutable dataset (add, replace, withdraw a record; add a CVE to the catalog), toggles that switch fixture records
// in and out, and per-ecosystem faults (slow, 500, truncated archive, digest mismatch, invalid records, shrink,
// 404 for a listed record, Range off).
//
// The default dataset is the fixture of fixture.go: synthetic records for the test packages of test/npmstub and
// test/nugetstub. Records frozen from the real feeds are in testdata/real (see real.go).
//
// Use it in a Go test through httptest.NewTLSServer(New(cfg)) or Stub.Start, or as a container (cmd/osvstub). It
// answers over TLS only when started by Start; the handler itself does not care.
package osvstub

import (
	"archive/zip"
	"bytes"
	"crypto/md5" //nolint:gosec // the bucket's ETag and x-goog-hash are MD5; this is no security use
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config configures a Stub. The zero value serves the fixture for npm and NuGet and has no control API.
type Config struct {
	// Fixture is the dataset the stub starts with and returns to on ResetAll; nil means DefaultFixture().
	Fixture *Fixture
	// ControlToken enables the control API under /-/stub/ for requests with this bearer token. Empty: the API
	// does not exist (the in-process Go methods are always available).
	ControlToken string
	// Filler adds this many synthetic records to every ecosystem at start, so percentages of records are exact
	// (see SetFiller).
	Filler int
	// Now is the clock for modification times; nil means time.Now.
	Now func() time.Time
	// Logger receives one line per request; nil discards.
	Logger *slog.Logger
}

// Stub is the test advisory source. It is safe for concurrent use and implements http.Handler.
type Stub struct {
	cfg     Config
	fix     *Fixture
	control *http.ServeMux

	mu        sync.Mutex
	eco       map[string]*ecoState
	order     []string // ecosystem names, as listed in ecosystems.txt
	toggles   map[string]bool
	kev       map[string]KEVEntry
	kevAt     time.Time
	kevFault  Fault
	lastStamp time.Time

	log requestLog
}

type entry struct {
	id       string
	modified time.Time
	body     []byte
}

type ecoState struct {
	name    string
	recs    map[string]*entry
	changed time.Time
	fault   Fault
	filler  int

	built   bool
	archive []byte
	changes []byte
	sorted  []*entry // by id
}

// FixtureTime is the modification time of the fixture's records and files. Everything added later is newer.
var FixtureTime = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// New creates a Stub holding the fixture.
func New(cfg Config) *Stub {
	if cfg.Fixture == nil {
		cfg.Fixture = DefaultFixture()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	s := &Stub{cfg: cfg, fix: cfg.Fixture}
	s.control = s.controlMux()
	s.mu.Lock()
	s.load()
	s.mu.Unlock()
	return s
}

// load puts the fixture in place (caller holds mu).
func (s *Stub) load() {
	s.eco = map[string]*ecoState{}
	s.order = nil
	s.toggles = map[string]bool{}
	s.kev = map[string]KEVEntry{}
	s.kevFault = Fault{}
	s.lastStamp = FixtureTime
	s.kevAt = FixtureTime
	for _, name := range s.fix.Ecosystems() {
		s.order = append(s.order, name)
		st := &ecoState{name: name, recs: map[string]*entry{}, changed: FixtureTime}
		s.eco[name] = st
		for i, r := range s.fix.Base[name] {
			mod := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute)
			s.putLocked(st, r.ID, r.Body, mod)
		}
		if s.cfg.Filler > 0 {
			s.setFillerLocked(st, s.cfg.Filler, time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC))
		}
	}
	for _, k := range s.fix.KEV {
		s.kev[k.CVEID] = k.withDefaults(FixtureTime)
	}
}

// ErrUnknown is returned for an ecosystem, a record or a toggle the stub does not have.
var ErrUnknown = errors.New("osvstub: unknown")

func (s *Stub) ecoLocked(name string) (*ecoState, error) {
	st, ok := s.eco[name]
	if !ok {
		return nil, fmt.Errorf("%w ecosystem %q", ErrUnknown, name)
	}
	return st, nil
}

// next returns a modification time later than every one handed out before (caller holds mu).
func (s *Stub) next() time.Time {
	t := s.cfg.Now().UTC().Truncate(time.Microsecond)
	if !t.After(s.lastStamp) {
		t = s.lastStamp.Add(time.Millisecond)
	}
	s.lastStamp = t
	return t
}

// putLocked stores a record body with the given modified (the body's own member is replaced).
func (s *Stub) putLocked(st *ecoState, id string, body []byte, modified time.Time) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		panic(err) // callers validated
	}
	obj["modified"], _ = json.Marshal(stamp(modified))
	out, _ := json.Marshal(obj)
	st.recs[id] = &entry{id: id, modified: modified, body: out}
	if modified.After(st.changed) {
		st.changed = modified
	}
	if modified.After(s.lastStamp) {
		s.lastStamp = modified
	}
	st.built = false
}

// Upsert adds or replaces a record of an ecosystem. body is an OSV record (a JSON object with a string "id"); its
// modified is set to a time later than every earlier one, which prepends a row to the change file and rebuilds
// the archive. An id that no client should accept ("../x") is stored as given.
func (s *Stub) Upsert(ecosystem string, body []byte) (time.Time, error) {
	return s.upsert(ecosystem, body, time.Time{})
}

// UpsertAt is Upsert with a modified chosen by the caller.
func (s *Stub) UpsertAt(ecosystem string, body []byte, modified time.Time) (time.Time, error) {
	return s.upsert(ecosystem, body, modified.UTC())
}

func (s *Stub) upsert(ecosystem string, body []byte, modified time.Time) (time.Time, error) {
	var probe struct {
		ID *string `json:"id"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe.ID == nil || *probe.ID == "" {
		return time.Time{}, errors.New("osvstub: the record needs a string id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.ecoLocked(ecosystem)
	if err != nil {
		return time.Time{}, err
	}
	if modified.IsZero() {
		modified = s.next()
	}
	s.putLocked(st, *probe.ID, body, modified)
	return modified, nil
}

// UpsertAdvisory is Upsert for a built record.
func (s *Stub) UpsertAdvisory(ecosystem string, a Advisory) (time.Time, error) {
	return s.Upsert(ecosystem, a.JSON())
}

// Withdraw sets the record's withdrawn (and a new modified): the archive keeps it, as the bucket does.
func (s *Stub) Withdraw(ecosystem, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.ecoLocked(ecosystem)
	if err != nil {
		return err
	}
	e, ok := st.recs[id]
	if !ok {
		return fmt.Errorf("%w record %q", ErrUnknown, id)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(e.body, &obj); err != nil {
		return err
	}
	now := s.next()
	obj["withdrawn"], _ = json.Marshal(stamp(now))
	body, _ := json.Marshal(obj)
	s.putLocked(st, id, body, now)
	return nil
}

// Remove deletes a record from the archive and the change file (the bucket never does; this is for shrink-like
// tests that need the data itself to change).
func (s *Stub) Remove(ecosystem, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.ecoLocked(ecosystem)
	if err != nil {
		return err
	}
	if _, ok := st.recs[id]; !ok {
		return fmt.Errorf("%w record %q", ErrUnknown, id)
	}
	delete(st.recs, id)
	st.changed = s.next()
	st.built = false
	return nil
}

// Toggle switches a fixture group in (its records are added with a new modified) or out (they are removed). The
// names are those of Fixture.Toggles.
func (s *Stub) Toggle(name string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	group, ok := s.fix.Toggles[name]
	if !ok {
		return fmt.Errorf("%w toggle %q", ErrUnknown, name)
	}
	for eco, advs := range group {
		st, err := s.ecoLocked(eco)
		if err != nil {
			return err
		}
		for _, a := range advs {
			if on {
				s.putLocked(st, a.ID, a.Body, s.next())
			} else if _, there := st.recs[a.ID]; there {
				delete(st.recs, a.ID)
				st.changed = s.next()
				st.built = false
			}
		}
	}
	s.toggles[name] = on
	return nil
}

// Toggles lists every toggle with its state.
func (s *Stub) Toggles() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for name := range s.fix.Toggles {
		out[name] = s.toggles[name]
	}
	return out
}

var fillerID = regexp.MustCompile(`^GHSA-fill-[0-9]{4}-0000$`)

// SetFiller makes the ecosystem hold exactly n synthetic filler records (GHSA-fill-NNNN-0000, for the packages
// trove-test-filler-NNNN or Trove.Test.FillerNNNN) besides its other records, so a test that needs "30 % fewer"
// or "2 % invalid" has records to take them from.
func (s *Stub) SetFiller(ecosystem string, n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.ecoLocked(ecosystem)
	if err != nil {
		return err
	}
	s.setFillerLocked(st, n, s.next())
	return nil
}

func (s *Stub) setFillerLocked(st *ecoState, n int, at time.Time) {
	for id := range st.recs {
		if fillerID.MatchString(id) {
			delete(st.recs, id)
		}
	}
	for i := range n {
		pkgName := fmt.Sprintf("trove-test-filler-%04d", i)
		rng := Range{Type: "SEMVER", Events: []map[string]string{Introduced("0"), Fixed("1.0.1")}}
		if st.name == "NuGet" {
			pkgName = fmt.Sprintf("Trove.Test.Filler%04d", i)
			rng.Type = "ECOSYSTEM"
		}
		a := Advisory{
			SchemaVersion: SchemaVersion, ID: fmt.Sprintf("GHSA-fill-%04d-0000", i), Summary: "Filler record " + pkgName,
			Affected: []Affected{{Package: Package{Ecosystem: st.name, Name: pkgName}, Ranges: []Range{rng}}},
		}
		s.putLocked(st, a.ID, a.JSON(), at)
	}
	st.filler = n
	if at.After(st.changed) {
		st.changed = at
	}
	st.built = false
}

// SetFault replaces the faults of an ecosystem ("kev" for the catalog); the zero Fault clears them.
func (s *Stub) SetFault(ecosystem string, f Fault) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ecosystem == "kev" {
		s.kevFault = f
		return nil
	}
	st, err := s.ecoLocked(ecosystem)
	if err != nil {
		return err
	}
	st.fault = f
	return nil
}

// Faults returns the faults that are on, by ecosystem.
func (s *Stub) Faults() map[string]Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Fault{}
	if !s.kevFault.isZero() {
		out["kev"] = s.kevFault
	}
	for name, st := range s.eco {
		if !st.fault.isZero() {
			out[name] = st.fault
		}
	}
	return out
}

// KEVEntry is one vulnerability of the catalog, with the members trove reads (R-VULN-008) and the others the feed
// has.
type KEVEntry struct {
	CVEID             string   `json:"cveID"`
	VendorProject     string   `json:"vendorProject,omitempty"`
	Product           string   `json:"product,omitempty"`
	VulnerabilityName string   `json:"vulnerabilityName,omitempty"`
	DateAdded         string   `json:"dateAdded,omitempty"`
	ShortDescription  string   `json:"shortDescription,omitempty"`
	RequiredAction    string   `json:"requiredAction,omitempty"`
	DueDate           string   `json:"dueDate,omitempty"`
	Ransomware        string   `json:"knownRansomwareCampaignUse,omitempty"`
	Notes             string   `json:"notes"`
	CWEs              []string `json:"cwes"`
}

func (k KEVEntry) withDefaults(now time.Time) KEVEntry {
	if k.VendorProject == "" {
		k.VendorProject = "Trove"
	}
	if k.Product == "" {
		k.Product = "trove-test"
	}
	if k.VulnerabilityName == "" {
		k.VulnerabilityName = "Trove test vulnerability " + k.CVEID
	}
	if k.DateAdded == "" {
		k.DateAdded = now.UTC().Format(time.DateOnly)
	}
	if k.ShortDescription == "" {
		k.ShortDescription = "Synthetic entry of the trove test catalog."
	}
	if k.RequiredAction == "" {
		k.RequiredAction = "Apply the fix of the test advisory."
	}
	if k.DueDate == "" {
		if d, err := time.Parse(time.DateOnly, k.DateAdded); err == nil {
			k.DueDate = d.AddDate(0, 0, 21).Format(time.DateOnly)
		}
	}
	if k.Ransomware == "" {
		k.Ransomware = "Unknown"
	}
	if k.CWEs == nil {
		k.CWEs = []string{}
	}
	return k
}

// AddKEV adds (or replaces) a CVE of the catalog; empty members get defaults (dateAdded: today by the clock).
func (s *Stub) AddKEV(k KEVEntry) error {
	if !regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]{4,}$`).MatchString(k.CVEID) {
		return fmt.Errorf("osvstub: %q is no CVE identifier", k.CVEID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.next()
	s.kev[k.CVEID] = k.withDefaults(t)
	s.kevAt = t
	return nil
}

// RemoveKEV drops a CVE from the catalog.
func (s *Stub) RemoveKEV(cve string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.kev[cve]; !ok {
		return fmt.Errorf("%w CVE %q", ErrUnknown, cve)
	}
	delete(s.kev, cve)
	s.kevAt = s.next()
	return nil
}

// ResetAll returns the dataset to the fixture, clears faults, toggles and the request log.
func (s *Stub) ResetAll() {
	s.mu.Lock()
	s.load()
	s.mu.Unlock()
	s.ResetCounts()
}

// IDs lists the record identifiers of an ecosystem, ascending.
func (s *Stub) IDs(ecosystem string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.ecoLocked(ecosystem)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range s.sortedLocked(st) {
		ids = append(ids, e.id)
	}
	return ids
}

// Record returns the body the stub serves for a record.
func (s *Stub) Record(ecosystem, id string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.ecoLocked(ecosystem)
	if err != nil {
		return nil, false
	}
	e, ok := st.recs[id]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), e.body...), true
}

// Archive is the healthy archive of an ecosystem, without any fault.
func (s *Stub) Archive(ecosystem string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.ecoLocked(ecosystem)
	if err != nil {
		return nil, err
	}
	s.buildLocked(st)
	return append([]byte(nil), st.archive...), nil
}

// ChangeFile is the healthy change file of an ecosystem.
func (s *Stub) ChangeFile(ecosystem string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.ecoLocked(ecosystem)
	if err != nil {
		return "", err
	}
	s.buildLocked(st)
	return string(st.changes), nil
}

func (s *Stub) sortedLocked(st *ecoState) []*entry {
	s.buildLocked(st)
	return st.sorted
}

// buildLocked renders the archive and the change file when records changed.
func (s *Stub) buildLocked(st *ecoState) {
	if st.built {
		return
	}
	st.sorted = st.sorted[:0]
	for _, e := range st.recs {
		st.sorted = append(st.sorted, e)
	}
	sort.Slice(st.sorted, func(i, j int) bool { return st.sorted[i].id < st.sorted[j].id })
	st.archive = zipOf(st.sorted)
	st.changes = changeFile(st.sorted)
	st.built = true
}

// entryName is the name of a record's file in the archive: the id, unless that is no safe file name, so a record
// with the id "../x" is a record the validation refuses and no archive the unpacking refuses.
func entryName(id string) string {
	safe := id != "" && len(id) <= 128
	for _, r := range id {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !alnum && !strings.ContainsRune("-_.:", r) {
			safe = false
		}
	}
	if safe && !strings.HasPrefix(id, ".") {
		return id + ".json"
	}
	sum := md5.Sum([]byte(id)) //nolint:gosec // a file name, not security
	return "invalid-id-" + hex.EncodeToString(sum[:6]) + ".json"
}

func zipOf(entries []*entry) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: entryName(e.id), Method: zip.Deflate, Modified: e.modified}
		w, err := zw.CreateHeader(h)
		if err != nil {
			panic(err)
		}
		_, _ = w.Write(e.body)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func changeFile(entries []*entry) []byte {
	rows := append([]*entry(nil), entries...)
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].modified.Equal(rows[j].modified) {
			return rows[i].modified.After(rows[j].modified)
		}
		return rows[i].id < rows[j].id
	})
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	for _, e := range rows {
		_ = cw.Write([]string{stamp(e.modified), e.id})
	}
	cw.Flush()
	return buf.Bytes()
}

// Info describes the dataset for the control API and for tests.
type Info struct {
	Ecosystems map[string]EcosystemInfo `json:"ecosystems"`
	KEV        int                      `json:"kev"`
	Toggles    map[string]bool          `json:"toggles"`
}

// EcosystemInfo describes one ecosystem.
type EcosystemInfo struct {
	Records       int    `json:"records"`
	ChangeRows    int    `json:"changeRows"`
	Newest        string `json:"newest"`
	ArchiveBytes  int    `json:"archiveBytes"`
	ArchiveMD5    string `json:"archiveMd5"` // hex
	ArchiveSHA256 string `json:"archiveSha256"`
}

// Info returns the state of the dataset (no faults applied).
func (s *Stub) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Info{Ecosystems: map[string]EcosystemInfo{}, KEV: len(s.kev), Toggles: map[string]bool{}}
	for _, name := range s.order {
		st := s.eco[name]
		s.buildLocked(st)
		sum := md5.Sum(st.archive) //nolint:gosec // as the bucket
		out.Ecosystems[name] = EcosystemInfo{
			Records: len(st.recs), ChangeRows: bytes.Count(st.changes, []byte("\n")), Newest: stamp(st.changed),
			ArchiveBytes: len(st.archive), ArchiveMD5: hex.EncodeToString(sum[:]), ArchiveSHA256: sha256hex(st.archive),
		}
	}
	for name := range s.fix.Toggles {
		out.Toggles[name] = s.toggles[name]
	}
	return out
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
