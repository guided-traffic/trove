package advisory

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"testing"
)

// R-VULN-012: severity_rank is critical 5, high 4, medium 3, low 2, none 1, unknown 0.
func TestSeverityRanks(t *testing.T) {
	want := map[Severity]int{
		SeverityCritical: 5, SeverityHigh: 4, SeverityMedium: 3, SeverityLow: 2, SeverityNone: 1, SeverityUnknown: 0,
	}
	for s, r := range want {
		if got := s.Rank(); got != r {
			t.Errorf("%s ranks %d, want %d", s, got, r)
		}
		if !s.Valid() {
			t.Errorf("%s is not valid", s)
		}
		if got := SeverityOfRank(r); got != s {
			t.Errorf("rank %d is %s, want %s", r, got, s)
		}
		if got := SeverityRank(string(s)); got != r {
			t.Errorf("SeverityRank(%q) = %d, want %d", s, got, r)
		}
	}
	if got := Severity("bogus").Rank(); got != 0 {
		t.Errorf("a string that is no level ranks %d, want 0 (unknown)", got)
	}
	if Severity("bogus").Valid() {
		t.Error("bogus is a valid level")
	}
	if got := Severities(); len(got) != len(want) {
		t.Fatalf("Severities lists %d levels, want %d", len(got), len(want))
	}
	prev := 99
	for _, s := range Severities() {
		if s.Rank() >= prev {
			t.Errorf("Severities is not in descending rank at %s", s)
		}
		prev = s.Rank()
	}
	if !SeverityCritical.Higher(SeverityHigh) || SeverityUnknown.Higher(SeverityNone) {
		t.Error("Higher disagrees with the ranks")
	}
}

func TestKindValid(t *testing.T) {
	if !KindVulnerability.Valid() || !KindMalicious.Valid() || Kind("x").Valid() {
		t.Error("Kind.Valid disagrees with the schema's CHECK")
	}
}

// R-VULN-003: the source is taken from the identifier prefix.
func TestR_VULN_003_SourceOf(t *testing.T) {
	cases := []struct {
		id, name, license, url string
	}{
		{"GHSA-vr7g-88fq-vhq3", "GitHub Advisory Database", "CC-BY 4.0", "https://github.com/advisories/GHSA-vr7g-88fq-vhq3"},
		{"MAL-2026-1234", "OpenSSF malicious-packages", "Apache-2.0", "https://osv.dev/vulnerability/MAL-2026-1234"},
		{"CVE-2026-0001", "osv.dev", "", "https://osv.dev/vulnerability/CVE-2026-0001"},
		{"PYSEC-2026-1", "osv.dev", "", "https://osv.dev/vulnerability/PYSEC-2026-1"},
	}
	for _, c := range cases {
		got := SourceOf(c.id)
		if got.Name != c.name || got.License != c.license || got.URL != c.url {
			t.Errorf("SourceOf(%q) = %+v, want %s / %s / %s", c.id, got, c.name, c.license, c.url)
		}
	}
	if k := KEVSource(); k.Name != "CISA KEV" || k.License != "CC0 1.0" {
		t.Errorf("KEVSource = %+v", k)
	}
}

// R-VULN-017 url: the GHSA address when the canonical identifier or an alias is a GHSA identifier.
func TestR_VULN_017_URLOf(t *testing.T) {
	cases := []struct {
		name      string
		canonical string
		aliases   []string
		want      string
	}{
		{"canonical GHSA", "GHSA-aaaa-bbbb-cccc", []string{"CVE-2026-0001"}, "https://github.com/advisories/GHSA-aaaa-bbbb-cccc"},
		{"MAL with a GHSA alias", "MAL-2026-1", []string{"GHSA-9j83-qj34-g5vc"}, "https://github.com/advisories/GHSA-9j83-qj34-g5vc"},
		{"two GHSA aliases: the smaller", "MAL-2026-1", []string{"GHSA-zzzz-zzzz-zzzz", "GHSA-aaaa-aaaa-aaaa"}, "https://github.com/advisories/GHSA-aaaa-aaaa-aaaa"},
		{"no GHSA", "CVE-2026-0001", []string{"OSV-2026-1"}, "https://osv.dev/vulnerability/CVE-2026-0001"},
		{"MAL alone", "MAL-2026-1234", nil, "https://osv.dev/vulnerability/MAL-2026-1234"},
		{"an identifier with a path character is escaped", "X-a/b", nil, "https://osv.dev/vulnerability/X-a%2Fb"},
	}
	for _, c := range cases {
		if got := URLOf(c.canonical, c.aliases); got != c.want {
			t.Errorf("%s: URLOf = %q, want %q", c.name, got, c.want)
		}
	}
}

// Q-VULN-17: 53 bits of SHA-256 of "npm:<canonical id>", stable across replicas, releases and restores.
func TestQ_VULN_17_NPMAuditID(t *testing.T) {
	for _, id := range []string{"GHSA-vr7g-88fq-vhq3", "MAL-2026-1234", "x"} {
		sum := sha256.Sum256([]byte("npm:" + id))
		want := int64(binary.BigEndian.Uint64(sum[:8]) >> 11) // the first 53 bits
		if got := NPMAuditID(id, 0); got != want {
			t.Errorf("NPMAuditID(%q) = %d, want the first 53 bits of SHA-256(npm:%s) = %d", id, got, id, want)
		}
		if want < 0 || want >= 1<<53 {
			t.Errorf("NPMAuditID(%q) = %d does not fit 53 bits", id, want)
		}
		sum = sha256.Sum256([]byte("npm:" + id + ":1"))
		if got, want := NPMAuditID(id, 1), int64(binary.BigEndian.Uint64(sum[:8])>>11); got != want {
			t.Errorf("NPMAuditID(%q, 1) = %d, want %d (the input npm:%s:1)", id, got, want, id)
		}
	}
}

func TestQ_VULN_17_AssignedIDsDoNotCollideAndDoNotDependOnOrder(t *testing.T) {
	ids := []string{"GHSA-a", "GHSA-b", "MAL-1", "CVE-1", "GHSA-b"}
	a := NPMAuditIDs(ids)
	b := NPMAuditIDs([]string{"CVE-1", "MAL-1", "GHSA-b", "GHSA-a"})
	if len(a) != 4 {
		t.Fatalf("got %d ids for 4 distinct identifiers", len(a))
	}
	seen := map[int64]string{}
	for id, n := range a {
		if other, dup := seen[n]; dup {
			t.Errorf("%s and %s share id %d", id, other, n)
		}
		seen[n] = id
		if b[id] != n {
			t.Errorf("%s: %d then %d, the order of the input changed the id", id, n, b[id])
		}
		if n != NPMAuditID(id, 0) {
			t.Errorf("%s: %d is not the first hash input's number %d", id, n, NPMAuditID(id, 0))
		}
	}
}

func TestEventJSONIsTheOSVForm(t *testing.T) {
	rs := []Range{{Type: RangeSemver, Events: []Event{
		{EventIntroduced, "0"}, {EventFixed, "1.2.3"}, {EventLastAffected, "2.0.0"}, {EventLimit, "3.0.0"},
	}}}
	b, err := json.Marshal(rs)
	if err != nil {
		t.Fatal(err)
	}
	const want = `[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"1.2.3"},{"last_affected":"2.0.0"},{"limit":"3.0.0"}]}]`
	if string(b) != want {
		t.Errorf("marshal = %s, want %s", b, want)
	}
	var back []Range
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || len(back[0].Events) != 4 || back[0].Events[1] != (Event{EventFixed, "1.2.3"}) {
		t.Errorf("round trip = %+v", back)
	}
}

func TestEventJSONRefusesWhatIsNotOneKnownKey(t *testing.T) {
	for _, in := range []string{`{}`, `{"introduced":"0","fixed":"1"}`, `{"nope":"1"}`, `{"fixed":1}`, `"fixed"`, `null`} {
		var e Event
		if err := json.Unmarshal([]byte(in), &e); err == nil && in != `null` {
			t.Errorf("%s decoded to %+v", in, e)
		}
	}
	if _, err := json.Marshal(Event{Kind: "x", Version: "1"}); err == nil {
		t.Error("an event of an unknown kind marshalled")
	}
}

func TestAdvisoryHelpers(t *testing.T) {
	var a Advisory
	if a.IsWithdrawn() || (CVSS{}).Present() {
		t.Error("the zero advisory is withdrawn or has a vector")
	}
	if !(CVSS{Version: "3.1", Vector: "CVSS:3.1/AV:N", Score: 7.5}).Present() {
		t.Error("a scored vector is not present")
	}
}
