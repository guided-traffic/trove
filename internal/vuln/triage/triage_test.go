package triage

import (
	"errors"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("not an *errs.Error: %v", err)
	}
	return e.Code
}

// TestR_VULN_013_Validate: the checks that do not depend on the finding.
func TestR_VULN_013_Validate(t *testing.T) {
	for _, c := range []struct {
		name string
		req  Request
		want string
	}{
		{"a settable status", Request{Status: vuln.StatusConfirmed}, ""},
		{"blocked is a status a person sets", Request{Status: vuln.StatusBlocked}, ""},
		{"resolved is trove's", Request{Status: vuln.StatusResolved}, errs.CodeStatusNotSettable},
		{"no such status", Request{Status: "fixed"}, errs.CodeValidation},
		{"a comment of 4000 characters", Request{HasComment: true, Comment: strings.Repeat("ä", 4000)}, ""},
		{"a comment of 4001 characters", Request{HasComment: true, Comment: strings.Repeat("ä", 4001)}, errs.CodeValidation},
		{"an unknown reason", Request{HasReason: true, DismissalReason: "meh"}, errs.CodeValidation},
		{"a known reason", Request{HasReason: true, DismissalReason: "mitigated"}, ""},
	} {
		err := c.req.Validate(false)
		if c.want == "" && err != nil || c.want != "" && (err == nil || codeOf(t, err) != c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	if err := (Request{}).Validate(true); err == nil {
		t.Error("a bulk change without a status")
	}
}

// TestR_VULN_013_ValidateBulk: a bulk change has no stored reason to fall back on.
func TestR_VULN_013_ValidateBulk(t *testing.T) {
	if err := (Request{Status: vuln.StatusDismissed}).ValidateBulk(); err == nil {
		t.Error("a bulk dismissal without a reason")
	}
	if err := (Request{Status: vuln.StatusDismissed, HasReason: true, DismissalReason: "test_only"}).ValidateBulk(); err != nil {
		t.Errorf("a bulk dismissal: %v", err)
	}
	if err := (Request{Status: vuln.StatusConfirmed, HasReason: true, DismissalReason: "test_only"}).ValidateBulk(); err == nil {
		t.Error("a reason with another status")
	}
}

// TestR_VULN_013_Target: the status and the reason a request leaves a finding with.
func TestR_VULN_013_Target(t *testing.T) {
	dismissed := store.Locked{Status: vuln.StatusDismissed, DismissalReason: "mitigated"}
	open := store.Locked{Status: vuln.StatusNeedsTriage}
	for _, c := range []struct {
		name       string
		l          store.Locked
		req        Request
		to         vuln.Status
		reason     string
		wantReason bool
		fail       bool
	}{
		{"a comment keeps the status and the reason", dismissed, Request{HasComment: true}, vuln.StatusDismissed, "mitigated", true, false},
		{"a new reason replaces the stored one", dismissed, Request{Status: vuln.StatusDismissed, HasReason: true, DismissalReason: "test_only"}, vuln.StatusDismissed, "test_only", true, false},
		{"leaving the dismissal clears the reason", dismissed, Request{Status: vuln.StatusConfirmed}, vuln.StatusConfirmed, "", false, false},
		{"a dismissal needs a reason", open, Request{Status: vuln.StatusDismissed}, vuln.StatusDismissed, "", false, true},
		{"a reason with another status", open, Request{Status: vuln.StatusConfirmed, HasReason: true, DismissalReason: "mitigated"}, vuln.StatusConfirmed, "", false, true},
		{"a null reason with another status is none", open, Request{Status: vuln.StatusConfirmed, HasReason: true}, vuln.StatusConfirmed, "", false, false},
	} {
		to, reason, err := target(c.l, c.req)
		if c.fail != (err != nil) {
			t.Errorf("%s: err = %v", c.name, err)
			continue
		}
		if !c.fail && (to != c.to || reason != c.reason) {
			t.Errorf("%s: %s %q, want %s %q", c.name, to, reason, c.to, c.reason)
		}
	}
}

// TestR_VULN_013_BlockChange: a change sets or lifts a download block under the repository's current policy, by the rule of
// the download (R-VULN-012, R-VULN-015).
func TestR_VULN_013_BlockChange(t *testing.T) {
	on := vuln.PolicyFor(vuln.DefaultSettings(), "npm", true, vuln.EnforceBlocks)
	noMalicious := on
	noMalicious.BlockMalicious = false
	off := vuln.PolicyFor(vuln.Settings{Enabled: false}, "npm", true, vuln.EnforceBlocks)
	report := vuln.PolicyFor(vuln.DefaultSettings(), "npm", true, vuln.EnforceReport)
	vulnerability := store.Locked{ID: "f1", Kind: advisory.KindVulnerability, Status: vuln.StatusConfirmed}
	malicious := store.Locked{ID: "f2", Kind: advisory.KindMalicious, Status: vuln.StatusNeedsTriage}
	blocked := store.Locked{ID: "f3", Kind: advisory.KindVulnerability, Status: vuln.StatusBlocked}
	for _, c := range []struct {
		name string
		p    vuln.Policy
		l    store.Locked
		to   vuln.Status
		want string
	}{
		{"confirmed to blocked sets", on, vulnerability, vuln.StatusBlocked, BlockSet},
		{"blocked to confirmed lifts", on, blocked, vuln.StatusConfirmed, BlockLifted},
		{"blocked to dismissed lifts", on, blocked, vuln.StatusDismissed, BlockLifted},
		{"confirmed to dismissed touches none", on, vulnerability, vuln.StatusDismissed, ""},
		{"a malicious finding dismissed lifts the malicious block", on, malicious, vuln.StatusDismissed, BlockLifted},
		{"a malicious finding confirmed again keeps it", on, malicious, vuln.StatusConfirmed, ""},
		{"a dismissed malicious finding back to needs_triage sets it", on, store.Locked{ID: "f4", Kind: advisory.KindMalicious, Status: vuln.StatusDismissed}, vuln.StatusNeedsTriage, BlockSet},
		{"without blockMalicious a malicious dismissal touches none", noMalicious, malicious, vuln.StatusDismissed, ""},
		{"a block of triage applies whatever blockMalicious says", noMalicious, blocked, vuln.StatusConfirmed, BlockLifted},
		{"a repository with the scanning off blocks nothing", off, vulnerability, vuln.StatusBlocked, ""},
		{"the mode report still names the block the policy decides", report, vulnerability, vuln.StatusBlocked, BlockSet},
	} {
		if got := blockChange(c.p, c.l, c.to); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDedupe(t *testing.T) {
	got := dedupe([]string{"a", "b", "a", "c", "b"})
	if strings.Join(got, "") != "abc" {
		t.Errorf("%v", got)
	}
}
