package sysstate

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

func TestR_OPS_024_RefusalCarriesTheMessageAndTheRetryHint(t *testing.T) {
	if err := Refusal(Maintenance{Mode: ModeNormal}); err != nil {
		t.Fatalf("normal mode refuses: %v", err)
	}
	err := Refusal(Maintenance{Mode: ModeReadOnly, Message: "Storage migration until 14:00 UTC", RetryAfterSeconds: 120})
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.Unavailable || e.Code != errs.CodeUnavailable || e.Detail != "Storage migration until 14:00 UTC" || e.RetryAfter != 2*time.Minute {
		t.Fatalf("%#v", err)
	}
	e, _ = errs.As(Refusal(Maintenance{Mode: ModeReadOnly}))
	if e.Detail != DefaultRefusal || e.RetryAfter != DefaultRetryAfterSeconds*time.Second {
		t.Errorf("defaults: %#v", e)
	}
	var nilStore *Store
	if err := nilStore.RefuseWrite(t.Context()); err != nil {
		t.Errorf("a replica without the settings refuses: %v", err)
	}
}

func TestR_OPS_025_BannerValidation(t *testing.T) {
	long := func(n int) string { return strings.Repeat("é", n) } // counted in characters, not bytes
	for name, tc := range map[string]struct {
		b       Banner
		pointer string
	}{
		"valid":             {Banner{Enabled: true, Severity: SeverityCritical, Title: long(120), Message: long(2000)}, ""},
		"disabled and bare": {Banner{Severity: SeverityInfo}, ""},
		"severity":          {Banner{Enabled: true, Severity: "loud", Message: "x"}, "/severity"},
		"title":             {Banner{Severity: SeverityInfo, Title: long(121)}, "/title"},
		"message":           {Banner{Severity: SeverityInfo, Message: long(2001)}, "/message"},
		"enabled, no text":  {Banner{Enabled: true, Severity: SeverityInfo}, "/message"},
	} {
		err := validateBanner(tc.b)
		if tc.pointer == "" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		var e *errs.Error
		if !errors.As(err, &e) || len(e.Fields) != 1 || e.Fields[0].Pointer != tc.pointer {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestR_OPS_024_MaintenanceValidation(t *testing.T) {
	ok := Maintenance{Mode: ModeReadOnly, RetryAfterSeconds: 300}
	if err := validateMaintenance(ok); err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]Maintenance{
		"mode":        {Mode: "paused", RetryAfterSeconds: 300},
		"message":     {Mode: ModeReadOnly, RetryAfterSeconds: 300, Message: strings.Repeat("m", 501)},
		"retry low":   {Mode: ModeReadOnly, RetryAfterSeconds: -1},
		"retry above": {Mode: ModeReadOnly, RetryAfterSeconds: MaxRetryAfterSeconds + 1},
	} {
		if err := validateMaintenance(m); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestR_OPS_025_BannerChangesNameTheMembers(t *testing.T) {
	a := Banner{Enabled: true, Severity: SeverityInfo, Title: "t", Message: "m"}
	if got := bannerChanges(a, a); len(got) != 0 || got == nil {
		t.Errorf("no change: %#v", got)
	}
	b := Banner{Enabled: false, Severity: SeverityWarning, Title: "t2", Message: "m2"}
	if got := strings.Join(bannerChanges(a, b), ","); got != "enabled,severity,title,message" {
		t.Errorf("%s", got)
	}
}

func TestR_OPS_024_SameMaintenanceComparesTheInstantNotTheRepresentation(t *testing.T) {
	now := time.Now()
	a := Maintenance{Mode: ModeReadOnly, CacheFill: true, Since: now.UTC(), By: "alice"}
	b := a
	b.Since = now.In(time.FixedZone("x", 3600))
	if !a.same(b) {
		t.Error("the same instant in another zone differs")
	}
	b.CacheFill = false
	if a.same(b) {
		t.Error("cacheFill is ignored")
	}
}
