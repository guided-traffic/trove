//go:build integration

// Package packages tests the package screens' API (R-API-029, R-API-030) against the
// integration stack: PostgreSQL, the object store and in-process replicas started
// through the composition root, with npm publishes as the data. Nothing in the path
// of a request is stood in for: the real chain, the real authentication and
// authorization, the grant index and the npm format run, and every answer of the
// management API is validated against the OpenAPI document by the harness.
package packages

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // dist.shasum is a SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// spec describes one npm version to publish.
type spec struct {
	Name, Version string
	Tag           string
	Description   string
	Keywords      []string
	License       string
	// Readme is the text of README.md in the tarball; "-" leaves the file out.
	Readme string
	// Extra members of the manifest and of the tarball's package.json.
	Extra map[string]any
}

// env is one installation with two npm repositories and five callers:
//
//	alice  reads and deploys on both repositories
//	bob    reads npm-local only
//	carol  reads npm-team only for the packages @team-a/*
//	dave   holds no grant
//	erin   reads both repositories
//	bot    a service account that reads both repositories; its token is bound to the
//	       endpoint partners (a further endpoint accepts bound service-account tokens only)
//
// npm-old and npm-new hold the versions of the acceptance case of R-API-029.
type env struct {
	t  *testing.T
	st *testenv.Stack
	r  *testenv.Replica

	alice, bob, carol, dave, erin, bot testenv.Person
	tokA, tokB, tokC, tokD, tokE       string
	// tokBot is a token of the service account bot, bound to the endpoint partners.
	tokBot string
}

func newEnv(t *testing.T, replicaEnv ...string) *env {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "packages"})
	e := &env{t: t, st: st}
	e.alice, e.bob, e.carol, e.dave, e.erin = st.User("alice"), st.User("bob"), st.User("carol"), st.User("dave"), st.User("erin")
	st.Repository("npm-local", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	st.Repository("npm-team", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	e.bot = st.ServiceAccount("bot")
	st.Repository("npm-old", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	st.Repository("npm-new", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	st.Policy("writers", []string{"read", "deploy", "delete"}, []string{"npm-local", "npm-team", "npm-old", "npm-new"}, e.alice)
	st.Policy("old-readers", []string{"read"}, []string{"npm-old"}, e.bob)
	st.Policy("bot-readers", []string{"read"}, []string{"npm-local", "npm-team"}, e.bot)
	st.Policy("local-readers", []string{"read"}, []string{"npm-local"}, e.bob)
	st.Policy("all-readers", []string{"read"}, []string{"npm-local", "npm-team"}, e.erin)
	e.narrowed("team-a-only", "npm-team", e.carol, "@team-a/*")
	scp := scope.Default()
	e.tokA, e.tokB, e.tokC, e.tokD, e.tokE = st.Token(e.alice, scp), st.Token(e.bob, scp), st.Token(e.carol, scp), st.Token(e.dave, scp), st.Token(e.erin, scp)
	e.tokBot = st.Token(e.bot, scope.Scope{Inherit: true, Endpoints: []string{"partners"}})
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

// publish stores one version through the npm protocol as alice.
func (e *env) publish(repo string, s spec) {
	e.t.Helper()
	if s.Tag == "" {
		s.Tag = "latest"
	}
	a := e.st.Do(e.r, "PUT", "/api/npm/"+repo+"/"+strings.ReplaceAll(s.Name, "/", "%2f"), e.tokA, publishBody(e.t, s))
	if a.Status != 201 {
		e.t.Fatalf("publish %s@%s to %s: %d %s", s.Name, s.Version, repo, a.Status, a.Body)
	}
}

func publishBody(t testing.TB, s spec) []byte {
	t.Helper()
	members := map[string]any{"name": s.Name, "version": s.Version}
	if s.Description != "" {
		members["description"] = s.Description
	}
	if len(s.Keywords) > 0 {
		members["keywords"] = s.Keywords
	}
	if s.License != "" {
		members["license"] = s.License
	}
	for k, v := range s.Extra {
		members[k] = v
	}
	raw, _ := json.Marshal(members)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	add := func(name, content string) {
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(content))
	}
	add("package.json", string(raw))
	switch s.Readme {
	case "-":
	case "":
		add("README.md", "# "+s.Name+"\n")
	default:
		add("README.md", s.Readme)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	tgz := buf.Bytes()
	s512 := sha512.Sum512(tgz)
	s1 := sha1.Sum(tgz) //nolint:gosec // see the import
	integrity, shasum := "sha512-"+base64.StdEncoding.EncodeToString(s512[:]), hex.EncodeToString(s1[:])
	base := s.Name[strings.LastIndex(s.Name, "/")+1:]
	file := fmt.Sprintf("%s-%s.tgz", s.Name, s.Version)
	m := map[string]any{
		"name": s.Name, "version": s.Version, "_id": s.Name + "@" + s.Version,
		"dist": map[string]any{"integrity": integrity, "shasum": shasum, "tarball": "http://x/" + s.Name + "/-/" + base + "-" + s.Version + ".tgz"},
	}
	for k, v := range members {
		m[k] = v
	}
	doc := map[string]any{
		"_id": s.Name, "name": s.Name, "dist-tags": map[string]string{s.Tag: s.Version},
		"versions": map[string]any{s.Version: m}, "access": nil,
		"_attachments": map[string]any{file: map[string]any{
			"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tgz), "length": len(tgz),
		}},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// seed publishes the common data. The versions of npm-team are newer than the ones of
// npm-local, so a caller who reads only npm-local sees an older latest version.
func (e *env) seed() {
	e.t.Helper()
	ui := func(v, tag string) spec {
		return spec{Name: "@team-a/ui", Version: v, Tag: tag, Description: "Shared UI components", Keywords: []string{"ui", "components"}, License: "MIT"}
	}
	e.publish("npm-local", ui("1.3.0", ""))
	e.publish("npm-local", ui("1.4.0", ""))
	e.publish("npm-local", ui("2.0.0-beta.1", "next"))
	e.publish("npm-local", spec{Name: "left-pad", Version: "1.0.0", Description: "pads strings on the left", Keywords: []string{"string"}})
	e.publish("npm-team", ui("1.4.0", ""))
	e.publish("npm-team", ui("1.5.0", ""))
	e.publish("npm-team", spec{Name: "@team-b/core", Version: "0.1.0", Description: "the core of team b, it holds the needle-word"})
	e.publish("npm-team", spec{Name: "secret-tool", Version: "9.9.9", Keywords: []string{"needle-kw"}})
}

// get sends a GET as a caller ("" for anonymous).
func (e *env) get(token, path string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(e.r, "GET", path, token, nil, hdr...)
}

// page is the envelope of a list.
type page struct {
	Items      []map[string]any `json:"items"`
	NextCursor *string          `json:"nextCursor"`
}

func (e *env) page(token, path string) page {
	e.t.Helper()
	a := e.get(token, path)
	if a.Status != 200 {
		e.t.Fatalf("GET %s: %d %s", path, a.Status, a.Body)
	}
	var p page
	if err := json.Unmarshal(a.Body, &p); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// all follows the cursor of a list and returns every item.
func (e *env) all(token, path string) []map[string]any {
	e.t.Helper()
	var out []map[string]any
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	cursor := ""
	for range 100 {
		u := path
		if cursor != "" {
			u += sep + "cursor=" + url.QueryEscape(cursor)
		}
		p := e.page(token, u)
		out = append(out, p.Items...)
		if p.NextCursor == nil {
			return out
		}
		cursor = *p.NextCursor
	}
	e.t.Fatalf("GET %s does not end", path)
	return nil
}

// names returns the "name" member of each item.
func names(items []map[string]any) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i], _ = it["name"].(string)
	}
	return out
}

// pkgByName reads one package of a list by its name.
func pkgByName(items []map[string]any, name string) map[string]any {
	for _, it := range items {
		if it["name"] == name {
			return it
		}
	}
	return nil
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

// idOf resolves a package by lookup and returns its id.
func (e *env) idOf(token, name string) string {
	e.t.Helper()
	a := e.get(token, "/api/v1/packages/lookup?format=npm&name="+url.QueryEscape(name))
	if a.Status != 200 {
		e.t.Fatalf("lookup %s: %d %s", name, a.Status, a.Body)
	}
	id, _ := a.JSON(e.t)["id"].(string)
	return id
}

func mustJSON(t testing.TB, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
}
