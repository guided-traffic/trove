package oidcstub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// The fixtures in testdata were recorded from a real GitLab by cmd/record-gitlab (make
// oidc-fixtures; Q-CI-25 of docs/spec/gitlab-ci-oidc.md). The tests in this file check the double
// against them. They are evidence that the double is GitLab-shaped, not evidence for trove's
// requirements, so their names carry no requirement ID; the requirement each one concerns is named
// in its comment.

var (
	uuidRE    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	decimalRE = regexp.MustCompile(`^[0-9]+$`)
	sha1RE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// ci_config_ref_uri: host[:port]/<project path>//<file>@<ref path>, without a scheme.
	configRefRE = regexp.MustCompile(`^[^/]+/.+//\.gitlab-ci\.yml@refs/(heads|tags)/.+$`)
)

type tokenFixture struct {
	GitLabVersion string `json:"gitlab_version"`
	Case          string `json:"case"`
	Job           struct {
		TimeoutSeconds int64 `json:"timeout_seconds"`
		Parts          int   `json:"parts"`
	} `json:"job"`
	Header    map[string]any               `json:"header"`
	Claims    map[string]any               `json:"claims"`
	Variables map[string]map[string]string `json:"variables"`
}

func readFixture(t *testing.T, path string, into any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func tokenFixtures(t *testing.T) map[string]tokenFixture {
	t.Helper()
	files, err := filepath.Glob("testdata/id-token-*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no ID token fixtures in testdata (%v)", err)
	}
	out := map[string]tokenFixture{}
	for _, f := range files {
		var fx tokenFixture
		readFixture(t, f, &fx)
		if fx.GitLabVersion == "" || fx.Case == "" || len(fx.Claims) == 0 {
			t.Fatalf("%s: no version, case or claims", f)
		}
		out[fx.Case] = fx
	}
	return out
}

// kind is the JSON type of a decoded value and, for a string, the shape of its content: GitLab
// writes IDs as decimal strings and booleans as "true"/"false".
func kind(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case map[string]any:
		return "object"
	case []any:
		var kinds []string
		for _, e := range x {
			if k := kind(e); !slices.Contains(kinds, k) {
				kinds = append(kinds, k)
			}
		}
		return "array of " + strings.Join(kinds, ", ")
	case string:
		switch {
		case x == "true" || x == "false":
			return "string (boolean)"
		case decimalRE.MatchString(x):
			return "string (decimal)"
		case uuidRE.MatchString(x):
			return "string (UUID)"
		case sha1RE.MatchString(x):
			return "string (SHA-1)"
		case strings.HasPrefix(x, "https://"):
			return "string (https URL)"
		}
		return "string"
	}
	return fmt.Sprintf("%T", v)
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

// subShape is the component names of a sub claim: "project_path:a/b:ref_type:tag:ref:v1" gives
// [project_path ref_type ref]. Git forbids ":" in ref names, so the split is unambiguous.
func subShape(v any) []string {
	s, _ := v.(string)
	parts := strings.Split(s, ":")
	var names []string
	for i := 0; i < len(parts); i += 2 {
		names = append(names, parts[i])
	}
	return names
}

// refPathOf is the ref_path GitLab derives from ref and ref_type.
func refPathOf(c map[string]any) string {
	if c["ref_type"] == "tag" {
		return "refs/tags/" + fmt.Sprint(c["ref"])
	}
	return "refs/heads/" + fmt.Sprint(c["ref"])
}

// Every recorded ID token is what Mint returns once the claims the fixture lacks are removed: the
// same claim names, JSON types and value shapes, the same header members, alg and typ, the same
// sub components and the same time relations (R-CI-006; R-CI-004 for the header and nbf). The
// release job's fixture is DefaultClaims itself.
func TestDoubleReproducesTheRecordedIDTokens(t *testing.T) {
	fixtures := tokenFixtures(t)
	s, err := New(Config{PublicURL: "https://gitlab.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	defaults := DefaultClaims(s.Issuer(), nil, time.Now())
	release, ok := fixtures["push-protected-tag-environment"]
	if !ok {
		t.Fatal("no fixture of the release job (push-protected-tag-environment)")
	}
	if got, want := keysOf(defaults), keysOf(release.Claims); !slices.Equal(got, want) {
		t.Errorf("DefaultClaims has %v, GitLab %s's release job %v", got, release.GitLabVersion, want)
	}

	for name, fx := range fixtures {
		t.Run(name, func(t *testing.T) {
			var remove, missing []string
			for c := range defaults {
				if _, ok := fx.Claims[c]; !ok {
					remove = append(remove, c)
				}
			}
			for c := range fx.Claims {
				if _, ok := defaults[c]; !ok {
					missing = append(missing, c)
				}
			}
			if len(missing) > 0 {
				t.Fatalf("GitLab %s sends %v; DefaultClaims has no such claim", fx.GitLabVersion, missing)
			}
			o := MintOptions{Remove: remove}
			if _, list := fx.Claims["aud"].([]any); list { // the job chose the audience
				o.Claims = map[string]any{"aud": fx.Claims["aud"]}
			}
			tok := mustMint(t, s, o)
			if n := len(strings.Split(tok, ".")); n != fx.Job.Parts {
				t.Errorf("%d parts, GitLab's token has %d", n, fx.Job.Parts)
			}
			h, p := parseHeader(t, tok), parsePayload(t, tok)

			if got, want := keysOf(h), keysOf(fx.Header); !slices.Equal(got, want) {
				t.Errorf("header members %v, GitLab %v", got, want)
			}
			kid, _ := h["kid"].(string)
			wantKID, _ := fx.Header["kid"].(string)
			if h["alg"] != fx.Header["alg"] || h["typ"] != fx.Header["typ"] || len(kid) != len(wantKID) {
				t.Errorf("header %v, GitLab %v", h, fx.Header)
			}

			if got, want := keysOf(p), keysOf(fx.Claims); !slices.Equal(got, want) {
				t.Errorf("claims %v, GitLab %v", got, want)
			}
			for c, want := range fx.Claims {
				if got := kind(p[c]); got != kind(want) {
					t.Errorf("%s is %s, GitLab sends %s (%v)", c, got, kind(want), want)
				}
			}
			if got, want := subShape(p["sub"]), subShape(fx.Claims["sub"]); !slices.Equal(got, want) {
				t.Errorf("sub components %v, GitLab %v", got, want)
			}
			for who, c := range map[string]map[string]any{"double": p, "GitLab": fx.Claims} {
				if ref, _ := c["ci_config_ref_uri"].(string); !configRefRE.MatchString(ref) {
					t.Errorf("%s: ci_config_ref_uri %q", who, ref)
				}
				if c["ref_path"] != refPathOf(c) {
					t.Errorf("%s: ref_path %v for ref %v of type %v", who, c["ref_path"], c["ref"], c["ref_type"])
				}
			}

			// GitLab's rule, as recorded: exp - iat is the job timeout, nbf is iat - 5.
			life := num(fx.Claims["exp"]) - num(fx.Claims["iat"])
			if life != fx.Job.TimeoutSeconds {
				t.Errorf("GitLab's exp - iat is %d, the job timeout %d", life, fx.Job.TimeoutSeconds)
			}
			if fx.Job.TimeoutSeconds == int64(DefaultLifetime/time.Second) && num(p["exp"])-num(p["iat"]) != life {
				t.Errorf("exp - iat is %d, GitLab's %d with the default job timeout", num(p["exp"])-num(p["iat"]), life)
			}
			if got, want := num(p["nbf"])-num(p["iat"]), num(fx.Claims["nbf"])-num(fx.Claims["iat"]); got != want {
				t.Errorf("nbf - iat is %d, GitLab's %d", got, want)
			}
		})
	}
}

// A job's own id_tokens replace those of default: instead of being merged with them (recorded,
// R-CI-006). This is GitLab's behaviour; the double has nothing to mirror, the test keeps the
// recorded fact from silently changing on a re-recording.
func TestRecordedIDTokensOfDefaultAreReplaced(t *testing.T) {
	fx := tokenFixtures(t)["default-id-tokens"]
	want := map[string]map[string]string{
		"claims":         {"TROVE_ID_TOKEN": "present", "ARRAY_ID_TOKEN": "present", "DEFAULT_ID_TOKEN": "absent"},
		"claims-default": {"DEFAULT_ID_TOKEN": "present", "TROVE_ID_TOKEN": "absent"},
	}
	for job, vars := range want {
		for v, state := range vars {
			if got := fx.Variables[job][v]; got != state {
				t.Errorf("job %s: %s is %q, want %q (GitLab %s)", job, v, got, state, fx.GitLabVersion)
			}
		}
	}
}

type issuerFixture struct {
	GitLabVersion     string         `json:"gitlab_version"`
	Discovery         map[string]any `json:"discovery"`
	DiscoveryResponse struct {
		ContentType  string `json:"content_type"`
		CacheControl string `json:"cache_control"`
		ETag         string `json:"etag"`
	} `json:"discovery_response"`
	JWKSResponse struct {
		ContentType  string `json:"content_type"`
		CacheControl string `json:"cache_control"`
		ETag         string `json:"etag"`
		IfNoneMatch  int    `json:"if_none_match_status"`
	} `json:"jwks_response"`
	Keys []struct {
		Members         []string `json:"members"`
		KTY             string   `json:"kty"`
		Alg             string   `json:"alg"`
		Use             string   `json:"use"`
		E               string   `json:"e"`
		ModulusBits     int      `json:"modulus_bits"`
		KIDLength       int      `json:"kid_length"`
		KIDIsThumbprint bool     `json:"kid_is_rfc7638_thumbprint"`
		SignsIDTokens   bool     `json:"signs_id_tokens"`
	} `json:"jwks_keys"`
	Signature struct {
		Alg      string `json:"alg"`
		KeyIndex int    `json:"key_index"`
		Verified bool   `json:"verified_rsassa_pkcs1_v1_5_sha256"`
	} `json:"signature"`
}

func mustRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func etagKind(v string) string {
	switch {
	case v == "":
		return "none"
	case strings.HasPrefix(v, "W/"):
		return "weak"
	}
	return "strong"
}

// The discovery document and the key set have the shape GitLab serves: the same members and JSON
// types, the same fixed lists, Content-Type, Cache-Control and ETag, and the CI signing key at the
// index where GitLab has it, signing RS256 with RSASSA-PKCS1-v1_5 as GitLab's verified token does
// (R-CI-003). Two differences are known and not mirrored: a fresh GitLab EE installation lists its
// Cloud Connector keys after the CI key, and its OpenID Connect key has 4096 bits.
func TestDoubleServesTheRecordedIssuer(t *testing.T) {
	var fx issuerFixture
	readFixture(t, "testdata/issuer.json", &fx)
	f := newFixture(t, Config{})

	var doc map[string]any
	resp := f.mustJSON(f.s.DiscoveryURL(), &doc)
	if got, want := keysOf(doc), keysOf(fx.Discovery); !slices.Equal(got, want) {
		t.Errorf("discovery members %v, GitLab %s %v", got, fx.GitLabVersion, want)
	}
	for m, want := range fx.Discovery {
		if got := kind(doc[m]); got != kind(want) {
			t.Errorf("discovery %s is %s, GitLab's %s", m, got, kind(want))
		}
		if _, list := want.([]any); list { // the fixed lists are GitLab's own
			g, _ := json.Marshal(doc[m])
			w, _ := json.Marshal(want)
			if string(g) != string(w) {
				t.Errorf("discovery %s is %s, GitLab's %s", m, g, w)
			}
		}
	}
	if doc["jwks_uri"] != doc["issuer"].(string)+JWKSPath || fx.Discovery["jwks_uri"] != fx.Discovery["issuer"].(string)+JWKSPath {
		t.Errorf("jwks_uri %v for issuer %v; GitLab's %v", doc["jwks_uri"], doc["issuer"], fx.Discovery["jwks_uri"])
	}
	d := fx.DiscoveryResponse
	if resp.Header.Get("Content-Type") != d.ContentType || resp.Header.Get("Cache-Control") != d.CacheControl || etagKind(resp.Header.Get("ETag")) != d.ETag {
		t.Errorf("discovery headers %q %q %q, GitLab's %+v", resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"), resp.Header.Get("ETag"), d)
	}

	resp, _, err := f.get(f.s.JWKSURL())
	if err != nil {
		t.Fatal(err)
	}
	j := fx.JWKSResponse
	if resp.Header.Get("Content-Type") != j.ContentType || resp.Header.Get("Cache-Control") != j.CacheControl || etagKind(resp.Header.Get("ETag")) != j.ETag {
		t.Errorf("key set headers %q %q %q, GitLab's %+v", resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"), resp.Header.Get("ETag"), j)
	}
	if j.IfNoneMatch != 0 {
		req := mustRequest(t, f.s.JWKSURL())
		req.Header.Set("If-None-Match", resp.Header.Get("ETag"))
		again, err := f.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = again.Body.Close()
		if again.StatusCode != j.IfNoneMatch {
			t.Errorf("If-None-Match answered %d, GitLab %d", again.StatusCode, j.IfNoneMatch)
		}
	}

	var raw struct {
		Keys []map[string]any `json:"keys"`
	}
	f.mustJSON(f.s.JWKSURL(), &raw)
	keys := f.jwks().Keys
	if len(fx.Keys) < 2 || len(keys) != 2 {
		t.Fatalf("GitLab lists %d keys, the double %d; both have at least the OpenID Connect key and the CI key", len(fx.Keys), len(keys))
	}
	for i, k := range keys {
		g := fx.Keys[i]
		if got := keysOf(raw.Keys[i]); !slices.Equal(got, g.Members) {
			t.Errorf("key %d members %v, GitLab's %v", i, got, g.Members)
		}
		if k.KTY != g.KTY || k.Alg != g.Alg || k.Use != g.Use || k.E != g.E || len(k.KID) != g.KIDLength || !g.KIDIsThumbprint {
			t.Errorf("key %d %+v, GitLab's %+v", i, k, g)
		}
	}
	if !fx.Signature.Verified || fx.Signature.Alg != "RS256" || !fx.Keys[fx.Signature.KeyIndex].SignsIDTokens {
		t.Fatalf("the fixture records no verified RS256 signature: %+v", fx.Signature)
	}
	h, _ := verifyJWS(t, mustMint(t, f.s, MintOptions{}), f.jwks()) // RS256, RSASSA-PKCS1-v1_5
	idx := slices.IndexFunc(keys, func(k jwk) bool { return k.KID == h["kid"] })
	if idx != fx.Signature.KeyIndex || h["alg"] != fx.Signature.Alg {
		t.Errorf("the double signs %v with key %d, GitLab %s with key %d", h["alg"], idx, fx.Signature.Alg, fx.Signature.KeyIndex)
	}
	if bits := len(dec(t, keys[idx].N)) * 8; bits != fx.Keys[fx.Signature.KeyIndex].ModulusBits {
		t.Errorf("the signing key has %d bits, GitLab's %d", bits, fx.Keys[fx.Signature.KeyIndex].ModulusBits)
	}
}

// GitLab's CI_JOB_TOKEN starts with glcbt- and holds three dot-separated parts after it, in every
// recorded pipeline; the double's job token has that shape (R-CI-004 step 2, R-CI-006).
func TestDoubleJobTokenHasTheRecordedShape(t *testing.T) {
	var fx struct {
		GitLabVersion string `json:"gitlab_version"`
		Jobs          []struct {
			Pipeline   string `json:"pipeline"`
			Prefix     string `json:"prefix"`
			Dots       int    `json:"dots"`
			ThreeParts bool   `json:"three_parts"`
		} `json:"jobs"`
	}
	readFixture(t, "testdata/job-token.json", &fx)
	if len(fx.Jobs) == 0 {
		t.Fatal("no job token recorded")
	}
	s, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	tok := mustMint(t, s, MintOptions{JobToken: true})
	for _, j := range fx.Jobs {
		if j.Prefix+"-" != JobTokenPrefix || !j.ThreeParts {
			t.Errorf("GitLab %s, pipeline %s: prefix %q, three parts %v; the double assumes %q and three parts", fx.GitLabVersion, j.Pipeline, j.Prefix, j.ThreeParts, JobTokenPrefix)
		}
		if strings.Count(tok, ".") != j.Dots || !strings.HasPrefix(tok, j.Prefix+"-") {
			t.Errorf("the double's job token has %d dots, GitLab's %d", strings.Count(tok, "."), j.Dots)
		}
	}
}
