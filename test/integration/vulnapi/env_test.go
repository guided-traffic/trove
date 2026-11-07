//go:build integration

// Package vulnapi tests the report and the triage of the vulnerability scanning over the management API (R-VULN-013,
// R-VULN-019 to R-VULN-021): the findings are inserted straight into the database, as the evaluation (work package B) would
// write them, so that this suite does not wait for it.
package vulnapi

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/packages/view"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// env is one installation with six callers and four repositories:
//
//	rita   an administrator
//	olga   an auditor (content:metadata-read, and the capability vulnerabilities:triage)
//	alice  reads npm-local and nuget-local, and holds no capability
//	carol  reads npm-local only for the packages @team-a/*
//	dave   holds no grant
//	bot    a service account bound to admin whose token carries the capability vulnerabilities:triage and nothing else
//
// npm-local and nuget-local are local repositories, npm-all is a virtual repository over npm-local that alice and carol read.
type env struct {
	t  *testing.T
	st *testenv.Stack
	r  *testenv.Replica

	rita, olga, alice, carol, dave, bot testenv.Person
	tokRita, tokOlga, tokAlice          string
	tokCarol, tokDave, tokBot           string
	// tokSam is the token of sam, a security administrator (the capability vulnerabilities:triage) who may read npm-local only
	// for the packages @team-a/*: a triager who reads only a part of the findings.
	tokSam string
}

func newEnv(t *testing.T, replicaEnv ...string) *env {
	t.Helper()
	return newEnvWith(t, nil, replicaEnv...)
}

// newEnvWith is newEnv with a step that seeds more state before the replica starts, which reads the repositories once.
func newEnvWith(t *testing.T, seed func(*testenv.Stack), replicaEnv ...string) *env {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "vulnapi"})
	e := &env{t: t, st: st}
	e.rita, e.olga, e.alice, e.carol, e.dave = st.User("rita"), st.User("olga"), st.User("alice"), st.User("carol"), st.User("dave")
	e.bot = st.ServiceAccount("bot")
	st.Bind("admin", e.rita)
	st.Bind("auditor", e.olga)
	st.Bind("admin", e.bot)
	st.Repository("npm-local", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	st.Repository("npm-lax", "local", "npm", `{"local":{"packageOwnership":"off"},"vulnerabilities":{"blockMalicious":false}}`)
	st.Repository("npm-mirror", "remote", "npm", `{"remote":{"url":"https://npm-mirror.corp.example/"}}`)
	st.Repository("npm-off", "local", "npm", `{"vulnerabilities":{"enabled":false}}`)
	st.Repository("npm-strict", "local", "npm", `{"vulnerabilities":{"onMissingData":"block"}}`)
	st.Repository("nuget-local", "local", "nuget", `{}`)
	st.Repository("gen-local", "local", "generic", `{}`)
	st.Repository("npm-all", "virtual", "npm", "")
	st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id) SELECT v.id, 0, m.id FROM repositories v, repositories m WHERE v.key = 'npm-all' AND m.key = 'npm-local'`)
	st.Policy("readers", []string{"read"}, []string{"npm-local", "nuget-local", "npm-all", "npm-mirror", "npm-off", "npm-strict"}, e.alice)
	st.Policy("virtual-readers", []string{"read"}, []string{"npm-all"}, e.carol)
	e.narrowed("team-a-only", "npm-local", e.carol, "@team-a/*")
	e.tokRita = st.Token(e.rita, scope.Scope{Inherit: true, Admin: true})
	e.tokOlga = st.Token(e.olga, scope.Scope{Inherit: true, System: []string{"content:metadata-read", "vulnerabilities:triage", "settings:read"}})
	e.tokAlice = st.Token(e.alice, scope.Default())
	e.tokCarol = st.Token(e.carol, scope.Default())
	e.tokDave = st.Token(e.dave, scope.Default())
	e.tokBot = st.Token(e.bot, scope.Scope{Inherit: false, System: []string{"vulnerabilities:triage"}})
	sam := st.User("sam")
	st.Bind("security-admin", sam)
	e.narrowed("team-a-sam", "npm-local", sam, "@team-a/*")
	e.tokSam = st.Token(sam, scope.Scope{Inherit: true, System: []string{"vulnerabilities:triage"}})
	if seed != nil {
		seed(st)
	}
	replicaEnv = append([]string{"TROVE_API_RATE_LIMITS_ENABLED=false"}, replicaEnv...)
	e.r = st.Start(t, testenv.ReplicaOptions{Name: "web1", Env: replicaEnv})
	return e
}

// narrowed grants read on a repository for the packages matching one include pattern.
func (e *env) narrowed(name, repo string, who testenv.Person, include string) {
	e.t.Helper()
	id := e.st.Text("INSERT INTO authz_policy (id, name) VALUES (gen_random_uuid(), $1) RETURNING id::text", name)
	e.st.Exec("INSERT INTO authz_policy_selector (policy_id, ordinal, kind, repository_id) SELECT $1::uuid, 0, 'key', id FROM repositories WHERE key = $2", id, repo)
	e.st.Exec("INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) VALUES ($1::uuid, 'package', 'include', $2)", id, include)
	e.st.Exec("INSERT INTO authz_policy_entry (policy_id, principal_id, actions) VALUES ($1::uuid, $2::uuid, ARRAY['read'])", id, who.ID)
}

// finding describes a finding to insert; the zero value is an open high finding of kind vulnerability, detected now.
type finding struct {
	Repo, Package, Version, Advisory string
	Ecosystem                        string
	Severity                         string
	Kind                             string
	Status                           string
	DismissalReason                  string
	// Detected is a timestamptz literal, "" for now; Resolved likewise, set with the status resolved.
	Detected, Resolved string
	CVSS               float64
	Fix                bool
	KEV                bool
}

var rank = map[string]int{"critical": 5, "high": 4, "medium": 3, "low": 2, "none": 1, "unknown": 0}

// addFinding inserts a finding and returns its id.
func (e *env) addFinding(f finding) string {
	e.t.Helper()
	if f.Ecosystem == "" {
		f.Ecosystem = "npm"
		if strings.HasPrefix(f.Repo, "nuget") {
			f.Ecosystem = "NuGet"
		}
	}
	if f.Severity == "" {
		f.Severity = "high"
	}
	if f.Kind == "" {
		f.Kind = "vulnerability"
	}
	if f.Status == "" {
		f.Status = "needs_triage"
	}
	if f.Version == "" {
		f.Version = "1.0.0"
	}
	if f.Advisory == "" {
		f.Advisory = "GHSA-aaaa-aaaa-0001"
	}
	if f.Detected == "" {
		f.Detected = "now()"
	}
	resolved, resolvedReason := "NULL", "NULL"
	if f.Status == "resolved" {
		resolved, resolvedReason = f.Resolved, "'no_longer_affected'"
		if resolved == "" {
			resolved = "now()"
		}
	}
	reason := "NULL"
	if f.DismissalReason != "" {
		reason = "'" + f.DismissalReason + "'"
	}
	cvss := "NULL"
	if f.CVSS > 0 {
		cvss = fmt.Sprintf("%.1f", f.CVSS)
	}
	return e.st.Text(fmt.Sprintf(`INSERT INTO vuln_finding (id, repo_id, ecosystem, package_name, version, advisory_id, kind, severity, severity_rank,
			cvss_score, known_exploited, fix_available, fixed_versions, status, dismissal_reason, resolved_reason, advisory_modified, detected_at, resolved_at)
		SELECT gen_random_uuid(), r.id, $2, $3, $4, $5, $6, $7, $8, %s, $9, $10, CASE WHEN $10 THEN ARRAY['9.9.9'] ELSE ARRAY[]::text[] END,
		       $11, %s, %s, now(), (%s)::timestamptz, (%s)::timestamptz
		  FROM repositories r WHERE r.key = $1 RETURNING id::text`, cvss, reason, resolvedReason, f.Detected, resolved),
		f.Repo, f.Ecosystem, f.Package, f.Version, f.Advisory, f.Kind, f.Severity, rank[f.Severity], f.KEV, f.Fix, f.Status)
}

// addAdvisory inserts the advisory a finding names, with aliases.
func (e *env) addAdvisory(ecosystem, id, summary, severity string, aliases ...string) {
	e.t.Helper()
	kind := "vulnerability"
	if strings.HasPrefix(id, "MAL-") {
		kind = "malicious"
	}
	e.st.Exec(`INSERT INTO vuln_advisory (ecosystem, id, canonical_id, modified, kind, aliases, summary, details, severity, severity_source,
			cvss_version, cvss_vector, cvss_score, record_sha256, cwe_ids)
		VALUES ($1, $2, $2, now(), $3, $4, $5, 'The details.', $6, 'cvss', '3.1', 'CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H', 9.8, '\x00', ARRAY['CWE-79'])`,
		ecosystem, id, kind, aliases, summary, severity)
}

// addVersion stores a package version row, so that the package views find the package.
func (e *env) addVersion(repo, pkg, version string) {
	e.t.Helper()
	e.st.Exec(`INSERT INTO packages (repo_id, name, display_name) SELECT id, $2, $2 FROM repositories WHERE key = $1 ON CONFLICT (repo_id, name) DO NOTHING`, repo, pkg)
	e.st.Exec(`INSERT INTO package_versions (package_id, version, display_version)
		SELECT p.id, $3, $3 FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE r.key = $1 AND p.name = $2`, repo, pkg, version)
}

// get sends an authenticated GET.
func (e *env) get(r *testenv.Replica, path, token string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, "GET", path, token, nil, hdr...)
}

// page is one page of a findings list.
type page struct {
	Items []struct {
		ID          string  `json:"id"`
		Project     string  `json:"project"`
		Repository  string  `json:"repository"`
		Package     string  `json:"package"`
		Version     string  `json:"version"`
		Advisory    string  `json:"advisory"`
		Severity    string  `json:"severity"`
		Status      string  `json:"status"`
		Kind        string  `json:"kind"`
		Blocked     bool    `json:"blocked"`
		BlockReason *string `json:"blockReason"`
	} `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

func (e *env) list(r *testenv.Replica, token, query string) page {
	e.t.Helper()
	a := e.get(r, "/api/v1/vulnerabilities/findings"+query, token)
	if a.Status != 200 {
		e.t.Fatalf("list %q: %d %s", query, a.Status, a.Body)
	}
	var p page
	if err := json.Unmarshal(a.Body, &p); err != nil {
		e.t.Fatalf("list %q: %v\n%s", query, err, a.Body)
	}
	return p
}

// packages returns the package names of a page, in order.
func (p page) packages() []string {
	var out []string
	for _, it := range p.Items {
		out = append(out, it.Package)
	}
	return out
}

func q(kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

// patch sends a merge patch with If-Match.
func (e *env) patch(token, id, etag, body string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(e.r, "PATCH", "/api/v1/vulnerabilities/findings/"+id, token, []byte(body), "Content-Type", "application/merge-patch+json", "If-Match", etag)
}

func (e *env) bulk(token, body string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(e.r, "POST", "/api/v1/vulnerabilities/findings/set-status", token, []byte(body), append([]string{"Content-Type", "application/json"}, hdr...)...)
}

// packageID is the package id of the management API.
func packageID(format, name string) string { return view.ID(format, name) }
