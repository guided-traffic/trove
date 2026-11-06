//go:build integration

package ci

// CI tokens at use time (work package W5): R-AUTHZ-014's evaluation of a CI token, R-CI-013's criteria that need no
// exchange, R-CI-017's presentation. The tokens are issued the way the exchange will issue them (W4 is built in
// parallel): through the replica's token service, IssueCITx, in a transaction that also writes the issuance record
// with CiExchangeInsertIssuance, with a frozen bundle the test builds. The mapping store of W3 is built in parallel
// too, so the fixture writes the two rows that store writes for a mapping, the principal (kind ci_mapping, name
// <issuer>/<mapping>) and the ci_mappings row, directly with SQL before the replicas start (Q-CI-10, R-CI-009).

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // dist.shasum of an npm manifest is a SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// ciIssuerName is the issuer of the use-time tests.
const ciIssuerName = "gitlab-main"

// ciEnv is an installation with CI mappings: npm-internal (ownership enforced), npm-loose (ownership off), npm-all
// (virtual over npm-internal, deploying to it), nuget-internal; the issuer gitlab-main and the mappings the test
// names, each with its principal.
type ciEnv struct {
	*env
	issuerID string
	maps     map[string]ciMapping
}

// ciMapping is a mapping row the fixture wrote.
type ciMapping struct{ id, principalID, name, project string }

// ref is the principal reference of the mapping, ci:<issuer>/<mapping>, or ci:<issuer>/<project>/<mapping> for a
// mapping of a project's tier (R-AUTHZ-002, Q-AUTHZ-214).
func (m ciMapping) ref() string { return claims.MappingRef(ciIssuerName, m.project, m.name) }

func newCIEnv(t *testing.T, valkey bool, mappings ...string) *ciEnv {
	t.Helper()
	return newCIEnvWith(t, options{valkey: valkey}, mappings...)
}

// newCIEnvWith is newCIEnv with the options of the installation (the environment of its replicas).
func newCIEnvWith(t *testing.T, o options, mappings ...string) *ciEnv {
	t.Helper()
	e := &ciEnv{env: newEnv(t, o), maps: map[string]ciMapping{}}
	st := e.stack
	st.Repository("npm-internal", "local", "npm", `{"local":{"packageOwnership":"enforce"}}`)
	st.Repository("npm-loose", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	st.Repository("npm-all", "virtual", "npm", "")
	e.exec(`INSERT INTO virtual_members (virtual_id, position, member_id) SELECT v.id, 0, m.id FROM repositories v, repositories m WHERE v.key = 'npm-all' AND m.key = 'npm-internal'`)
	e.exec(`INSERT INTO virtual_deploy_targets (virtual_id, target_id) SELECT v.id, m.id FROM repositories v, repositories m WHERE v.key = 'npm-all' AND m.key = 'npm-internal'`)
	st.Repository("nuget-internal", "local", "nuget", `{"local":{"packageOwnership":"enforce"}}`)
	// The issuer row as the issuer service stores one; no test here fetches its keys or exchanges.
	e.issuerID = e.text(`INSERT INTO ci_issuers (id, name, kind, issuer, allowed_audiences, required_claims, pin_claims, single_use_subject_tokens, public_issuer)
	        VALUES (gen_random_uuid(), $1, 'gitlab', 'https://gitlab.example', '{https://trove.test}', '{}', '{project_id}', false, false) RETURNING id::text`, ciIssuerName)
	for i, name := range mappings {
		e.maps[name] = e.ciMappingRow(name, i+1)
	}
	return e
}

// ciMappingRow writes a system-tier mapping and its principal, the rows W3's store writes (R-CI-009).
func (e *ciEnv) ciMappingRow(name string, priority int) ciMapping {
	e.t.Helper()
	pid := e.text(`INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'ci_mapping', $1) RETURNING id::text`, ciIssuerName+"/"+name)
	id := e.text(`INSERT INTO ci_mappings (id, issuer_id, principal_id, name, priority, match, grants, created_by)
	        VALUES (gen_random_uuid(), $1::uuid, $2::uuid, $3, $4, '{"claim":"project_id","op":"exists"}'::jsonb, '{}'::jsonb, 'test') RETURNING id::text`,
		e.issuerID, pid, name, priority)
	return ciMapping{id: id, principalID: pid, name: name}
}

// policy writes a policy whose entries give the principals the actions on the repositories, before the replicas
// start (testenv.Stack.Policy for a CI mapping principal).
func (e *ciEnv) policy(name string, actions, repositories []string, pkgs []string, principals ...string) {
	e.t.Helper()
	id := e.text("INSERT INTO authz_policy (id, name) VALUES (gen_random_uuid(), $1) RETURNING id::text", name)
	for i, key := range repositories {
		e.exec("INSERT INTO authz_policy_selector (policy_id, ordinal, kind, repository_id) SELECT $1::uuid, $2, 'key', id FROM repositories WHERE key = $3", id, i, key)
	}
	for _, p := range pkgs {
		e.exec("INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) VALUES ($1::uuid, 'package', 'include', $2)", id, p)
	}
	for _, pid := range principals {
		e.exec("INSERT INTO authz_policy_entry (policy_id, principal_id, actions) VALUES ($1::uuid, $2::uuid, $3)", id, pid, actions)
	}
}

// contribution is a system-tier contribution of a mapping to a bundle (Q-CI-17).
func (m ciMapping) contribution(groups []string, inline ...claims.InlineGrant) claims.FrozenMapping {
	for i := range inline {
		inline[i].Ownership = inline[i].ResolvedOwnership()
	}
	return claims.FrozenMapping{Ref: m.ref(), ID: m.id, Tier: "system", Groups: groups, Inline: inline}
}

// bundle is the frozen bundle of a token for a GitLab project.
func (e *ciEnv) bundle(projectID, projectPath string, cs ...claims.FrozenMapping) claims.Frozen {
	f := claims.Frozen{Version: claims.FrozenVersion, Issuer: ciIssuerName, IssuerID: e.issuerID, ProjectID: projectID, ProjectPath: projectPath}
	for _, c := range cs {
		f.Add(c)
	}
	return f
}

// grant is an inline grant on package names.
func grant(repos, packages []string, acts ...claims.Action) claims.InlineGrant {
	return claims.InlineGrant{Repositories: repos, Packages: &claims.PatternSet{Include: packages}, Actions: acts}
}

// ciToken is an issued CI token.
type ciToken struct{ id, secret string }

// job is the job identity of an issuance record.
type job struct{ pipeline, job string }

// issue issues a CI token through a replica the way the exchange does: IssueCITx and the issuance record in one
// transaction (R-CI-017, R-CI-020). The owner is the first contribution's mapping.
func (e *ciEnv) issue(r *testenv.Replica, f claims.Frozen, j job, ttl time.Duration) ciToken {
	e.t.Helper()
	return e.issueScoped(r, f, j, ttl, scope.Scope{Inherit: true})
}

func (e *ciEnv) issueScoped(r *testenv.Replica, f claims.Frozen, j job, ttl time.Duration, sc scope.Scope) ciToken {
	e.t.Helper()
	owner := e.maps[strings.TrimPrefix(f.Mappings[0], "ci:"+ciIssuerName+"/")]
	svc, db := r.App.CITokenIssuer()
	var out token.CITIssued
	grants, _ := json.Marshal(f)
	names := make([]string, 0, len(f.Mappings))
	for _, m := range f.Mappings {
		names = append(names, strings.TrimPrefix(m, "ci:"+ciIssuerName+"/"))
	}
	err := db.InTx(e.t.Context(), func(tx meta.Tx) error {
		var err error
		out, err = svc.IssueCITx(e.t.Context(), tx, token.CITRequest{
			OwnerID: owner.principalID, Description: "CI exchange " + ciIssuerName + " job " + j.job, Frozen: f, Scope: sc, TTL: ttl,
			Claims: map[string]any{"project_id": f.ProjectID, "project_path": f.ProjectPath, "job_id": j.job, "pipeline_id": j.pipeline},
		})
		if err != nil {
			return err
		}
		ptx, _ := postgres.Unwrap(tx)
		return q.New(ptx).CiExchangeInsertIssuance(e.t.Context(), q.CiExchangeInsertIssuanceParams{
			TokenID: out.ID, IssuerID: e.issuerID, MappingIds: f.MappingIDs, MappingNames: names, SubjectKind: "id_token",
			ProjectID: f.ProjectID, ProjectPath: f.ProjectPath, PipelineID: j.pipeline, JobID: j.job, Ref: "main", RefProtected: "true",
			Claims: `{"project_id":"` + f.ProjectID + `"}`, Grants: string(grants), GrantedScope: "read:npm-internal",
			ExpiresUs: out.ExpiresAt.UnixMicro(), ClientIp: "192.0.2.7", UserAgent: "curl/8",
		})
	})
	if err != nil {
		e.t.Fatalf("issuing a CI token: %v", err)
	}
	return ciToken{id: out.ID, secret: out.Secret}
}

// npm publishing and installing, as the npm client does it (R-NPM-007): the package document with the tarball
// attached, the package document and the tarball of an install.

func npmPath(repo, name string) string {
	return npmBase(repo) + strings.ReplaceAll(name, "/", "%2f")
}

// npmBase is the protocol base path of a repository: "<project>/<key>", or a bare key of the default project.
func npmBase(repo string) string {
	project, key := testenv.DefaultProject, repo
	if p, k, ok := strings.Cut(repo, "/"); ok {
		project, key = p, k
	}
	return testenv.ProtocolURL("npm", project, key)
}

// npmBody is the publish request of a version; description makes two builds of one version differ, as two projects' builds do.
func npmBody(t *testing.T, name, version string, description ...string) []byte {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"name": name, "version": version, "description": strings.Join(description, " ")})
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(raw)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	tgz := buf.Bytes()
	s512, s1 := sha512.Sum512(tgz), sha1.Sum(tgz) //nolint:gosec // see the import
	base := name[strings.LastIndexByte(name, '/')+1:]
	doc := map[string]any{
		"_id": name, "name": name, "dist-tags": map[string]string{"latest": version},
		"versions": map[string]any{version: map[string]any{
			"name": name, "version": version, "_id": name + "@" + version,
			"dist": map[string]any{
				"integrity": "sha512-" + base64.StdEncoding.EncodeToString(s512[:]), "shasum": hex.EncodeToString(s1[:]),
				"tarball": "http://x/" + name + "/-/" + base + "-" + version + ".tgz",
			},
		}},
		"_attachments": map[string]any{fmt.Sprintf("%s-%s.tgz", name, version): map[string]any{
			"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tgz), "length": len(tgz),
		}},
	}
	b, _ := json.Marshal(doc)
	return b
}

// publish runs npm publish with a credential (Bearer).
func (e *ciEnv) publish(r *testenv.Replica, repo, secret, name, version string) testenv.Answer {
	e.t.Helper()
	return e.stack.Do(r, "PUT", npmPath(repo, name), secret, npmBody(e.t, name, version))
}

// install reads the package document the way npm install does.
func (e *ciEnv) install(r *testenv.Replica, repo, secret, name string) testenv.Answer {
	e.t.Helper()
	return e.stack.Do(r, "GET", npmPath(repo, name), secret, nil)
}

// npmCode is the code of an npm error body.
func npmCode(a testenv.Answer) string {
	var b map[string]any
	_ = json.Unmarshal(a.Body, &b)
	c, _ := b["code"].(string)
	return c
}

func mustStatus(t *testing.T, what string, a testenv.Answer, status int) {
	t.Helper()
	if a.Status != status {
		t.Fatalf("%s: %d, want %d\n%s", what, a.Status, status, a.Body)
	}
}

// R-CI-013 with the grant of its example, R-AUTHZ-014's union and its escaped claim: a job installs through the
// policy ci-read-internal, publishes the package its inline grant renders, is refused another namespace, and a
// claim value that carries a glob character renders as a literal.
func TestR_CI_013_AJobInstallsThroughItsPolicyAndPublishesItsOwnPackage(t *testing.T) {
	e := newCIEnv(t, false, "acme-release")
	m := e.maps["acme-release"]
	e.policy("ci-read-internal", []string{"read"}, []string{"npm-all", "npm-internal"}, nil, m.principalID)
	ops := e.stack.User("publisher")
	e.stack.Policy("publishers", []string{"read", "deploy"}, []string{"npm-internal"}, ops)
	opsTok := e.stack.Token(ops, scope.Default())
	a := e.web("web-a")
	mustStatus(t, "seeding a dependency", e.publish(a, "npm-internal", opsTok, "@acme/base", "1.0.0"), 201)

	// The inline grant of R-CI-013's example, rendered for acme/billing/invoice-api.
	spec := claims.Spec{Inline: []claims.InlineGrant{grant([]string{"it/npm-internal"}, []string{"@{{ namespace_path | slug }}/*"}, claims.Read, claims.Deploy)}}
	rendered, err := spec.Render(claims.Claims{"namespace_path": "acme/billing", "project_path": "acme/billing/invoice-api"})
	if err != nil {
		t.Fatal(err)
	}
	tok := e.issue(a, e.bundle("4711", "acme/billing/invoice-api", m.contribution(nil, rendered.Inline...)), job{"77", "9001"}, 10*time.Minute)

	t.Run("R-CI-013 npm install from npm-all through ci-read-internal", func(t *testing.T) {
		mustStatus(t, "install", e.install(a, "npm-all", tok.secret, "@acme/base"), 200)
	})
	t.Run("R-CI-013 npm publish of the project's own package to npm-internal", func(t *testing.T) {
		mustStatus(t, "publish", e.publish(a, "npm-internal", tok.secret, "@acme-billing/invoice-client", "1.0.0"), 201)
	})
	t.Run("R-CI-013 publishing @acme-shipping/x is refused", func(t *testing.T) {
		mustStatus(t, "publish", e.publish(a, "npm-internal", tok.secret, "@acme-shipping/x", "1.0.0"), 403)
	})
	t.Run("R-AUTHZ-014 the rendering of team/ui* matches the literal name only, so @acme/ui-kit is refused", func(t *testing.T) {
		spec := claims.Spec{Inline: []claims.InlineGrant{grant([]string{"it/npm-internal"}, []string{"@acme/{{ project_path | segment -1 }}"}, claims.Read, claims.Deploy)}}
		r, err := spec.Render(claims.Claims{"project_path": "team/ui*"})
		if err != nil || r.Inline[0].Packages.Include[0] != `@acme/ui\*` {
			t.Fatalf("rendered %v, %v", r, err)
		}
		ui := e.issue(a, e.bundle("4712", "team/ui*", m.contribution(nil, r.Inline...)), job{"78", "9002"}, 10*time.Minute)
		mustStatus(t, "publish @acme/ui-kit", e.publish(a, "npm-internal", ui.secret, "@acme/ui-kit", "1.0.0"), 403)
	})
}

// R-AUTHZ-002: group everyone is auto-join and reads npm-loose, and authenticated reads it as well; a CI token whose
// mapping lists no group and that no policy covering npm-loose names gets 404 there.
func TestR_AUTHZ_002_ACITokenIsNeitherAuthenticatedNorInAnAutoJoinGroup(t *testing.T) {
	e := newCIEnv(t, false, "acme-release")
	m := e.maps["acme-release"]
	everyone := e.text(`INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'group', 'everyone') RETURNING id::text`)
	e.exec(`INSERT INTO user_group (principal_id, origin, auto_join) VALUES ($1::uuid, 'admin', true)`, everyone)
	e.policy("everyone-reads", []string{"read"}, []string{"npm-loose"}, nil, everyone, e.text(`SELECT id::text FROM principal WHERE kind = 'authenticated'`))
	reader := e.stack.User("reader")
	readerTok := e.stack.Token(reader, scope.Default())
	a := e.web("web-a")
	tok := e.issue(a, e.bundle("4711", "acme/web/frontend", m.contribution(nil, grant([]string{"it/npm-internal"}, []string{"@acme/*"}, claims.Read))), job{"1", "2"}, 10*time.Minute)
	if r := e.stack.Do(a, "GET", "/api/npm/it/npm-loose/-/whoami", readerTok, nil); r.Status != 200 {
		t.Fatalf("the user cannot see npm-loose: %d", r.Status)
	}
	if r := e.stack.Do(a, "GET", "/api/npm/it/npm-loose/-/whoami", tok.secret, nil); r.Status != 404 {
		t.Fatalf("the CI token sees npm-loose: %d %s", r.Status, r.Body)
	}
}

// nupkg builds a package archive with its .nuspec, as dotnet pack writes one.
func nupkg(t *testing.T, id, version string) []byte {
	t.Helper()
	nuspec := `<?xml version="1.0" encoding="utf-8"?><package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata><id>` +
		id + `</id><version>` + version + `</version><authors>Acme</authors><description>Widgets</description></metadata></package>`
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{id + ".nuspec": nuspec, "lib/net8.0/" + id + ".dll": "MZ" + version} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// nugetForm wraps a package the way the NuGet clients send a push.
func nugetForm(t *testing.T, pkg []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="package"; filename="package"`)
	h.Set("Content-Type", "application/octet-stream")
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(pkg)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

// R-AUTHZ-014, R-CI-013: removing a mapping from a policy's entries makes the next install with an already issued
// token fail on every replica within the bound of R-AUTHZ-028, without a new exchange, with Valkey running and
// stopped. The refused read answers 404 under authz.hide_existence, the default (R-AUTHZ-016, Q-CI-39).
func TestR_AUTHZ_014_RemovingAMappingFromAPolicyFailsTheNextInstall(t *testing.T) {
	for _, valkey := range []bool{true, false} {
		t.Run(fmt.Sprintf("valkey %v", valkey), func(t *testing.T) {
			e := newCIEnv(t, valkey, "acme-read")
			m := e.maps["acme-read"]
			ops := e.stack.User("publisher")
			e.stack.Policy("publishers", []string{"read", "deploy"}, []string{"npm-internal"}, ops)
			opsTok := e.stack.Token(ops, scope.Default())
			reps := []*testenv.Replica{e.web("web-a"), e.web("web-b"), e.web("web-c")}
			mustStatus(t, "seeding", e.publish(reps[0], "npm-internal", opsTok, "@acme/base", "1.0.0"), 201)
			created := e.do(reps[0], "POST", "/api/v1/permission-policies", map[string]any{
				"name": "ci-read-internal", "resources": map[string]any{"repositories": []string{"it/npm-all", "npm-internal"}},
				"entries": []any{map[string]any{"principal": m.ref(), "actions": []string{"read"}}},
			})
			wantStatus(t, created, 201, "")
			tok := e.issue(reps[0], e.bundle("4711", "acme/web/frontend", m.contribution(nil)), job{"1", "2"}, 10*time.Minute)
			for _, r := range reps {
				within(t, r.Name+" granting the install", 3*time.Second, func() bool { return e.install(r, "npm-all", tok.secret, "@acme/base").Status == 200 })
			}
			patched := e.do(reps[0], "PATCH", "/api/v1/permission-policies/ci-read-internal", map[string]any{"entries": []any{}},
				"If-Match", etag(created), "Content-Type", "application/merge-patch+json")
			wantStatus(t, patched, 200, "")
			start := time.Now()
			bound := time.Second
			if !valkey {
				bound += 500 * time.Millisecond // cluster.epoch_poll_interval of the fixture plus 1 s (R-AUTHZ-032)
			}
			for _, r := range reps[1:] {
				within(t, r.Name+" refusing the install", bound+time.Second, func() bool { return e.install(r, "npm-all", tok.secret, "@acme/base").Status == 404 })
			}
			if took := time.Since(start); took > bound {
				t.Errorf("refused on every replica after %s, bound %s", took, bound)
			}
		})
	}
}

// R-CI-017: the CI token is presented as a Bearer token to npm whoami, as the password of HTTP Basic to a NuGet
// source and as X-NuGet-ApiKey to a NuGet push (dotnet nuget push -k), all over HTTP as the clients send them.
func TestR_CI_017_TheTokenIsPresentedLikeEveryTroveToken(t *testing.T) {
	e := newCIEnv(t, false, "acme-release")
	m := e.maps["acme-release"]
	a := e.web("web-a")
	tok := e.issue(a, e.bundle("4711", "acme/web/frontend", m.contribution(nil,
		grant([]string{"it/npm-internal"}, []string{"@acme/*"}, claims.Read, claims.Deploy),
		grant([]string{"it/nuget-internal"}, []string{"Acme.*"}, claims.Read, claims.Deploy))), job{"5", "6"}, 10*time.Minute)

	t.Run("R-CI-017 npm whoami with Bearer", func(t *testing.T) {
		r := e.stack.Do(a, "GET", "/api/npm/it/npm-internal/-/whoami", tok.secret, nil)
		mustStatus(t, "whoami", r, 200)
		if got := r.JSON(t)["username"]; got != "ci:"+ciIssuerName+"/acme-release" {
			t.Errorf("username %v", got)
		}
	})
	t.Run("R-CI-017 a NuGet source with Basic credentials", func(t *testing.T) {
		basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("gitlab-ci-token:"+tok.secret))
		mustStatus(t, "service index", e.stack.Do(a, "GET", "/api/nuget/it/nuget-internal/v3/index.json", "", nil, "Authorization", basic), 200)
	})
	t.Run("R-CI-017 dotnet nuget push -k sends the token as X-NuGet-ApiKey", func(t *testing.T) {
		body, ct := nugetForm(t, nupkg(t, "Acme.Widgets", "1.0.0"))
		r := e.stack.Do(a, "PUT", "/api/nuget/it/nuget-internal/v3/package", "", body, "Content-Type", ct, "X-NuGet-ApiKey", tok.secret)
		mustStatus(t, "push", r, 201)
	})
}
