//go:build integration

package vulnaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The client audits of the vulnerability scanning (R-VULN-017, R-VULN-018) against the integration stack: advisory rows
// are inserted directly, as the sync of the advisory data would write them, and the state of the ecosystem's data is set
// the same way. The client runs (npm, pnpm, yarn, dotnet) are the end-to-end tier's.

const (
	npmLocal    = "npm-local"
	npmVendored = "npm-vendored"
	npmRemote   = "npm-remote"
	npmAll      = "npm-all"
	npmOff      = "npm-off"
	npmCustomer = "npm-customers"

	nugetLocal  = "nuget-local"
	nugetRemote = "nuget-remote"
	nugetAll    = "nuget-all"
	nugetOff    = "nuget-off"

	feedBase = "http://" + testenv.DefaultHost
)

// env is one installation with an administrator, a reader and a person without any grant.
type env struct {
	t  *testing.T
	st *testenv.Stack

	tokAdmin, tokReader, tokNobody string
}

// remoteDoc is a remote repository's document: the upstream is never contacted by an audit in mode local.
func remoteDoc(format string, extra string) string {
	doc := `{"remote":{"url":"https://upstream.invalid/","extraHosts":[],"authMode":"none","username":"","password":null,` +
		`"clientCertificate":null,"headers":[],"forwardAuthOnRedirect":false,"outboundProxy":{"mode":"direct"},` +
		`"connectTimeoutMs":5000,"readTimeoutMs":15000,"sourceNetwork":null,"cacheFillAction":"read",` +
		`"cache":{"storeArtifacts":true,"metadataTtlSeconds":7200,"metadataRefreshTimeoutSeconds":10,"notFoundTtlSeconds":60,` +
		`"maxStaleSeconds":0,"unusedRetentionHours":0},` +
		`"integrity":{"checksumPolicy":"verify-if-available","blockUnexpectedContentTypes":true,"allowedContentTypes":[]},` +
		`"availability":{"mode":"online","failureThreshold":3,"circuitOpenSeconds":30},` +
		`"limits":{"maxArtifactBytes":null,"maxMetadataBytes":null,"maxConcurrentFetches":16}}`
	if extra != "" {
		doc += "," + extra
	}
	return doc + "}"
}

// newEnv creates the installation: npm and NuGet repositories of the three classes, the claims of npm-local (@acme/*) and
// nuget-local (contoso.util), and a repository that declares to hold public packages (npm-vendored, @vendored/*).
func newEnv(t *testing.T) *env {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "vulnaudit"})
	e := &env{t: t, st: st}
	rita, reader, nobody := st.User("rita"), st.User("reader"), st.User("nobody")
	st.Bind("admin", rita)
	e.tokAdmin = st.Token(rita, scope.Scope{Inherit: true, Admin: true})
	e.tokReader = st.Token(reader, scope.Default())
	e.tokNobody = st.Token(nobody, scope.Default())

	st.Repository(npmLocal, "local", "npm", `{}`)
	st.Repository(npmVendored, "local", "npm", `{"vulnerabilities":{"matchPublicAdvisories":"always"}}`)
	st.Repository(npmRemote, "remote", "npm", remoteDoc("npm", ""))
	st.Repository(npmOff, "remote", "npm", remoteDoc("npm", `"npm":{"audit":{"mode":"disabled"}}`))
	st.Repository(npmAll, "virtual", "npm", `{}`)
	st.Repository(nugetLocal, "local", "nuget", `{}`)
	st.Repository(nugetRemote, "remote", "nuget", remoteDoc("nuget", ""))
	st.Repository(nugetOff, "remote", "nuget", remoteDoc("nuget", `"nuget":{"vulnerabilityInfo":"off"}`))
	st.Repository(nugetAll, "virtual", "nuget", `{}`)
	for _, v := range [][2]string{{npmAll, npmLocal}, {npmAll, npmRemote}, {nugetAll, nugetLocal}, {nugetAll, nugetRemote}} {
		st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
			SELECT v.id, (SELECT count(*) FROM virtual_members WHERE virtual_id = v.id), m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $2`, v[0], v[1])
	}
	e.claim(npmLocal, "@acme/*")
	e.claim(npmVendored, "@vendored/*")
	e.claim(nugetLocal, "contoso.util")
	all := []string{npmLocal, npmVendored, npmRemote, npmOff, npmAll, nugetLocal, nugetRemote, nugetOff, nugetAll}
	st.Policy("readers", []string{"read"}, all, reader)
	return e
}

// claim inserts a namespace claim of a local repository.
func (e *env) claim(repo, pattern string) {
	e.t.Helper()
	e.st.Exec(`INSERT INTO namespace_claims (repo_id, pattern, source, scope) SELECT id, $2, 'manual', 'members' FROM repositories WHERE key = $1`, repo, pattern)
}

// replica starts a web replica.
func (e *env) replica(name string, env ...string) *testenv.Replica {
	e.t.Helper()
	return e.st.Start(e.t, testenv.ReplicaOptions{Name: name, Env: env})
}

// aff is one affected entry of a test advisory: ranges as the OSV JSON writes them.
type aff struct {
	name     string
	ranges   string // JSON of vuln_affected.ranges
	versions []string
	every    bool
}

// rangeJSON is the ranges member of one range of the given type.
func rangeJSON(typ string, events ...string) string {
	return fmt.Sprintf(`[{"type":%q,"events":[%s]}]`, typ, strings.Join(events, ","))
}

func ev(kind, v string) string { return fmt.Sprintf(`{%q:%q}`, kind, v) }

// advisory inserts a record of the ecosystem with its affected entries.
func (e *env) advisory(eco, id, canonical, kind, severity, summary string, aliases []string, withdrawn bool, entries ...aff) {
	e.t.Helper()
	if aliases == nil {
		aliases = []string{}
	}
	wd := "NULL"
	if withdrawn {
		wd = "now()"
	}
	e.st.Exec(`INSERT INTO vuln_advisory (ecosystem, id, canonical_id, modified, withdrawn, kind, aliases, summary, severity, severity_source, record_sha256)
		VALUES ($1, $2, $3, now(), `+wd+`, $4, $5, $6, $7, 'advisory', '\x00')`, eco, id, canonical, kind, aliases, summary, severity)
	for i, a := range entries {
		ranges := a.ranges
		if ranges == "" {
			ranges = "[]"
		}
		if a.versions == nil {
			a.versions = []string{}
		}
		e.st.Exec(`INSERT INTO vuln_affected (ecosystem, advisory_id, ord, name, ranges, versions, every_version)
			VALUES ($1, $2, $3, $4, $5::text::jsonb, $6, $7)`, eco, id, i, a.name, ranges, a.versions, a.every)
	}
}

// data sets the state of the ecosystem's data: complete at the epoch, synced at the age before now.
func (e *env) data(eco string, complete bool, epoch int64, ageHours int) {
	e.t.Helper()
	state := "empty"
	if complete {
		state = "complete"
	}
	e.st.Exec(`INSERT INTO vuln_source_state (source, ecosystem, state, data_epoch, synced_at)
		VALUES ('osv', $1, $2, $3, now() - make_interval(hours => $4))
		ON CONFLICT (source, ecosystem) DO UPDATE SET state = $2, data_epoch = $3, synced_at = now() - make_interval(hours => $4)`, eco, state, epoch, ageHours)
}

// changed publishes a change of an area as a writer's transaction does (R-ARCH-018 step 1) and has every replica apply it.
func (e *env) changed(area, payload string, reps ...*testenv.Replica) {
	e.t.Helper()
	epoch := e.st.Number(`UPDATE cluster_epoch SET epoch = epoch + 1, changed_at = now() WHERE area = $1 RETURNING epoch`, area)
	e.st.Exec(`INSERT INTO cluster_change (area, epoch, payload) VALUES ($1, $2, $3::text::jsonb)`, area, epoch, payload)
	for _, r := range reps {
		if err := r.App.Changes().PollNow(context.Background()); err != nil {
			e.t.Fatalf("poll: %v", err)
		}
	}
}

// post sends a POST of an audit body below the repository's registry URL.
func (e *env) post(r *testenv.Replica, repo, path, token, body string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, "POST", "/api/npm/it/"+repo+"/"+path, token, []byte(body), hdr...)
}

// get sends a GET of a path below the repository's NuGet feed.
func (e *env) get(r *testenv.Replica, repo, path, token string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, "GET", "/api/nuget/it/"+repo+"/"+path, token, nil, hdr...)
}

// npmCode is the code of an npm error body.
func npmCode(t *testing.T, a testenv.Answer) string {
	t.Helper()
	var b map[string]string
	if err := json.Unmarshal(a.Body, &b); err != nil || b["code"] == "" {
		t.Fatalf("answer %d is no npm error body: %v\n%s", a.Status, err, a.Body)
	}
	return b["code"]
}
