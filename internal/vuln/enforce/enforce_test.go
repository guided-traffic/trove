package enforce

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/metrics"
)

type states map[string]advisory.DataState

func (s states) State(e string) advisory.DataState { return s[e] }

type recorder struct {
	mu  sync.Mutex
	evs []audit.Event
}

func (r *recorder) RecordTx(context.Context, meta.Tx, audit.Event) error { return nil }
func (r *recorder) Record(_ context.Context, ev audit.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
}

type evaluator struct {
	block vuln.Block
	err   error
	calls []vuln.Miss
}

func (e *evaluator) OnVersionCommitted(context.Context, meta.Tx, vuln.CommittedVersion) error {
	return nil
}
func (e *evaluator) OnVersionsRemoved(context.Context, meta.Tx, []int64) error { return nil }
func (e *evaluator) EvaluateMiss(_ context.Context, m vuln.Miss) (vuln.Block, error) {
	e.calls = append(e.calls, m)
	return e.block, e.err
}

type names struct{ calls int }

func (n *names) VersionNames(context.Context, int64) (string, string, bool, error) {
	n.calls++
	return "trove-test-mal", "1.0.0", true, nil
}

type fixture struct {
	e   *Enforcer
	rec *recorder
	m   *metrics.Metrics
	now time.Time
	n   *names
	ev  *evaluator
}

func newFixture(t *testing.T, st states, enforcement vuln.Enforcement) *fixture {
	t.Helper()
	m, err := metrics.New(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{rec: &recorder{}, m: m, now: time.Unix(1_700_000_000, 0), n: &names{}, ev: &evaluator{}}
	f.e = New(Options{
		Enabled: true, Enforcement: enforcement, States: st, Evaluator: f.ev, Names: f.n, Metrics: m, Audit: f.rec,
		BaseURL: "https://trove.example.com/", Now: func() time.Time { return f.now },
	})
	return f
}

var scanned = vuln.Policy{Ecosystem: "npm", Enabled: true, Covered: true, Enforcement: vuln.EnforceBlocks, BlockMalicious: true, OnMissingData: vuln.MissingAllow}

func malRow() vuln.BlockRow {
	return vuln.BlockRow{FindingID: "01933b2a-0000", AdvisoryID: "MAL-2026-1234", Kind: advisory.KindMalicious, Status: vuln.StatusNeedsTriage}
}

// R-VULN-015: the verdict of a stored item. An item without a version, a policy that is not enabled and a replica without the
// data state block nothing.
func TestR_VULN_015_DecideAppliesThePolicyToTheLookupsRow(t *testing.T) {
	f := newFixture(t, states{"npm": {Complete: true}}, vuln.EnforceBlocks)
	if b := f.e.Decide(scanned, malRow(), 7); !b.Refused() || b.Reason != vuln.BlockMalicious || b.FindingID == "" {
		t.Fatalf("malicious open finding: %+v", b)
	}
	if b := f.e.Decide(scanned, malRow(), 0); b.Active() {
		t.Errorf("an item that belongs to no version is blocked: %+v", b)
	}
	if b := f.e.Decide(vuln.Policy{}, malRow(), 7); b.Active() {
		t.Errorf("an unscanned repository blocks: %+v", b)
	}
	dismissed := malRow()
	dismissed.Status = vuln.StatusDismissed
	if b := f.e.Decide(scanned, dismissed, 7); b.Active() {
		t.Errorf("a dismissed finding blocks: %+v", b)
	}
	if b := (*Enforcer)(nil).Decide(scanned, malRow(), 7); b.Active() {
		t.Errorf("a nil enforcer blocks: %+v", b)
	}
	if b := New(Options{Enabled: true}).Decide(scanned, malRow(), 7); b.Active() {
		t.Errorf("an enforcer without the data state blocks: %+v", b)
	}
	// "Not evaluated yet": onMissingData block refuses while the ecosystem's data is not complete.
	block := scanned
	block.OnMissingData = vuln.MissingBlock
	if b := f.e.Decide(block, vuln.BlockRow{}, 7); b.Active() {
		t.Errorf("complete data counts as missing: %+v", b)
	}
	f = newFixture(t, states{"npm": {}}, vuln.EnforceBlocks)
	if b := f.e.Decide(block, vuln.BlockRow{}, 7); b.Reason != vuln.BlockMissingData || b.Code() != errs.CodeVulnerabilityDataMissing {
		t.Errorf("missing data: %+v", b)
	}
}

// R-VULN-016: the refusal is a 403 with the code, the text that names the package, version, repository, reason and the UI link,
// the advisory and finding as members of the problem, and no-store.
func TestR_VULN_016_TheRefusalNamesWhatBlockedAndIsNeverCached(t *testing.T) {
	f := newFixture(t, states{"npm": {Complete: true}}, vuln.EnforceBlocks)
	err := f.e.Refuse(context.Background(), Download{
		Addressed: "npm-all", Store: "npm-remote", RepoID: "5", Format: "npm", Path: "trove-test-mal/-/trove-test-mal-1.0.0.tgz",
		VersionID: 9, Stored: true, Block: f.e.Decide(scanned, malRow(), 9),
		Source: audit.Source{ClientAddr: "10.0.0.1"},
	})
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.Forbidden || e.Code != errs.CodeVulnerabilityBlocked {
		t.Fatalf("refusal = %v", err)
	}
	want := "trove blocked trove-test-mal@1.0.0 in npm-all: malicious package (MAL-2026-1234). Details: https://trove.example.com/ui/vulnerabilities/findings/01933b2a-0000"
	if e.Detail != want {
		t.Errorf("text = %q\nwant   %q", e.Detail, want)
	}
	if strings.Contains(e.Detail, "npm-remote") {
		t.Error("the text names the member that stores the version")
	}
	if e.Headers["Cache-Control"] != "no-store" {
		t.Errorf("headers = %v", e.Headers)
	}
	if e.Extensions["advisory"] != "MAL-2026-1234" || e.Extensions["findingId"] != "01933b2a-0000" {
		t.Errorf("extensions = %v", e.Extensions)
	}
	if n := counter(t, f.m.DownloadsBlocked.WithLabelValues("npm", "malicious", "enforce")); n != 1 {
		t.Errorf("trove_vuln_downloads_blocked_total{enforce} = %v", n)
	}
}

// R-VULN-016: the dedup of R-OPS-015, once a minute per repository, version and client address; every request is counted.
func TestR_VULN_016_TheAuditEventIsDeduplicatedPerMinuteRepositoryVersionAndClient(t *testing.T) {
	f := newFixture(t, states{"npm": {Complete: true}}, vuln.EnforceBlocks)
	d := func(addr string) Download {
		return Download{
			Addressed: "npm-remote", Store: "npm-remote", Format: "npm", Path: "p", VersionID: 9, Stored: true,
			Block: f.e.Decide(scanned, malRow(), 9), Source: audit.Source{ClientAddr: addr},
		}
	}
	for range 5 {
		_ = f.e.Refuse(context.Background(), d("10.0.0.1"))
	}
	if len(f.rec.evs) != 1 {
		t.Fatalf("%d events for five refusals of one client in one minute, want 1", len(f.rec.evs))
	}
	_ = f.e.Refuse(context.Background(), d("10.0.0.2"))
	if len(f.rec.evs) != 2 {
		t.Fatalf("another client address has no event of its own: %d", len(f.rec.evs))
	}
	f.now = f.now.Add(61 * time.Second)
	_ = f.e.Refuse(context.Background(), d("10.0.0.1"))
	if len(f.rec.evs) != 3 {
		t.Fatalf("the window did not reopen: %d", len(f.rec.evs))
	}
	if n := counter(t, f.m.DownloadsBlocked.WithLabelValues("npm", "malicious", "enforce")); n != 7 {
		t.Errorf("counter = %v, want one per request (7)", n)
	}
	var det map[string]any
	ev := f.rec.evs[0]
	if err := json.Unmarshal(ev.Details, &det); err != nil {
		t.Fatal(err)
	}
	if ev.Type != audit.EventVulnDownloadBlocked || ev.Repository != "npm-remote" || det["package"] != "trove-test-mal" || det["version"] != "1.0.0" ||
		det["reason"] != "malicious" || det["enforced"] != true || det["stored"] != true || det["advisory"] != "MAL-2026-1234" {
		t.Errorf("event %+v details %v", ev, det)
	}
}

// The name of a stored version is read once and kept for the window: a client that hammers a blocked version costs no query
// per request.
func TestR_VULN_016_TheVersionIsNamedOncePerWindow(t *testing.T) {
	f := newFixture(t, states{"npm": {Complete: true}}, vuln.EnforceBlocks)
	for range 4 {
		_ = f.e.Refuse(context.Background(), Download{
			Addressed: "npm-remote", Store: "npm-remote", Format: "npm", Path: "p", VersionID: 9, Stored: true, Block: f.e.Decide(scanned, malRow(), 9),
		})
	}
	if f.n.calls != 1 {
		t.Errorf("the version was named %d times, want once", f.n.calls)
	}
}

// R-VULN-015 "Report mode": the block is counted with mode report and recorded with enforced false, and nothing is refused.
func TestR_VULN_015_ReportModeCountsAndRefusesNothing(t *testing.T) {
	f := newFixture(t, states{"npm": {Complete: true}}, vuln.EnforceReport)
	p := scanned
	p.Enforcement = vuln.EnforceReport
	err := f.e.Refuse(context.Background(), Download{
		Addressed: "npm-remote", Store: "npm-remote", Format: "npm", Path: "p", VersionID: 9, Stored: true, Block: f.e.Decide(p, malRow(), 9),
	})
	if err != nil {
		t.Fatalf("report mode refuses: %v", err)
	}
	if n := counter(t, f.m.DownloadsBlocked.WithLabelValues("npm", "malicious", "report")); n != 1 {
		t.Errorf("trove_vuln_downloads_blocked_total{mode=report} = %v", n)
	}
	if len(f.rec.evs) != 1 || !strings.Contains(string(f.rec.evs[0].Details), `"enforced":false`) {
		t.Errorf("events = %+v", f.rec.evs)
	}
}

// R-VULN-015 "Remote miss": the evaluation names the block, the installation's mode decides whether it is enforced, and a miss
// without a finding links the advisory page.
func TestR_VULN_015_TheMissGateAppliesTheEvaluationAndTheMissingDataRule(t *testing.T) {
	f := newFixture(t, states{"npm": {Complete: true}}, vuln.EnforceBlocks)
	m := Miss{Package: "trove-test-mal", Version: "1.0.0", Path: "trove-test-mal/-/trove-test-mal-1.0.0.tgz"}
	m.Repo.Name = qr("npm-remote")
	if err := f.e.gate(context.Background(), scanned, m); err != nil {
		t.Fatalf("nothing blocks: %v", err)
	}
	f.ev.block = vuln.Block{Reason: vuln.BlockMalicious, AdvisoryID: "MAL-2026-1234"}
	err := f.e.gate(context.Background(), scanned, m)
	e, ok := errs.As(err)
	if !ok || e.Code != errs.CodeVulnerabilityBlocked {
		t.Fatalf("gate = %v", err)
	}
	if !strings.HasSuffix(e.Detail, "Details: https://trove.example.com/ui/vulnerabilities/advisories/npm/MAL-2026-1234") ||
		!strings.Contains(e.Detail, "trove-test-mal@1.0.0 in it/npm-remote: malicious package (MAL-2026-1234)") {
		t.Errorf("text = %q", e.Detail)
	}
	if _, has := e.Extensions["findingId"]; has {
		t.Error("a miss without a finding names a finding")
	}
	if len(f.ev.calls) != 2 || f.ev.calls[0].Repo.Key != "npm-remote" || f.ev.calls[0].Name != "trove-test-mal" || f.ev.calls[0].Version != "1.0.0" {
		t.Errorf("EvaluateMiss calls = %+v", f.ev.calls)
	}
	// A block the evaluation found and a report-mode installation: counted, not refused.
	rp := scanned
	rp.Enforcement = vuln.EnforceReport
	if err := f.e.gate(context.Background(), rp, m); err != nil {
		t.Errorf("report mode refuses a miss: %v", err)
	}
	// The data is not complete and onMissingData is block.
	f = newFixture(t, states{}, vuln.EnforceBlocks)
	bp := scanned
	bp.OnMissingData = vuln.MissingBlock
	err = f.e.gate(context.Background(), bp, m)
	if e, ok := errs.As(err); !ok || e.Code != errs.CodeVulnerabilityDataMissing {
		t.Fatalf("missing data gate = %v", err)
	}
	// An error of the evaluation is the miss's error: nothing is fetched on a verdict that could not be read.
	f.ev.err = errors.New("db down")
	if err := f.e.gate(context.Background(), scanned, m); err == nil {
		t.Error("an evaluation error lets the fetch go on")
	}
	// A miss that maps to no version is no download of a version.
	if err := f.e.GateMiss(context.Background(), Miss{Path: "p"}); err != nil {
		t.Errorf("a miss without a version is gated: %v", err)
	}
}

// A package name that carries a control character cannot split the line of a NuGet body or a header.
func TestR_VULN_016_TheTextIsOneLine(t *testing.T) {
	f := newFixture(t, states{"npm": {Complete: true}}, vuln.EnforceBlocks)
	err := f.e.Refuse(context.Background(), Download{
		Addressed: "npm-remote", Store: "npm-remote", Format: "npm", Package: "evil\r\nX-Injected: 1", Version: "1.0.0", Block: f.e.Decide(scanned, malRow(), 9),
	})
	e, _ := errs.As(err)
	if e == nil || strings.ContainsAny(e.Detail, "\r\n") {
		t.Fatalf("text = %v", err)
	}
}

// counter reads a counter's value.
func counter(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}
