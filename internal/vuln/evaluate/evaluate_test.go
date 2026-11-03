package evaluate

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

func finding(status vuln.Status) *store.Finding {
	return &store.Finding{
		ID: "f1", AdvisoryID: "GHSA-1", Kind: advisory.KindVulnerability, Severity: advisory.SeverityMedium, CVSS: 5.3, Status: status,
		AdvisoryModifiedU: 100,
	}
}

func advisoryOf(sev advisory.Severity) *matched {
	return &matched{ID: "GHSA-1", Kind: advisory.KindVulnerability, Severity: sev, CVSS: 5.3, ModifiedUs: 100}
}

// R-VULN-012: a material change is a rise of the severity level, a kind that became malicious or an advisory that became
// known exploited; text, a lower severity or a new modification time are not.
func TestR_VULN_012_MaterialChange(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*matched)
		want   bool
	}{
		{"nothing changed", func(*matched) {}, false},
		{"the level rose", func(m *matched) { m.Severity = advisory.SeverityCritical }, true},
		{"the level fell", func(m *matched) { m.Severity = advisory.SeverityLow }, false},
		{"the score changed inside the level", func(m *matched) { m.CVSS = 6.1 }, false},
		{"the kind became malicious", func(m *matched) { m.Kind = advisory.KindMalicious }, true},
		{"the advisory became known exploited", func(m *matched) { m.KnownExploited = true }, true},
		{"the record was only modified", func(m *matched) { m.ModifiedUs = 200 }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := advisoryOf(advisory.SeverityMedium)
			c.mutate(m)
			if _, got := m.material(finding(vuln.StatusDismissed)); got != c.want {
				t.Errorf("material = %v, want %v", got, c.want)
			}
		})
	}
}

// R-VULN-012: the facts that changed, "modified" only when nothing else did.
func TestR_VULN_012_Changes(t *testing.T) {
	f := finding(vuln.StatusConfirmed)
	m := advisoryOf(advisory.SeverityMedium)
	if got := m.changes(f); len(got) != 0 {
		t.Errorf("unchanged: %v", got)
	}
	m.ModifiedUs = 300
	if got := strings.Join(m.changes(f), ","); got != "modified" {
		t.Errorf("modified only: %q", got)
	}
	m.Severity, m.FixAvailable, m.Fixed = advisory.SeverityHigh, true, []string{"1.2.0"}
	if got := strings.Join(m.changes(f), ","); got != "severity,fix" {
		t.Errorf("severity and fix: %q", got)
	}
}

// R-VULN-012: a resolved finding returns to the status it had; a blocked one blocked; a dismissed one needs triage again when
// a material change happened meanwhile; a finding without a recorded status needs triage.
func TestR_VULN_012_Redetected(t *testing.T) {
	cases := []struct {
		name       string
		before     vuln.Status
		raise      bool
		want       vuln.Status
		wantReopen bool
	}{
		{"confirmed", vuln.StatusConfirmed, false, vuln.StatusConfirmed, false},
		{"blocked", vuln.StatusBlocked, false, vuln.StatusBlocked, false},
		{"dismissed, nothing material", vuln.StatusDismissed, false, vuln.StatusDismissed, false},
		{"dismissed, the level rose meanwhile", vuln.StatusDismissed, true, vuln.StatusNeedsTriage, true},
		{"needs triage", vuln.StatusNeedsTriage, false, vuln.StatusNeedsTriage, false},
		{"none recorded", "", false, vuln.StatusNeedsTriage, false},
		{"garbage", "bogus", false, vuln.StatusNeedsTriage, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := finding(vuln.StatusResolved)
			f.BeforeResolution = c.before
			m := advisoryOf(advisory.SeverityMedium)
			if c.raise {
				m.Severity = advisory.SeverityCritical
			}
			got, reopened, _ := redetected(f, m)
			if got != c.want || reopened != c.wantReopen {
				t.Errorf("redetected = %s, %v; want %s, %v", got, reopened, c.want, c.wantReopen)
			}
		})
	}
}

// R-VULN-015: the status a finding would have after the fill's evaluation decides the block of a miss.
func TestR_VULN_015_EffectiveStatus(t *testing.T) {
	m := advisoryOf(advisory.SeverityMedium)
	if got := effectiveStatus(nil, m); got != vuln.StatusNeedsTriage {
		t.Errorf("no finding: %s", got)
	}
	f := finding(vuln.StatusResolved)
	f.ResolvedReason, f.BeforeResolution = vuln.ResolvedVersionRemoved, vuln.StatusBlocked
	if got := effectiveStatus(f, m); got != vuln.StatusBlocked {
		t.Errorf("a blocked finding evicted: %s", got)
	}
	d := finding(vuln.StatusDismissed)
	if got := effectiveStatus(d, m); got != vuln.StatusDismissed {
		t.Errorf("dismissed: %s", got)
	}
	m.KnownExploited = true
	if got := effectiveStatus(d, m); got != vuln.StatusNeedsTriage {
		t.Errorf("dismissed, now known exploited: %s", got)
	}
}

// R-VULN-015: the finding that blocks is the one the lateral join returns: malicious first, then the higher level, then the
// smaller identifier.
func TestR_VULN_015_BetterBlock(t *testing.T) {
	mal := blocking{m: &matched{ID: "MAL-1", Severity: advisory.SeverityLow}, reason: vuln.BlockMalicious}
	hi := blocking{m: &matched{ID: "GHSA-2", Severity: advisory.SeverityHigh}, reason: vuln.BlockTriage}
	lo := blocking{m: &matched{ID: "GHSA-1", Severity: advisory.SeverityLow}, reason: vuln.BlockTriage}
	if !betterBlock(mal, hi) || betterBlock(hi, mal) {
		t.Error("a malicious block is not first")
	}
	if !betterBlock(hi, lo) || betterBlock(lo, hi) {
		t.Error("the higher level is not first")
	}
	lo2 := blocking{m: &matched{ID: "GHSA-0", Severity: advisory.SeverityLow}, reason: vuln.BlockTriage}
	if !betterBlock(lo2, lo) {
		t.Error("the smaller identifier is not first")
	}
}

type dottedAdapter struct{}

func (dottedAdapter) Ecosystem() string                       { return "test" }
func (dottedAdapter) AdvisoryName(n string) (string, bool)    { return n, true }
func (dottedAdapter) AdvisoryVersion(v string) (string, bool) { return v, strings.Count(v, ".") == 2 }

func (dottedAdapter) CompareVersions(a, b string) (int, error) {
	return strings.Compare(pad(a), pad(b)), nil
}

func pad(v string) string {
	parts := strings.Split(v, ".")
	for i, p := range parts {
		parts[i] = strings.Repeat("0", 4-len(p)) + p
	}
	return strings.Join(parts, ".")
}

// R-VULN-009, R-VULN-012: the entries of several records of one alias group are one advisory, the fixed versions and the fix
// flag are collected over the entries that affect the version, a withdrawn record matches nothing, and a group whose every
// entry is withdrawn is withdrawn.
func TestR_VULN_009_MatchVersionGroupsEntriesByCanonicalIdentifier(t *testing.T) {
	fixed := func(v string) advisory.Affected {
		return advisory.Affected{Name: "p", Ranges: []advisory.Range{{Type: advisory.RangeEcosystem, Events: []advisory.Event{{Kind: "introduced", Version: "0"}, {Kind: "fixed", Version: v}}}}, Fixed: []string{v}}
	}
	rows := []store.AffectedRow{
		{Name: "p", RecordID: "GHSA-1", CanonicalID: "GHSA-1", Kind: advisory.KindVulnerability, Severity: advisory.SeverityHigh, Entry: fixed("1.2.0")},
		{Name: "p", RecordID: "CVE-1", CanonicalID: "GHSA-1", Kind: advisory.KindVulnerability, Severity: advisory.SeverityHigh, Entry: fixed("1.5.0")},
		{Name: "p", RecordID: "GHSA-2", CanonicalID: "GHSA-2", Withdrawn: true, Kind: advisory.KindVulnerability, Severity: advisory.SeverityLow, Entry: fixed("9.0.0")},
		{Name: "p", RecordID: "GHSA-3", CanonicalID: "GHSA-3", Kind: advisory.KindVulnerability, Severity: advisory.SeverityLow, Entry: advisory.Affected{
			Name: "p", Ranges: []advisory.Range{{Type: advisory.RangeEcosystem, Events: []advisory.Event{{Kind: "introduced", Version: "0"}, {Kind: "fixed", Version: "bad"}}}},
		}},
	}
	g := groupAffected(rows)["p"]
	if !g.withdrawn["GHSA-2"] || g.withdrawn["GHSA-1"] {
		t.Errorf("withdrawn groups: %v", g.withdrawn)
	}
	var st stats
	got := matchVersion(dottedAdapter{}, g, "1.1.0", &st)
	if len(got) != 1 || got["GHSA-1"] == nil {
		t.Fatalf("matched %v", got)
	}
	if m := got["GHSA-1"]; !m.FixAvailable || strings.Join(m.Fixed, ",") != "1.2.0,1.5.0" {
		t.Errorf("GHSA-1: %+v", m)
	}
	if got["GHSA-2"] != nil {
		t.Error("a withdrawn record matched")
	}
	if st.unevaluable != 1 {
		t.Errorf("unevaluable = %d, want 1", st.unevaluable)
	}
	if got := matchVersion(dottedAdapter{}, g, "1.3.0", &st); got["GHSA-1"] == nil || strings.Join(got["GHSA-1"].Fixed, ",") != "1.5.0" {
		t.Errorf("1.3.0: %v", got)
	}
}

type fakeClaims map[string][]repository.ClaimMatch

func (f fakeClaims) ClaimsFor(_ context.Context, format, name string) ([]repository.ClaimMatch, error) {
	return f[format+"/"+name], nil
}

type fakeRepos map[string]repository.Repository

func (f fakeRepos) ByKey(_ context.Context, key string) (repository.Repository, error) {
	r, ok := f[key]
	if !ok {
		return repository.Repository{}, errors.New("not found")
	}
	return r, nil
}
func (f fakeRepos) List(context.Context) ([]repository.Repository, error) { return nil, nil }

// R-VULN-010: a name is internal when a claim of a local repository of the format that does not set always covers it; the
// claim of a repository with always, of a remote or of another format does not count.
func TestR_VULN_010_ClaimRule(t *testing.T) {
	local := func(key, format string) repository.Repository {
		return repository.Repository{Key: key, Class: repository.Local, Format: format}
	}
	repos := fakeRepos{"npm-local": local("npm-local", "npm"), "npm-nugetish": local("npm-nugetish", "nuget"), "npm-remote": {Key: "npm-remote", Class: repository.Remote, Format: "npm"}}
	claims := fakeClaims{
		"npm/@acme/ui":   {{Repository: "npm-local", Pattern: "@acme/*"}},
		"npm/other":      {{Repository: "npm-nugetish"}},
		"npm/remote-one": {{Repository: "npm-remote"}},
		"npm/ghost":      {{Repository: "missing"}},
		"npm/two":        {{Repository: "npm-remote"}, {Repository: "npm-local"}},
	}
	rule := ClaimRule{Claims: claims, Repos: repos}
	cases := map[string]struct {
		internal bool
		repo     string
	}{
		"@acme/ui": {true, "npm-local"}, "other": {false, ""}, "remote-one": {false, ""}, "ghost": {true, "missing"}, "two": {true, "npm-local"}, "free": {false, ""},
	}
	for name, want := range cases {
		repo, internal, err := rule.Claim(context.Background(), "npm", name)
		if err != nil || internal != want.internal || repo != want.repo {
			t.Errorf("Claim(%q) = %q, %v, %v; want %q, %v", name, repo, internal, err, want.repo, want.internal)
		}
	}
	got, err := rule.InternalNames(context.Background(), "npm", []string{"@acme/ui", "free", "two"})
	if err != nil || len(got) != 2 || !got["@acme/ui"] || !got["two"] {
		t.Errorf("InternalNames = %v, %v", got, err)
	}
	if got, err := (ClaimRule{}).InternalNames(context.Background(), "npm", []string{"x"}); err != nil || len(got) != 0 {
		t.Errorf("a rule without an index: %v, %v", got, err)
	}
}

// R-VULN-010: a remote repository is matched when its upstream host is a public upstream of the format or it sets always.
func TestR_VULN_010_ScopeOfARemote(t *testing.T) {
	s := &Service{o: Options{PublicUpstreams: map[string][]string{"npm": {"registry.npmjs.org"}}}}
	v := func(host string, m vuln.MatchPublic) repoView {
		return repoView{Class: repository.Remote, Format: "npm", UpstreamHost: host, Settings: vuln.Settings{Enabled: true, MatchPublicAdvisories: m}}
	}
	cases := []struct {
		name string
		view repoView
		want Scope
	}{
		{"the public registry", v("registry.npmjs.org", vuln.MatchAuto), ScopeMatched},
		{"a mirror", v("npm-mirror.corp.example", vuln.MatchAuto), ScopeNotCovered},
		{"a mirror with always", v("npm-mirror.corp.example", vuln.MatchAlways), ScopeMatched},
		{"the public registry with never", v("registry.npmjs.org", vuln.MatchNever), ScopeNotCovered},
		{"no host", v("", vuln.MatchAuto), ScopeNotCovered},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := s.scopeOf(context.Background(), c.view, "x", ClaimRule{})
			if err != nil || got != c.want {
				t.Errorf("scope = %v, %v; want %v", got, err, c.want)
			}
		})
	}
	if hostOf("https://Registry.NPMJS.org:443/x") != "registry.npmjs.org" || hostOf("%zz") != "" {
		t.Error("hostOf")
	}
}

type recorder struct{ calls []string }

func (r *recorder) OnVersionCommitted(_ context.Context, _ meta.Tx, c vuln.CommittedVersion) error {
	r.calls = append(r.calls, "committed:"+string(c.Trigger)+":"+c.RepoKey+":"+strconv.FormatInt(c.ItemID, 10))
	return nil
}

func (r *recorder) OnVersionsRemoved(_ context.Context, _ meta.Tx, ids []int64) error {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	r.calls = append(r.calls, "removed:"+strings.Join(s, ","))
	return nil
}

func (r *recorder) EvaluateMiss(context.Context, vuln.Miss) (vuln.Block, error) {
	return vuln.Block{}, nil
}

type innerHooks struct {
	log *recorder
	err error
}

func (h innerHooks) OnCommit(context.Context, meta.Tx, items.CommitEvent) error {
	h.log.calls = append(h.log.calls, "inner:commit")
	return h.err
}

func (h innerHooks) OnItemsRemoved(context.Context, meta.Tx, items.RemovedEvent) error {
	h.log.calls = append(h.log.calls, "inner:removed")
	return h.err
}

// R-VULN-011: the evaluation of a commit runs after the format's OnCommit and only when it succeeded; the resolution of a
// removal runs before the format's OnItemsRemoved removes the version rows, once per distinct version.
func TestR_VULN_011_HooksOrder(t *testing.T) {
	log := &recorder{}
	h := Hooks(innerHooks{log: log}, log)
	ctx := context.Background()
	repo := items.RepoRef{ID: 7, Key: "npm-local", Format: "npm"}
	if err := h.OnCommit(ctx, nil, items.CommitEvent{Origin: items.OriginUpload, Repo: repo, Item: items.Ref{ID: 3}}); err != nil {
		t.Fatal(err)
	}
	if err := h.OnCommit(ctx, nil, items.CommitEvent{Origin: items.OriginFill, Repo: repo, Item: items.Ref{ID: 4}}); err != nil {
		t.Fatal(err)
	}
	removed := items.RemovedEvent{Repo: repo, Items: []items.Removed{{Version: 5}, {Version: 5}, {Version: 0}, {Version: 6}}}
	if err := h.OnItemsRemoved(ctx, nil, removed); err != nil {
		t.Fatal(err)
	}
	if err := h.OnItemsRemoved(ctx, nil, items.RemovedEvent{Repo: repo, Items: []items.Removed{{Version: 0}}}); err != nil {
		t.Fatal(err)
	}
	want := "inner:commit committed:publish:npm-local:3 inner:commit committed:fill:npm-local:4 removed:5,6 inner:removed inner:removed"
	if got := strings.Join(log.calls, " "); got != want {
		t.Errorf("calls\n got %s\nwant %s", got, want)
	}

	failing := &recorder{}
	h = Hooks(innerHooks{log: failing, err: errors.New("the format refused")}, failing)
	if err := h.OnCommit(ctx, nil, items.CommitEvent{Repo: repo, Item: items.Ref{ID: 3}}); err == nil {
		t.Error("the error of the format's hook was swallowed")
	}
	if got := strings.Join(failing.calls, " "); got != "inner:commit" {
		t.Errorf("a refused commit was evaluated: %s", got)
	}
	var inner items.Hooks = items.NoHooks{}
	if got := Hooks(inner, nil); got != inner {
		t.Error("a nil evaluator wrapped the hooks")
	}
}

type enqueuer struct{ specs []jobs.EnqueueSpec }

func (e *enqueuer) Enqueue(_ context.Context, _ meta.Tx, s jobs.EnqueueSpec) (jobs.Result, error) {
	e.specs = append(e.specs, s)
	return jobs.Result{Created: true}, nil
}

// R-VULN-002: a repository that is not scanned (a virtual one, a format without the adapter) enqueues nothing, and so does a
// change that touches no scanning input.
func TestR_VULN_002_RepositoryHookIgnoresRepositoriesTheScanningDoesNotApplyTo(t *testing.T) {
	q := &enqueuer{}
	h := NewRepositoryHook(q)
	if err := h.OnUpdated(context.Background(), nil, repository.Caller{}, repository.Repository{}, repository.Repository{}); err != nil {
		t.Fatal(err)
	}
	if len(q.specs) != 0 {
		t.Errorf("enqueued %v for a repository without an ecosystem", q.specs)
	}
	if err := h.OnDelete(context.Background(), nil, repository.Caller{}, repository.Repository{}); err != nil {
		t.Fatal(err)
	}
}
