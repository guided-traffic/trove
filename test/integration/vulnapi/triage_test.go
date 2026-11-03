//go:build integration

package vulnapi

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// auditEvents returns the details of the audit events of an action, oldest first. Transactional events are in the table when
// the answer arrives.
func (e *env) auditEvents(action string) []map[string]any {
	e.t.Helper()
	var out []map[string]any
	e.st.Query(`SELECT details::text FROM audit_event WHERE action = $1 ORDER BY occurred_at, id`, func(r pgx.Rows) error {
		var raw string
		if err := r.Scan(&raw); err != nil {
			return err
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return err
		}
		out = append(out, m)
		return nil
	}, action)
	return out
}

// TestR_VULN_013_Patch: the capability, the If-Match, the transitions and the block of a triage.
func TestR_VULN_013_Patch(t *testing.T) {
	e := newEnv(t)
	id := e.addFinding(finding{Repo: "npm-local", Package: "lodash", Version: "4.17.20"})
	path := "/api/v1/vulnerabilities/findings/" + id

	t.Run("R-VULN-013 a reader without the capability gets 403 on the PATCH, an administrator succeeds", func(t *testing.T) {
		if a := e.patch(e.tokAlice, id, `"1"`, `{"status":"confirmed"}`); a.Status != 403 {
			t.Fatalf("alice: %d %s", a.Status, a.Body)
		}
		a := e.patch(e.tokRita, id, `"1"`, `{"status":"confirmed","comment":"accepted"}`)
		if a.Status != 200 {
			t.Fatalf("rita: %d %s", a.Status, a.Body)
		}
		if got := a.Header.Get("ETag"); got != `"2"` {
			t.Errorf("ETag after the change = %q", got)
		}
		var d struct {
			Finding struct {
				Status string `json:"status"`
			} `json:"finding"`
			Comment string `json:"comment"`
			History []struct {
				Change     string `json:"change"`
				FromStatus string `json:"fromStatus"`
				ToStatus   string `json:"toStatus"`
				Actor      *struct {
					Name string `json:"name"`
				} `json:"actor"`
			} `json:"history"`
		}
		if err := json.Unmarshal(a.Body, &d); err != nil {
			t.Fatal(err)
		}
		if d.Finding.Status != "confirmed" || d.Comment != "accepted" {
			t.Errorf("after the change: %+v", d)
		}
		if n := len(d.History); n != 1 || d.History[0].Change != "status_changed" || d.History[0].FromStatus != "needs_triage" ||
			d.History[0].ToStatus != "confirmed" || d.History[0].Actor == nil || d.History[0].Actor.Name != "rita" {
			t.Errorf("history = %+v", d.History)
		}
	})

	t.Run("R-VULN-013 a second PATCH with the old ETag answers 412", func(t *testing.T) {
		a := e.patch(e.tokRita, id, `"1"`, `{"status":"needs_triage"}`)
		if a.Status != 412 || a.Header.Get("Content-Type") == "" {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		if got, _ := a.JSON(t)["currentEtag"].(string); got != `"2"` {
			t.Errorf("currentEtag = %q", got)
		}
	})

	t.Run("R-VULN-013 a PATCH without If-Match answers 428", func(t *testing.T) {
		a := e.st.Do(e.r, "PATCH", path, e.tokRita, []byte(`{"status":"confirmed"}`), "Content-Type", "application/merge-patch+json")
		if a.Status != 428 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})

	t.Run("R-VULN-012 resolved is not settable and blocked needs a confirmed finding", func(t *testing.T) {
		if a := e.patch(e.tokRita, id, `*`, `{"status":"resolved"}`); a.Status != 422 || a.Code(t) != "status_not_settable" {
			t.Errorf("resolved: %d %s", a.Status, a.Body)
		}
		other := e.addFinding(finding{Repo: "npm-local", Package: "left-pad"})
		if a := e.patch(e.tokRita, other, `*`, `{"status":"blocked"}`); a.Status != 409 || a.Code(t) != "finding_not_confirmed" {
			t.Errorf("blocked on needs_triage: %d %s", a.Status, a.Body)
		}
		gone := e.addFinding(finding{Repo: "npm-local", Package: "gone", Status: "resolved"})
		if a := e.patch(e.tokRita, gone, `*`, `{"status":"confirmed"}`); a.Status != 409 || a.Code(t) != "finding_resolved" {
			t.Errorf("a resolved finding: %d %s", a.Status, a.Body)
		}
	})

	t.Run("R-VULN-013 a dismissal needs a reason, and a reason goes with a dismissal only", func(t *testing.T) {
		other := e.addFinding(finding{Repo: "npm-local", Package: "dismiss-me"})
		if a := e.patch(e.tokRita, other, `*`, `{"status":"dismissed"}`); a.Status != 422 {
			t.Errorf("dismissed without a reason: %d %s", a.Status, a.Body)
		}
		if a := e.patch(e.tokRita, other, `*`, `{"status":"confirmed","dismissalReason":"mitigated"}`); a.Status != 422 {
			t.Errorf("a reason without a dismissal: %d %s", a.Status, a.Body)
		}
		if a := e.patch(e.tokRita, other, `*`, `{"status":"dismissed","dismissalReason":"vibes"}`); a.Status != 422 {
			t.Errorf("an unknown reason: %d %s", a.Status, a.Body)
		}
		a := e.patch(e.tokRita, other, `*`, `{"status":"dismissed","dismissalReason":"not_exploitable","comment":"not reachable"}`)
		if a.Status != 200 || !strings.Contains(string(a.Body), `"dismissalReason":"not_exploitable"`) {
			t.Fatalf("dismissed: %d %s", a.Status, a.Body)
		}
		if a := e.patch(e.tokRita, other, `*`, `{"comment":"`+strings.Repeat("x", 4001)+`"}`); a.Status != 422 {
			t.Errorf("a comment of 4001 characters: %d", a.Status)
		}
		// Leaving the dismissal clears the reason.
		a = e.patch(e.tokRita, other, `*`, `{"status":"needs_triage"}`)
		if a.Status != 200 || strings.Contains(string(a.Body), `"dismissalReason":"not_exploitable"`) {
			t.Errorf("reopened: %d %s", a.Status, a.Body)
		}
		if a := e.patch(e.tokRita, other, `*`, `{"nope":1}`); a.Status != 400 {
			t.Errorf("an unknown member: %d", a.Status)
		}
	})

	t.Run("R-VULN-013 the block is set and lifted, with block set and lifted in the audit events", func(t *testing.T) {
		blk := e.addFinding(finding{Repo: "npm-local", Package: "evil", Status: "confirmed"})
		a := e.patch(e.tokRita, blk, `"1"`, `{"status":"blocked","comment":"confirmed by the security team"}`)
		if a.Status != 200 || !strings.Contains(string(a.Body), `"blocked":true`) || !strings.Contains(string(a.Body), `"blockReason":"triage"`) {
			t.Fatalf("set: %d %s", a.Status, a.Body)
		}
		var d struct {
			BlockedBy *struct {
				Principal *struct {
					Name string `json:"name"`
				} `json:"principal"`
				Comment string `json:"comment"`
			} `json:"blockedBy"`
		}
		_ = json.Unmarshal(a.Body, &d)
		if d.BlockedBy == nil || d.BlockedBy.Principal == nil || d.BlockedBy.Principal.Name != "rita" || d.BlockedBy.Comment != "confirmed by the security team" {
			t.Errorf("blockedBy = %+v", d.BlockedBy)
		}
		if a := e.patch(e.tokRita, blk, `*`, `{"status":"confirmed"}`); a.Status != 200 || !strings.Contains(string(a.Body), `"blocked":false`) {
			t.Fatalf("lift: %d %s", a.Status, a.Body)
		}
		// blocked -> needs_triage is no transition.
		e.patch(e.tokRita, blk, `*`, `{"status":"blocked"}`)
		if a := e.patch(e.tokRita, blk, `*`, `{"status":"needs_triage"}`); a.Status != 409 {
			t.Errorf("blocked to needs_triage: %d %s", a.Status, a.Body)
		}
		var blocks []string
		for _, ev := range e.auditEvents("vulnerability.finding.status_changed") {
			if ev["finding"] == blk {
				b, _ := ev["block"].(string)
				blocks = append(blocks, ev["from"].(string)+">"+ev["to"].(string)+":"+b)
			}
		}
		want := []string{"confirmed>blocked:set", "blocked>confirmed:lifted", "confirmed>blocked:set"}
		if !slices.Equal(blocks, want) {
			t.Errorf("events = %v, want %v", blocks, want)
		}
	})

	t.Run("R-OPS-014 only a change that sets a block is journaled, and the Bastion feed reads it", func(t *testing.T) {
		before := e.st.Number(`SELECT count(*) FROM revocation_outbox WHERE event = 'vulnerability.finding.status_changed'`)
		other := e.addFinding(finding{Repo: "npm-local", Package: "journal", Status: "confirmed"})
		e.patch(e.tokRita, other, `*`, `{"status":"blocked"}`)
		e.patch(e.tokRita, other, `*`, `{"status":"confirmed"}`)
		e.patch(e.tokRita, other, `*`, `{"comment":"again"}`)
		if got := e.st.Number(`SELECT count(*) FROM revocation_outbox WHERE event = 'vulnerability.finding.status_changed'`) - before; got != 1 {
			t.Fatalf("journal rows of three changes (set, lift, comment) = %d, want 1", got)
		}
		entry := e.st.Text(`SELECT entry::text FROM revocation_outbox WHERE event = 'vulnerability.finding.status_changed' ORDER BY at DESC, id DESC LIMIT 1`)
		if !strings.Contains(entry, `"block": "set"`) && !strings.Contains(entry, `"block":"set"`) {
			t.Errorf("entry = %s", entry)
		}
	})

	t.Run("R-VULN-013 an unchanged status is 200 and writes nothing", func(t *testing.T) {
		same := e.addFinding(finding{Repo: "npm-local", Package: "same", Status: "confirmed"})
		events := len(e.auditEvents("vulnerability.finding.status_changed"))
		a := e.patch(e.tokRita, same, `"1"`, `{"status":"confirmed"}`)
		if a.Status != 200 || a.Header.Get("ETag") != `"1"` || len(e.auditEvents("vulnerability.finding.status_changed")) != events {
			t.Errorf("%d etag %q", a.Status, a.Header.Get("ETag"))
		}
	})
}

// TestR_VULN_013_Bulk: three readable and one unreadable id, one bulk id, the results, the limits and the idempotency key.
func TestR_VULN_013_Bulk(t *testing.T) {
	e := newEnv(t)
	var ids []string
	for _, p := range []string{"@team-a/one", "@team-a/two", "@team-a/three"} {
		ids = append(ids, e.addFinding(finding{Repo: "npm-local", Package: p}))
	}
	unreadable := e.addFinding(finding{Repo: "npm-local", Package: "@team-b/secret"})
	type result struct {
		Results []struct {
			ID      string  `json:"id"`
			Outcome string  `json:"outcome"`
			Status  int     `json:"status"`
			Code    *string `json:"code"`
		} `json:"results"`
		BulkID string `json:"bulkId"`
	}
	body := func(ids []string, rest string) string {
		b, _ := json.Marshal(ids)
		return `{"ids":` + string(b) + rest + `}`
	}

	t.Run("R-VULN-013 a bulk dismissal of 3 readable and 1 unreadable id answers three changed and one 404", func(t *testing.T) {
		// sam holds the capability and reads @team-a/* in npm-local only.
		all := append(slices.Clone(ids), unreadable)
		a := e.bulk(e.tokSam, body(all, `,"status":"dismissed","dismissalReason":"test_only"`))
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		var r result
		_ = json.Unmarshal(a.Body, &r)
		if len(r.Results) != 4 || r.BulkID == "" {
			t.Fatalf("result = %+v", r)
		}
		changed, notFound := 0, 0
		for _, x := range r.Results {
			switch {
			case x.Outcome == "changed" && x.Status == 200:
				changed++
			case x.ID == unreadable && x.Outcome == "failed" && x.Status == 404:
				notFound++
			}
		}
		if changed != 3 || notFound != 1 {
			t.Errorf("changed %d, 404 %d: %+v", changed, notFound, r.Results)
		}
		if st := e.st.Text(`SELECT status FROM vuln_finding WHERE id = $1::uuid`, unreadable); st != "needs_triage" {
			t.Errorf("the unreadable finding is %s", st)
		}
		bulkIDs := map[string]bool{}
		n := 0
		for _, ev := range e.auditEvents("vulnerability.finding.status_changed") {
			if b, _ := ev["bulk"].(string); b != "" {
				bulkIDs[b] = true
				n++
			}
		}
		if n != 3 || len(bulkIDs) != 1 || !bulkIDs[r.BulkID] {
			t.Errorf("%d events, bulk ids %v, want 3 events with %s", n, bulkIDs, r.BulkID)
		}
	})

	t.Run("R-VULN-013 a bulk blocked over a confirmed and a needs_triage finding answers changed and 409 finding_not_confirmed", func(t *testing.T) {
		c := e.addFinding(finding{Repo: "npm-local", Package: "c", Status: "confirmed"})
		n := e.addFinding(finding{Repo: "npm-local", Package: "n"})
		r := e.addFinding(finding{Repo: "npm-local", Package: "r", Status: "resolved"})
		a := e.bulk(e.tokRita, body([]string{c, n, r, c}, `,"status":"blocked"`))
		var res result
		_ = json.Unmarshal(a.Body, &res)
		if a.Status != 200 || len(res.Results) != 3 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		want := map[string][2]any{c: {"changed", 200}, n: {"failed", 409}, r: {"failed", 409}}
		for _, x := range res.Results {
			if w := want[x.ID]; x.Outcome != w[0] || x.Status != w[1] {
				t.Errorf("%s: %+v, want %v", x.ID, x, w)
			}
			if x.ID == n && (x.Code == nil || *x.Code != "finding_not_confirmed") || x.ID == r && (x.Code == nil || *x.Code != "finding_resolved") {
				t.Errorf("%s: code %v", x.ID, x.Code)
			}
		}
		// The same status again is unchanged.
		a = e.bulk(e.tokRita, body([]string{c}, `,"status":"blocked"`))
		_ = json.Unmarshal(a.Body, &res)
		if len(res.Results) != 1 || res.Results[0].Outcome != "unchanged" {
			t.Errorf("repeat: %s", a.Body)
		}
	})

	t.Run("R-VULN-013 a batch over 500 ids and an empty batch answer 422, a bad id too", func(t *testing.T) {
		many := make([]string, 501)
		for i := range many {
			many[i] = ids[0]
		}
		fresh := []string{e.addFinding(finding{Repo: "npm-local", Package: "fresh"})}
		for name, b := range map[string]string{
			"501 ids": body(many, `,"status":"confirmed"`), "no ids": body(nil, `,"status":"confirmed"`),
			"a bad id": body([]string{"nope"}, `,"status":"confirmed"`), "resolved": body(ids[:1], `,"status":"resolved"`),
			"no status": body(ids[:1], ``), "dismissed without a reason": body(fresh, `,"status":"dismissed"`),
		} {
			if a := e.bulk(e.tokRita, b); a.Status != 422 {
				t.Errorf("%s: %d %s", name, a.Status, a.Body)
			}
		}
		if a := e.bulk(e.tokAlice, body(ids[:1], `,"status":"confirmed"`)); a.Status != 403 {
			t.Errorf("without the capability: %d", a.Status)
		}
	})

	t.Run("R-API-014 an idempotency key replays the first answer and changes nothing twice", func(t *testing.T) {
		f := e.addFinding(finding{Repo: "npm-local", Package: "idem"})
		b := body([]string{f}, `,"status":"confirmed"`)
		first := e.bulk(e.tokRita, b, "Idempotency-Key", "bulk-1")
		second := e.bulk(e.tokRita, b, "Idempotency-Key", "bulk-1")
		if first.Status != 200 || second.Status != 200 || second.Header.Get("Idempotency-Replayed") != "true" || string(first.Body) != string(second.Body) {
			t.Fatalf("first %d %s, second %d %s", first.Status, first.Body, second.Status, second.Body)
		}
		if n := e.st.Number(`SELECT count(*) FROM vuln_finding_event WHERE finding_id = $1::uuid`, f); n != 1 {
			t.Errorf("history rows = %d", n)
		}
	})
}
