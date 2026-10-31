//go:build integration

package npm

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
)

// The security audit (R-NPM-038) against the integration stack, with the counting test
// upstream as the advisory provider: what reaches it, what is withheld and why, the
// refusals, and what the forward holds while the provider takes its time.

const bulkPath = "-/npm/v1/security/advisories/bulk"

// postAudit posts an audit body below a repository's registry URL; gz sends it gzip-compressed.
func (r *renv) postAudit(rep *testenv.Replica, repo, token, body string, gz bool) testenv.Answer {
	r.t.Helper()
	if !gz {
		return r.st.Do(rep, http.MethodPost, "/api/npm/"+repo+"/"+bulkPath, token, []byte(body))
	}
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write([]byte(body))
	_ = zw.Close()
	return r.st.Do(rep, http.MethodPost, "/api/npm/"+repo+"/"+bulkPath, token, b.Bytes(), "Content-Encoding", "gzip")
}

// askedOf is the sorted names of the i-th request the provider received.
func askedOf(t *testing.T, log []npmstub.AuditRequest, i int) []string {
	t.Helper()
	if i >= len(log) {
		t.Fatalf("the provider received %d requests, want at least %d", len(log), i+1)
	}
	var out []string
	for n := range log[i].Names {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// metricSum sums the series of a metric family whose labels contain label, over every
// repository label.
func metricSum(text, family, label string) float64 {
	var sum float64
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, family+"{") && strings.Contains(line, label) {
			f := strings.Fields(line)
			v, _ := strconv.ParseFloat(f[len(f)-1], 64)
			sum += v
		}
	}
	return sum
}

func TestR_NPM_038_TheForwardedSetThroughARemoteAndAVirtualRepository(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	// A claim of npm-local covers @cobalt/*: scope members, so npm-remote itself still fetches it.
	r.st.Exec(`INSERT INTO namespace_claims (repo_id, pattern, source, scope) SELECT id, '@cobalt/*', 'manual', 'members' FROM repositories WHERE key = $1`, rLocal)
	// A pin rule of npm-all selects left-pad.
	r.st.Exec(`INSERT INTO virtual_pin_rules (virtual_id, position, package_patterns) SELECT id, 0, ARRAY['left-pad'] FROM repositories WHERE key = $1`, rAll)
	r.st.Exec(`INSERT INTO virtual_pin_members (virtual_id, position, member_id)
		SELECT v.id, 0, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $2`, rAll, rRemote)
	rep := r.start(1)[0]
	for _, n := range []string{"lodash", "left-pad", "@cobalt/dusky-gauge", "JSONStream"} {
		if a := r.view(rep, rRemote, scopedPath(n), r.tokA); a.Status != 200 {
			t.Fatalf("fetch %s: %d %s", n, a.Status, a.Body)
		}
	}
	r.publishTo(rep, rLocal, r.tokA, pkg{Name: "@acme/ui", Version: "1.0.0"})
	// A document the core marked gone stays forwardable: withdrawn packages carry the advisories.
	r.st.Exec(`UPDATE remote_cache_entries SET upstream_gone_at = now() WHERE path = 'JSONStream' COLLATE "C"`)
	r.stub.Reset()

	body := `{"lodash":["4.17.20"],"left-pad":["1.3.0"],"@cobalt/dusky-gauge":["2.0.0"],"@acme/ui":["1.0.0"],` +
		`"azure-orbit":["1.0.0"],"JSONStream":["1.3.5"]}`

	t.Run("through the remote: the names it fetched minus the local and the claimed one; gzip", func(t *testing.T) {
		a := r.postAudit(rep, rRemote, r.tokA, body, true)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		if got := askedOf(t, r.stub.Audits(), 0); strings.Join(got, ",") != "JSONStream,left-pad,lodash" {
			t.Fatalf("the provider was asked about %v", got)
		}
		var answer map[string][]struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(a.Body, &answer); err != nil || len(answer) != 2 || answer["lodash"][0].ID != 1900002 || answer["left-pad"][0].ID != 1900001 {
			t.Fatalf("the provider's answer is not passed on: %s", a.Body)
		}
		want := "trove audited 3 of 6 packages; 3 internal or not yet proxied names were not sent to the advisory provider"
		if got := a.Header.Get("npm-notice"); got != want {
			t.Errorf("npm-notice %q, want %q", got, want)
		}
		if a.Header.Get("Content-Type") != "application/json" || a.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("headers %v", a.Header)
		}
	})

	t.Run("through the virtual repository: the pin rule withholds left-pad too", func(t *testing.T) {
		a := r.postAudit(rep, rAll, r.tokA, body, false)
		if a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		if got := askedOf(t, r.stub.Audits(), 1); strings.Join(got, ",") != "JSONStream,lodash" {
			t.Fatalf("the provider was asked about %v", got)
		}
		if got := a.Header.Get("npm-notice"); !strings.HasPrefix(got, "trove audited 2 of 6 packages; 4 ") {
			t.Errorf("npm-notice %q", got)
		}
	})

	t.Run("a body of withheld names only is {} and reaches no provider", func(t *testing.T) {
		before := len(r.stub.Audits())
		a := r.postAudit(rep, rAll, r.tokA, `{"@acme/ui":["1.0.0"],"azure-orbit":["1.0.0"]}`, false)
		if a.Status != 200 || string(a.Body) != "{}" || len(r.stub.Audits()) != before {
			t.Fatalf("%d %s, %d requests", a.Status, a.Body, len(r.stub.Audits())-before)
		}
	})

	t.Run("the provider's log never holds a name of npm-local, a claimed name or one npm-remote never fetched", func(t *testing.T) {
		log := r.stub.Audits()
		for _, n := range []string{"@acme/ui", "@cobalt/dusky-gauge", "azure-orbit"} {
			if npmstub.Asked(log, n) {
				t.Errorf("the provider was asked about %s", n)
			}
		}
		for _, x := range log {
			if x.Auth != "none" {
				t.Errorf("a request carried a %s credential, the remote has none", x.Auth)
			}
		}
	})

	t.Run("the withheld names are counted once, by reason", func(t *testing.T) {
		m := rep.Metrics(t)
		for reason, want := range map[string]float64{"local": 3, "claimed": 2, "pinned": 1, "not_proxied": 3} {
			if got := metricSum(m, "trove_npm_audit_names_withheld_total", `reason="`+reason+`"`); got != want {
				t.Errorf("reason %s: %v, want %v", reason, got, want)
			}
		}
	})
}

// scopedPath escapes the slash of a scoped name the way npm does.
func scopedPath(name string) string { return strings.Replace(name, "/", "%2f", 1) }

func TestR_NPM_038_PatternsAndTheVirtualRepositorysForwardingMember(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	// npm-audit-off forwards nothing; npm-audit-pat refuses JSONStream by its patterns; the
	// virtual repository lists npm-audit-off first, so npm-audit-pat is its forwarding member.
	off := map[string]any{"audit": map[string]any{"mode": "disabled"}}
	r.st.Repository("npm-audit-off", "remote", "npm", r.remoteConfig(3600, nil, off))
	patterned := r.remoteConfig(3600, nil, nil)
	patterned = strings.Replace(patterned, `{"remote"`, `{"excludePatterns":["JSONStream"],"remote"`, 1)
	r.st.Repository("npm-audit-pat", "remote", "npm", patterned)
	r.st.Repository("npm-audit-all", "virtual", "npm", `{}`)
	for i, m := range []string{"npm-audit-off", "npm-audit-pat"} {
		r.st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
			SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = 'npm-audit-all' AND m.key = $1`, m, i)
	}
	r.st.Policy("audit-readers", []string{"read"}, []string{"npm-audit-off", "npm-audit-pat", "npm-audit-all"}, r.alice)
	r.st.Policy("audit-virtual-only", []string{"read"}, []string{"npm-audit-all", "npm-audit-off"}, r.bob)
	rep := r.start(1)[0]
	if a := r.view(rep, "npm-audit-pat", "lodash", r.tokA); a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	// The pattern came after the fetch: the record exists, the name is refused all the same.
	r.st.Exec(`INSERT INTO npm_remote_packages (repo_id, name, doc_sha256) SELECT id, 'JSONStream', sha256('x') FROM repositories WHERE key = 'npm-audit-pat'`)
	r.stub.Reset()

	a := r.postAudit(rep, "npm-audit-all", r.tokA, `{"lodash":["4.17.20"],"JSONStream":["1.3.5"]}`, false)
	if a.Status != 200 || !strings.Contains(a.Header.Get("npm-notice"), "audited 1 of 2") {
		t.Fatalf("%d %v %s", a.Status, a.Header, a.Body)
	}
	if got := askedOf(t, r.stub.Audits(), 0); strings.Join(got, ",") != "lodash" {
		t.Fatalf("asked about %v", got)
	}
	t.Run("a caller without read on the forwarding member is refused, and no provider is asked", func(t *testing.T) {
		a := r.postAudit(rep, "npm-audit-all", r.tokB, `{"lodash":["4.17.20"]}`, false)
		if a.Status != 404 || len(r.stub.Audits()) != 1 {
			t.Fatalf("%d %s, %d requests", a.Status, a.Body, len(r.stub.Audits()))
		}
	})
}

func TestR_NPM_038_WhereTheAuditAnswers501And413And502(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	r.st.Repository("npm-audit-off", "remote", "npm", r.remoteConfig(3600, nil, map[string]any{"audit": map[string]any{"mode": "disabled"}}))
	r.st.Repository("npm-offline", "remote", "npm", r.remoteConfig(3600, map[string]any{
		"availability": map[string]any{"mode": "offline", "failureThreshold": 3, "circuitOpenSeconds": 30},
	}, nil))
	// 127.0.0.2 is loopback, which the egress policy refuses (only 127.0.0.1/32 is allowed).
	r.st.Repository("npm-egress", "remote", "npm", r.remoteConfig(3600, nil, map[string]any{
		"audit": map[string]any{"providerURL": strings.Replace(r.run.URL, "127.0.0.1", "127.0.0.2", 1) + "/"},
	}))
	r.st.Repository("npm-small", "remote", "npm", r.remoteConfig(3600, nil, map[string]any{"audit": map[string]any{"maxBodySize": "64KiB"}}))
	r.st.Policy("audit-readers", []string{"read"}, []string{"npm-audit-off", "npm-offline", "npm-egress", "npm-small"}, r.alice)
	rep := r.start(1)[0]
	for _, key := range []string{rRemote, "npm-egress"} {
		if a := r.view(rep, key, "lodash", r.tokA); a.Status != 200 {
			t.Fatalf("%s: %d %s", key, a.Status, a.Body)
		}
	}
	r.stub.Reset()
	text := func(key string) string {
		return "security audit is not available on repository " + key + "; it is disabled or no upstream provides advisories"
	}

	unavailable := func(t *testing.T, key, path string) {
		t.Helper()
		a := r.st.Do(rep, http.MethodPost, "/api/npm/"+key+"/"+path, r.tokA, []byte(`{"lodash":["4.17.20"]}`))
		var b map[string]string
		_ = json.Unmarshal(a.Body, &b)
		if a.Status != 501 || b["code"] != "npm_audit_unavailable" || b["error"] != text(key) {
			t.Errorf("%s %s: %d %s", key, path, a.Status, a.Body)
		}
	}

	t.Run("501 for a local repository, a remote in mode disabled, a virtual repository without a forwarding member", func(t *testing.T) {
		for _, key := range []string{rLocal, "npm-audit-off", rTwo} {
			for _, path := range []string{bulkPath, "-/npm/v1/security/audits/quick", "-/npm/v1/security/audits"} {
				unavailable(t, key, path)
			}
		}
	})

	t.Run("501 for an offline remote that has a name to forward", func(t *testing.T) {
		r.st.Exec(`INSERT INTO npm_remote_packages (repo_id, name, doc_sha256) SELECT id, 'lodash', sha256('x') FROM repositories WHERE key = 'npm-offline'`)
		unavailable(t, "npm-offline", bulkPath)
	})

	t.Run("no forward from an endpoint that does not fill caches", func(t *testing.T) {
		// The endpoint's ceiling may refuse the request first (404); either way no provider is asked.
		a := r.st.DoAt(rep.Addr("customers"), customersHost, http.MethodPost, "/api/npm/"+rCustomers+"/"+bulkPath, r.acmeSAT, []byte(`{"lodash":["4.17.20"]}`))
		if (a.Status != 501 || code(t, a) != "npm_audit_unavailable") && a.Status != 404 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})

	t.Run("413 for a small gzip body that inflates beyond ten times the limit, and for a declared length above it", func(t *testing.T) {
		bomb := `{"lodash":["` + strings.Repeat("0", 11<<20) + `"]}`
		a := r.postAudit(rep, rRemote, r.tokA, bomb, true)
		if a.Status != 413 || code(t, a) != "npm_too_large" {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		// 100 KiB above a limit of 64 KiB: refused before a byte is read, and small enough for the
		// server to read the rest and keep the connection for the answer.
		big := r.postAudit(rep, "npm-small", r.tokA, `{"lodash":["`+strings.Repeat("1", 100<<10)+`"]}`, false)
		if big.Status != 413 || code(t, big) != "npm_too_large" {
			t.Fatalf("%d %s", big.Status, big.Body)
		}
	})

	t.Run("502 when the provider fails, and egress_denied for a provider the policy refuses", func(t *testing.T) {
		if err := r.stub.SetMode(npmstub.Mode{Down: npmstub.DownUnavailable}); err != nil {
			t.Fatal(err)
		}
		a := r.postAudit(rep, rRemote, r.tokA, `{"lodash":["4.17.20"]}`, false)
		_ = r.stub.SetMode(npmstub.Mode{})
		if a.Status != 502 || code(t, a) != "upstream_error" {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		before := metricSum(rep.Metrics(t), "trove_remote_egress_denied_total", "repo=")
		e := r.postAudit(rep, "npm-egress", r.tokA, `{"lodash":["4.17.20"]}`, false)
		if e.Status != 502 || code(t, e) != "egress_denied" {
			t.Fatalf("%d %s", e.Status, e.Body)
		}
		if after := metricSum(rep.Metrics(t), "trove_remote_egress_denied_total", "repo="); after != before+1 {
			t.Errorf("trove_remote_egress_denied_total %v, then %v", before, after)
		}
	})
	if n := len(r.stub.Audits()); n != 0 {
		t.Errorf("a refused audit reached the provider %d times", n)
	}
}

func TestR_NPM_038_TheForwardHoldsNoConnectionAndEndsByItsDeadline(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	r.st.Repository("npm-quick", "remote", "npm", r.remoteConfig(3600, nil, map[string]any{"audit": map[string]any{"timeout": "1s"}}))
	r.st.Policy("audit-readers", []string{"read"}, []string{"npm-quick"}, r.alice)
	rep := r.start(1)[0]
	for _, key := range []string{rRemote, "npm-quick"} {
		if a := r.view(rep, key, "lodash", r.tokA); a.Status != 200 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	}

	t.Run("no session is idle in a transaction while the provider takes 2 s", func(t *testing.T) {
		if err := r.stub.SetMode(npmstub.Mode{DelayMS: map[string]int{npmstub.KindAudit: 2000}}); err != nil {
			t.Fatal(err)
		}
		var idle atomic.Int64
		done := make(chan struct{})
		go func() {
			for {
				select {
				case <-done:
					return
				case <-time.After(50 * time.Millisecond):
				}
				// The replicas' pollers hold transactions of a few milliseconds; a forward that held one
				// would hold it for the whole wait.
				n, err := r.st.TryNumber(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
					AND state LIKE 'idle in transaction%' AND clock_timestamp() - state_change > interval '700 milliseconds'`)
				if err == nil && n > idle.Load() {
					idle.Store(n)
				}
			}
		}()
		start := time.Now()
		a := r.postAudit(rep, rRemote, r.tokA, `{"lodash":["4.17.20"]}`, false)
		close(done)
		if a.Status != 200 || time.Since(start) < 2*time.Second {
			t.Fatalf("%d after %s: %s", a.Status, time.Since(start), a.Body)
		}
		if got := idle.Load(); got != 0 {
			t.Fatalf("%d sessions were idle in a transaction while the provider was asked", got)
		}
	})

	t.Run("min(npm.audit.timeout, server.shutdown_timeout - 1 s): 4 s of the stack's 5 s, and 1 s of a remote's own", func(t *testing.T) {
		if err := r.stub.SetMode(npmstub.Mode{DelayMS: map[string]int{npmstub.KindAudit: 7000}}); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.stub.SetMode(npmstub.Mode{}) }()
		for key, want := range map[string]time.Duration{rRemote: 4 * time.Second, "npm-quick": time.Second} {
			start := time.Now()
			a := r.postAudit(rep, key, r.tokA, `{"lodash":["4.17.20"]}`, false)
			took := time.Since(start)
			if a.Status != 502 || code(t, a) != "upstream_timeout" {
				t.Fatalf("%s: %d %s", key, a.Status, a.Body)
			}
			if took < want || took > want+1500*time.Millisecond {
				t.Errorf("%s: the forward ended after %s, want %s", key, took.Round(time.Millisecond), want)
			}
		}
	})
}

func TestR_NPM_038_CredentialsReachOnlyTheUpstreamOrigin(t *testing.T) {
	const env = "TROVE_CAC_NPM_AUDIT_UPSTREAM"
	t.Setenv(env, "secret")
	r := newRemoteEnv(t, remoteOptions{})
	// A second provider on another origin of the same CA.
	other, err := npmstub.New(npmstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := other.Start("127.0.0.1:0", r.ss.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = otherRun.Close() })
	auth := map[string]any{"authMode": "bearer", "password": map[string]any{"secretRef": map[string]any{"env": env}}}
	r.st.Repository("npm-cred", "remote", "npm", r.remoteConfig(3600, auth, nil))
	r.st.Repository("npm-cred-other", "remote", "npm", r.remoteConfig(3600, auth, map[string]any{"audit": map[string]any{"providerURL": otherRun.URL + "/"}}))
	for _, key := range []string{"npm-cred", "npm-cred-other"} {
		r.st.Exec(`INSERT INTO repository_secrets (repo_id, field, secret_ref) SELECT id, '/remote/password', $2::text::jsonb FROM repositories WHERE key = $1`,
			key, `{"env":"`+env+`"}`)
	}
	r.st.Policy("audit-readers", []string{"read"}, []string{"npm-cred", "npm-cred-other"}, r.alice)
	if err := r.stub.SetMode(npmstub.Mode{RequireAuthorization: "Bearer secret"}); err != nil {
		t.Fatal(err)
	}
	rep := r.start(1)[0]
	for _, key := range []string{"npm-cred", "npm-cred-other"} {
		if a := r.view(rep, key, "lodash", r.tokA); a.Status != 200 {
			t.Fatalf("%s: %d %s", key, a.Status, a.Body)
		}
	}
	r.stub.Reset()

	if a := r.postAudit(rep, "npm-cred", r.tokA, `{"lodash":["4.17.20"]}`, false); a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if log := r.stub.Audits(); len(log) != 1 || log[0].Auth != "bearer" {
		t.Fatalf("the upstream as provider received %+v, want the repository's bearer credential", log)
	}
	if a := r.postAudit(rep, "npm-cred-other", r.tokA, `{"lodash":["4.17.20"]}`, false); a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if log := other.Audits(); len(log) != 1 || log[0].Auth != "none" || len(r.stub.Audits()) != 1 {
		t.Fatalf("a provider on another origin received %+v, want no credential", log)
	}
}

func TestR_NPM_038_TheAuditSettingsThroughTheAPI(t *testing.T) {
	r := newRemoteEnv(t, remoteOptions{})
	rep := r.start(1)[0]
	create := func(key, class string, npm map[string]any) testenv.Answer {
		t.Helper()
		body := map[string]any{"key": key, "class": class, "format": "npm", "npm": npm}
		if class == "remote" {
			body["remote"] = map[string]any{"url": r.run.URL + "/"}
		}
		return r.st.Do(rep, http.MethodPost, "/api/v1/repositories", r.opsPAT, body)
	}

	t.Run("a remote takes the audit member and reads it back", func(t *testing.T) {
		a := create("npm-audit-set", "remote", map[string]any{"audit": map[string]any{
			"mode": "forward", "providerURL": "https://advisories.example.org/npm/", "timeout": "20s", "maxBodySize": "512KiB",
		}})
		if a.Status != 201 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		got, _ := json.Marshal(a.JSON(t)["npm"])
		if !strings.Contains(string(got), `"providerURL":"https://advisories.example.org/npm/"`) {
			t.Fatalf("read back %s", got)
		}
	})

	t.Run("a providerURL with user info, a query or http is refused, so no credential sits in the document", func(t *testing.T) {
		for _, u := range []string{"https://user:secret@advisories.example.org/", "https://token@advisories.example.org/", "https://advisories.example.org/?token=x", "http://advisories.example.org/"} {
			a := create("npm-audit-bad", "remote", map[string]any{"audit": map[string]any{"providerURL": u}})
			if a.Status != 422 || settingsErrors(t, a)["/npm/audit/providerURL"] != "invalid_value" {
				t.Errorf("%s: %d %s", u, a.Status, a.Body)
			}
		}
		if strings.Contains(r.st.Text(`SELECT coalesce(string_agg(config::text, ''), '') FROM repositories`), "secret@") {
			t.Fatal("a refused providerURL was stored")
		}
	})

	t.Run("the member applies to remote repositories only", func(t *testing.T) {
		a := create("npm-audit-local", "local", map[string]any{"audit": map[string]any{"mode": "disabled"}})
		if a.Status != 422 || settingsErrors(t, a)["/npm/audit"] != "not_applicable" {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
}
