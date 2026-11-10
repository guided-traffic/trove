package vuln

import (
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

func TestStatusSets(t *testing.T) {
	open := map[Status]bool{StatusNeedsTriage: true, StatusConfirmed: true, StatusBlocked: true}
	for _, s := range Statuses() {
		if s.Open() != open[s] {
			t.Errorf("%s: Open = %t", s, s.Open())
		}
		if s.Settable() != (s != StatusResolved) {
			t.Errorf("%s: Settable = %t", s, s.Settable())
		}
	}
	if Status("x").Valid() || Status("x").Settable() {
		t.Error("an unknown status is valid")
	}
	if got := OpenStatuses(); len(got) != 3 {
		t.Errorf("OpenStatuses = %v", got)
	}
	if len(DismissalReasons()) != 5 || len(ResolvedReasons()) != 4 || len(Changes()) != 6 || len(Evaluations()) != 6 || len(Triggers()) != 6 {
		t.Error("an enumeration lost a value the schema's CHECK has")
	}
	if !DismissNotExploitable.Valid() || DismissalReason("x").Valid() || !ResolvedWithdrawn.Valid() || ResolvedReason("x").Valid() {
		t.Error("Valid disagrees with the lists")
	}
}

// R-VULN-012: the transitions a person may ask for.
func TestR_VULN_012_CheckTransition(t *testing.T) {
	cases := []struct {
		from, to Status
		code     string // "" for allowed
	}{
		{StatusNeedsTriage, StatusConfirmed, ""},
		{StatusNeedsTriage, StatusDismissed, ""},
		{StatusConfirmed, StatusNeedsTriage, ""},
		{StatusConfirmed, StatusBlocked, ""},
		{StatusConfirmed, StatusDismissed, ""},
		{StatusDismissed, StatusConfirmed, ""},
		{StatusDismissed, StatusNeedsTriage, ""},
		{StatusBlocked, StatusConfirmed, ""},
		{StatusBlocked, StatusDismissed, ""},
		{StatusBlocked, StatusBlocked, ""},
		{StatusNeedsTriage, StatusNeedsTriage, ""},
		{StatusNeedsTriage, StatusBlocked, errs.CodeFindingNotConfirmed},
		{StatusDismissed, StatusBlocked, errs.CodeFindingNotConfirmed},
		{StatusBlocked, StatusNeedsTriage, errs.CodeConflict},
		{StatusNeedsTriage, StatusResolved, errs.CodeStatusNotSettable},
		{StatusBlocked, StatusResolved, errs.CodeStatusNotSettable},
		{StatusResolved, StatusConfirmed, errs.CodeFindingResolved},
		{StatusResolved, StatusResolved, errs.CodeFindingResolved},
		{StatusConfirmed, Status("nope"), errs.CodeValidation},
	}
	for _, c := range cases {
		err := CheckTransition(c.from, c.to)
		if c.code == "" {
			if err != nil {
				t.Errorf("%s -> %s refused: %v", c.from, c.to, err)
			}
			continue
		}
		e, ok := errs.As(err)
		if !ok || e.Code != c.code {
			t.Errorf("%s -> %s: got %v, want code %s", c.from, c.to, err, c.code)
		}
	}
}

// R-VULN-002: the member's defaults, and a partial member is a complete one.
func TestR_VULN_002_SettingsDefaultsAndDecode(t *testing.T) {
	d := DefaultSettings()
	if !d.Enabled || d.MatchPublicAdvisories != MatchAuto || !d.BlockMalicious || d.OnMissingData != MissingAllow {
		t.Fatalf("defaults = %+v", d)
	}
	var s Settings
	if err := json.Unmarshal([]byte(`{"enabled": false}`), &s); err != nil {
		t.Fatal(err)
	}
	want := d
	want.Enabled = false
	if s != want {
		t.Errorf("a partial member = %+v, want %+v", s, want)
	}
	if err := json.Unmarshal([]byte(`{"enable": false}`), &s); err == nil {
		t.Error("a misspelt member was accepted")
	}
	if err := json.Unmarshal([]byte(`{"blockMalicious": "yes"}`), &s); err == nil {
		t.Error("a member of the wrong type was accepted")
	}
	b, _ := json.Marshal(d)
	if string(b) != `{"enabled":true,"matchPublicAdvisories":"auto","blockMalicious":true,"onMissingData":"allow"}` {
		t.Errorf("marshal = %s", b)
	}
}

func TestSettingsValidate(t *testing.T) {
	if got := DefaultSettings().Validate(); len(got) != 0 {
		t.Fatalf("the defaults are invalid: %v", got)
	}
	bad := Settings{MatchPublicAdvisories: "sometimes", OnMissingData: "maybe"}
	got := bad.Validate()
	if len(got) != 2 || got[0].Pointer != "/matchPublicAdvisories" || got[1].Pointer != "/onMissingData" {
		t.Errorf("Validate = %+v", got)
	}
}

func TestRescanInputs(t *testing.T) {
	a := DefaultSettings()
	b := a
	b.BlockMalicious, b.OnMissingData = false, MissingBlock
	if a.RescanInputsDiffer(b) {
		t.Error("blockMalicious and onMissingData act without a job (R-VULN-002)")
	}
	b.Enabled = false
	if !a.RescanInputsDiffer(b) {
		t.Error("enabled enqueues a rescan")
	}
	c := a
	c.MatchPublicAdvisories = MatchAlways
	if !a.RescanInputsDiffer(c) {
		t.Error("matchPublicAdvisories enqueues a rescan")
	}
}

func scanned() Policy { return PolicyFor(DefaultSettings(), "npm", true, EnforceBlocks) }

func TestPolicyFor(t *testing.T) {
	if !scanned().Enabled {
		t.Fatal("the default member of an npm repository is not scanned")
	}
	off := DefaultSettings()
	off.Enabled = false
	for name, p := range map[string]Policy{
		"member off":       PolicyFor(off, "npm", true, EnforceBlocks),
		"no adapter":       PolicyFor(DefaultSettings(), "", true, EnforceBlocks),
		"installation off": PolicyFor(DefaultSettings(), "npm", false, EnforceBlocks),
		"zero value":       {},
	} {
		if p.Enabled || p.QueryEnforce() || p.QueryBlockMalicious() {
			t.Errorf("%s: policy is enabled: %+v", name, p)
		}
	}
	if p := scanned(); !p.QueryEnforce() || !p.QueryBlockMalicious() {
		t.Error("the query parameters of a scanned repository are off")
	}
}

// R-VULN-015: what blocks, with which reason.
func TestR_VULN_015_BlockReasonOf(t *testing.T) {
	on, off := scanned(), scanned()
	off.BlockMalicious = false
	cases := []struct {
		name   string
		p      Policy
		kind   advisory.Kind
		status Status
		want   BlockReason
	}{
		{"open malicious, blockMalicious on", on, advisory.KindMalicious, StatusNeedsTriage, BlockMalicious},
		{"confirmed malicious", on, advisory.KindMalicious, StatusConfirmed, BlockMalicious},
		{"blocked malicious reports malicious", on, advisory.KindMalicious, StatusBlocked, BlockMalicious},
		{"dismissed malicious never blocks", on, advisory.KindMalicious, StatusDismissed, ""},
		{"resolved malicious never blocks", on, advisory.KindMalicious, StatusResolved, ""},
		{"open malicious, blockMalicious off", off, advisory.KindMalicious, StatusNeedsTriage, ""},
		{"blocked malicious, blockMalicious off is a triage block", off, advisory.KindMalicious, StatusBlocked, BlockTriage},
		{"blocked vulnerability", on, advisory.KindVulnerability, StatusBlocked, BlockTriage},
		{"confirmed vulnerability does not block", on, advisory.KindVulnerability, StatusConfirmed, ""},
		{"needs_triage vulnerability does not block", on, advisory.KindVulnerability, StatusNeedsTriage, ""},
		{"a disabled policy blocks nothing", Policy{}, advisory.KindMalicious, StatusBlocked, ""},
	}
	for _, c := range cases {
		got, ok := c.p.BlockReasonOf(c.kind, c.status)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: %q %t, want %q", c.name, got, ok, c.want)
		}
	}
}

// R-VULN-015, Q-VULN-5: the verdict.
func TestR_VULN_015_Verdict(t *testing.T) {
	row := BlockRow{FindingID: "f1", AdvisoryID: "MAL-2026-1", Kind: advisory.KindMalicious, Status: StatusNeedsTriage}
	complete := advisory.DataState{Complete: true}
	missing := advisory.DataState{}
	blockMissing := scanned()
	blockMissing.OnMissingData = MissingBlock
	report := scanned()
	report.Enforcement = EnforceReport

	t.Run("a malicious row refuses", func(t *testing.T) {
		b := Verdict(scanned(), row, complete)
		if !b.Refused() || b.Reason != BlockMalicious || b.FindingID != "f1" || b.AdvisoryID != "MAL-2026-1" || b.Code() != errs.CodeVulnerabilityBlocked {
			t.Errorf("%+v", b)
		}
	})
	t.Run("a triage row refuses with the reason triage", func(t *testing.T) {
		b := Verdict(scanned(), BlockRow{FindingID: "f2", AdvisoryID: "GHSA-x", Kind: advisory.KindVulnerability, Status: StatusBlocked}, complete)
		if !b.Refused() || b.Reason != BlockTriage {
			t.Errorf("%+v", b)
		}
	})
	t.Run("no row, complete data: nothing", func(t *testing.T) {
		if b := Verdict(blockMissing, BlockRow{}, complete); b.Active() {
			t.Errorf("%+v", b)
		}
	})
	t.Run("incomplete data and allow: nothing", func(t *testing.T) {
		if b := Verdict(scanned(), BlockRow{}, missing); b.Active() {
			t.Errorf("%+v", b)
		}
	})
	t.Run("incomplete data and block: vulnerability_data_missing", func(t *testing.T) {
		b := Verdict(blockMissing, BlockRow{}, missing)
		if !b.Refused() || b.Reason != BlockMissingData || b.Code() != errs.CodeVulnerabilityDataMissing || b.FindingID != "" {
			t.Errorf("%+v", b)
		}
	})
	t.Run("stale data is not missing", func(t *testing.T) {
		if b := Verdict(blockMissing, BlockRow{}, advisory.DataState{Complete: true, Stale: true}); b.Active() {
			t.Errorf("%+v", b)
		}
	})
	t.Run("a restored finding blocks while the data is missing", func(t *testing.T) {
		if b := Verdict(scanned(), row, missing); !b.Refused() || b.Reason != BlockMalicious {
			t.Errorf("%+v", b)
		}
	})
	t.Run("a row that does not block under the policy falls through", func(t *testing.T) {
		dismissed := row
		dismissed.Status = StatusDismissed
		if b := Verdict(scanned(), dismissed, complete); b.Active() {
			t.Errorf("%+v", b)
		}
	})
	t.Run("report mode counts and refuses nothing", func(t *testing.T) {
		b := Verdict(report, row, complete)
		if !b.Active() || b.Refused() || b.Enforced || b.Reason != BlockMalicious {
			t.Errorf("%+v", b)
		}
	})
	t.Run("a disabled policy blocks nothing", func(t *testing.T) {
		if b := Verdict(Policy{}, row, missing); b.Active() {
			t.Errorf("%+v", b)
		}
	})
}

func TestJobsAndTasks(t *testing.T) {
	if RescanKey(42) != "vuln-rescan:42" {
		t.Errorf("RescanKey = %s", RescanKey(42))
	}
	tasks := TaskDefaults()
	if len(tasks) != 6 {
		t.Fatalf("%d tasks", len(tasks))
	}
	want := map[string]string{
		"vuln-sync-osv": "*/15 * * * *", "vuln-sync-osv-full": "17 3 * * 0", "vuln-sync-kev": "41 */6 * * *", "vuln-findings-retention": "50 3 * * *",
		"vuln-image-scan": "*/5 * * * *", "vuln-sync-trivy-db": "23 */6 * * *",
	}
	for _, td := range tasks {
		if want[td.Name] != td.Schedule {
			t.Errorf("%s: schedule %q, want %q", td.Name, td.Schedule, want[td.Name])
		}
	}
	if tasks[0].DedupKey != DedupSyncOSV || tasks[1].DedupKey != DedupSyncOSV {
		t.Error("the two OSV kinds share the dedup key vuln-sync-osv")
	}
	var a SyncArgs
	if err := json.Unmarshal([]byte(`{"acceptShrink": true}`), &a); err != nil || !a.AcceptShrink {
		t.Errorf("SyncArgs: %+v %v", a, err)
	}
}

// R-VULN-025: the four tasks are in maintenance.tasks with the schedules TaskDefaults names.
func TestR_VULN_025_TheConfigurationHoldsTheTaskDefaults(t *testing.T) {
	l, err := config.Load(config.Options{
		File: "/c.yaml", ReadFile: func(string) ([]byte, error) { return []byte(""), nil },
		Environ: []string{"TROVE_DATABASE_HOST=db.example", "TROVE_DATABASE_NAME=trove"}, Command: config.CommandMigrate,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, td := range TaskDefaults() {
		got, ok := l.Config.Maintenance.Tasks[td.Name]
		if !ok || !got.Enabled || got.Schedule != td.Schedule {
			t.Errorf("maintenance.tasks.%s = %+v (present %t), want enabled with %q", td.Name, got, ok, td.Schedule)
		}
	}
}
