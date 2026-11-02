package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// tokenCase is one fixture: the ID token in variable of job in the pipeline.
type tokenCase struct {
	name, pipeline, job, variable, description string
	audience                                   any
}

var cases = []tokenCase{
	{"push-protected-branch", "main", "claims", "TROVE_ID_TOKEN", "A push pipeline on the protected default branch main; aud a string.", audience},
	{"push-protected-branch-aud-array", "main", "claims", "ARRAY_ID_TOKEN", "The same job's second ID token, aud an array of two.", []string{audience, secondAudience}},
	{"default-id-tokens", "main", "claims-default", "DEFAULT_ID_TOKEN", "A job without id_tokens of its own gets the id_tokens of default:; a job with its own does not (see variables).", defaultAudience},
	{"job-timeout", "main", "claims-timeout", "TROVE_ID_TOKEN", "A job with timeout: 15m on main.", audience},
	{"environment-start", "main", "environment-start", "TROVE_ID_TOKEN", "A job with environment production (protected where the licence allows), action start, on main.", audience},
	{"environment-prepare", "main", "environment-prepare", "TROVE_ID_TOKEN", "A job with environment production, action prepare, on main.", audience},
	{"environment-unprotected", "main", "environment-unprotected", "TROVE_ID_TOKEN", "A job with the unprotected environment staging, action start, on main.", audience},
	{"push-unprotected-branch", "branch", "claims", "TROVE_ID_TOKEN", "A push pipeline on an unprotected branch.", audience},
	{"push-protected-tag", "tag", "claims", "TROVE_ID_TOKEN", "A push pipeline on a tag protected by the rule v*.", audience},
	{"push-protected-tag-environment", "tag", "environment-start", "TROVE_ID_TOKEN", "A job with environment production, action start, on the protected tag: the shape of the double's DefaultClaims.", audience},
	{"merge-request-same-project", "mr", "claims", "TROVE_ID_TOKEN", "A merge request pipeline from an unprotected branch of the same project.", audience},
	{"merge-request-fork-in-fork", "fork-in-fork", "claims", "TROVE_ID_TOKEN", "A merge request from a branch of the fork partner/invoice-api, opened by a user who is only a Reporter of the parent: GitLab runs its pipeline in the fork.", audience},
	{"merge-request-fork-in-parent", "fork-in-parent", "claims", "TROVE_ID_TOKEN", "The same merge request's pipeline, run in the parent project billing/invoice-api by a member of the parent (POST /projects/:id/merge_requests/:iid/pipelines).", audience},
	{"merge-request-fork-by-parent-member", "fork-by-member", "claims", "TROVE_ID_TOKEN", "A merge request from a branch of the fork opened by a member of the parent who may run pipelines there: GitLab runs its pipeline in the parent without being asked.", audience},
	{"schedule", "schedule", "claims", "TROVE_ID_TOKEN", "A pipeline started by playing a pipeline schedule on main.", audience},
	{"api", "api", "claims", "TROVE_ID_TOKEN", "A pipeline started through POST /projects/:id/pipeline on main.", audience},
}

type tokenFixture struct {
	GitLabVersion string                       `json:"gitlab_version"`
	Recorded      string                       `json:"recorded"`
	Case          string                       `json:"case"`
	Description   string                       `json:"description"`
	Job           jobFacts                     `json:"job"`
	Header        map[string]any               `json:"header"`
	Claims        map[string]any               `json:"claims"`
	Variables     map[string]map[string]string `json:"variables,omitempty"`
}

type jobFacts struct {
	Name     string `json:"name"`
	Pipeline string `json:"pipeline"`
	Variable string `json:"variable"`
	Audience any    `json:"audience"`
	// TimeoutSeconds is CI_JOB_TIMEOUT; ScriptStartedAt the job script's clock at its first line.
	TimeoutSeconds  int64 `json:"timeout_seconds"`
	ScriptStartedAt int64 `json:"script_started_at"`
	// Parts is the number of dot-separated parts of the token (counted in the job).
	Parts int `json:"parts"`
}

type issuerFixture struct {
	GitLabVersion string `json:"gitlab_version"`
	Recorded      string `json:"recorded"`
	// LicensePlan is the plan of the instance's licence, "" without one (then environment_protected
	// is "false" even for a protected environment).
	LicensePlan string `json:"license_plan"`
	*issuerRecord
}

type jobTokenFixture struct {
	GitLabVersion string             `json:"gitlab_version"`
	Recorded      string             `json:"recorded"`
	Description   string             `json:"description"`
	Jobs          []jobTokenObserved `json:"jobs"`
}

type jobTokenObserved struct {
	Pipeline   string `json:"pipeline"`
	Length     int    `json:"length"`
	Prefix     string `json:"prefix"`
	Dots       int    `json:"dots"`
	ThreeParts bool   `json:"three_parts"`
}

// anonymizer removes what names the instance or its user from a fixture.
type anonymizer struct {
	hosts     *strings.Replacer
	exact     map[string]string
	kids      map[string]string
	forbidden []string
}

const exampleHost = "gitlab.example.com"

// reserved reports whether a domain is one of the names RFC 2606 and RFC 6761 reserve.
func reserved(domain string) bool {
	d := strings.ToLower(domain)
	for _, r := range []string{"example.com", "example.net", "example.org", "localhost"} {
		if d == r || strings.HasSuffix(d, "."+r) {
			return true
		}
	}
	for _, tld := range []string{".test", ".example", ".invalid", ".localhost"} {
		if strings.HasSuffix(d, tld) {
			return true
		}
	}
	return net.ParseIP(d) != nil
}

// registrable is the last two labels of a host name, the part that names its owner.
func registrable(host string) string {
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return host
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

func placeholderKID(i int) string {
	sum := sha256.Sum256([]byte("oidcstub fixture key " + strconv.Itoa(i)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// person is a user of the recording and the neutral login and name it becomes.
type person struct {
	u           user
	login, name string
}

func newAnonymizer(base string, kids map[string]int, people ...person) *anonymizer {
	u, _ := url.Parse(base)
	host := u.Hostname()
	a := &anonymizer{
		hosts: strings.NewReplacer(base, "https://"+exampleHost, u.Host, exampleHost, host, exampleHost),
		exact: map[string]string{}, kids: map[string]string{},
	}
	if !reserved(host) {
		a.forbidden = append(a.forbidden, strings.ToLower(host), strings.ToLower(registrable(host)))
	}
	for _, p := range people {
		if p.u.Username != "" {
			a.exact[p.u.Username] = p.login
		}
		if p.u.Name != "" {
			a.exact[p.u.Name] = p.name
			if len(p.u.Name) >= 4 {
				a.forbidden = append(a.forbidden, strings.ToLower(p.u.Name))
			}
		}
		for _, e := range []string{p.u.Email, p.u.PublicEmail, p.u.CommitEmail} {
			if e == "" {
				continue
			}
			a.exact[e] = p.login + "@example.com"
			a.forbidden = append(a.forbidden, strings.ToLower(e))
			if at := strings.LastIndex(e, "@"); at >= 0 && !reserved(e[at+1:]) {
				a.forbidden = append(a.forbidden, strings.ToLower(e[at+1:]))
			}
		}
	}
	for kid, i := range kids {
		a.kids[kid] = placeholderKID(i)
	}
	return a
}

func (a *anonymizer) value(v any) any {
	switch t := v.(type) {
	case string:
		if r, ok := a.exact[t]; ok {
			return r
		}
		return a.hosts.Replace(t)
	case map[string]any:
		for k, x := range t {
			t[k] = a.value(x)
		}
	case []any:
		for i := range t {
			t[i] = a.value(t[i])
		}
	}
	return v
}

// claims anonymizes a payload. The external identities of the user (user_identities) name
// accounts at other providers and become placeholders.
func (a *anonymizer) claims(c map[string]any) {
	a.value(c)
	if ids, ok := c["user_identities"].([]any); ok {
		for i, id := range ids {
			if m, ok := id.(map[string]any); ok {
				if _, ok := m["extern_uid"]; ok {
					m["extern_uid"] = "external-uid-" + strconv.Itoa(i+1)
				}
			}
		}
	}
}

func (a *anonymizer) header(h map[string]any) {
	if kid, ok := h["kid"].(string); ok {
		if p, ok := a.kids[kid]; ok {
			h["kid"] = p
		} else {
			// A key the published set does not list: keep the shape, not the value.
			h["kid"] = placeholderKID(-1)[:min(len(kid), 43)]
		}
	}
	a.value(h)
}

func (a *anonymizer) check(name string, data []byte) error {
	low := bytes.ToLower(data)
	for _, f := range a.forbidden {
		if f != "" && bytes.Contains(low, []byte(f)) {
			return fmt.Errorf("%s still contains a value that names the instance or its user; not written", name)
		}
	}
	return nil
}

type writer struct {
	dir, version, day string
	an                *anonymizer
}

func (w writer) write(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := w.an.check(name, data); err != nil {
		return err
	}
	path := filepath.Join(w.dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // a fixture of the repository, readable by design
		return err
	}
	logf("wrote %s", path)
	return nil
}

func decodeJSON(s string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func presence(j *jobLog, names ...string) map[string]string {
	out := map[string]string{}
	for _, n := range names {
		switch t := j.tokens[n]; {
		case t == nil:
			out[n] = "not printed"
		case t.absent:
			out[n] = "absent"
		default:
			out[n] = "present"
		}
	}
	return out
}

func (w writer) writeAll(iss *issuerRecord, logs map[string]map[string]*jobLog, env setup) error {
	if err := os.MkdirAll(w.dir, 0o750); err != nil {
		return err
	}
	w.an.value(iss.Discovery)
	if err := w.write("issuer.json", issuerFixture{GitLabVersion: w.version, Recorded: w.day, LicensePlan: env.licensePlan, issuerRecord: iss}); err != nil {
		return err
	}
	var missing []string
	for _, tc := range cases {
		j := logs[tc.pipeline][tc.job]
		if j == nil || j.tokens[tc.variable] == nil || j.tokens[tc.variable].absent {
			missing = append(missing, tc.name)
			continue
		}
		t := j.tokens[tc.variable]
		header, err := decodeJSON(t.header)
		if err != nil {
			return fmt.Errorf("%s: header: %w", tc.name, err)
		}
		claims, err := decodeJSON(t.payload)
		if err != nil {
			return fmt.Errorf("%s: payload: %w", tc.name, err)
		}
		w.an.header(header)
		w.an.claims(claims)
		f := tokenFixture{
			GitLabVersion: w.version, Recorded: w.day, Case: tc.name, Description: tc.description,
			Job: jobFacts{
				Name: tc.job, Pipeline: tc.pipeline, Variable: tc.variable, Audience: tc.audience,
				TimeoutSeconds: j.timeout, ScriptStartedAt: j.started, Parts: t.dots + 1,
			},
			Header: header, Claims: claims,
		}
		if tc.name == "default-id-tokens" {
			f.Variables = map[string]map[string]string{"claims-default": presence(j, "DEFAULT_ID_TOKEN", "TROVE_ID_TOKEN")}
			if own := logs["main"]["claims"]; own != nil {
				f.Variables["claims"] = presence(own, "TROVE_ID_TOKEN", "ARRAY_ID_TOKEN", "DEFAULT_ID_TOKEN")
			}
		}
		if err := w.write("id-token-"+tc.name+".json", f); err != nil {
			return err
		}
	}
	if err := w.writeJobTokens(logs); err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("no ID token recorded for %s (see the job logs)", strings.Join(missing, ", "))
	}
	return nil
}

func (w writer) writeJobTokens(logs map[string]map[string]*jobLog) error {
	f := jobTokenFixture{
		GitLabVersion: w.version, Recorded: w.day,
		Description: "CI_JOB_TOKEN of the job claims in each pipeline: its length, the prefix up to the first \"-\" or \".\", and the number of dots. Nothing else of the token is recorded.",
	}
	keys := make([]string, 0, len(logs))
	for k := range logs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		j := logs[k]["claims"]
		if j == nil || j.jobToken == "" {
			continue
		}
		fields := strings.Fields(j.jobToken)
		if len(fields) != 3 {
			return errors.New("unexpected CI_JOB_TOKEN shape line in pipeline " + k)
		}
		length, _ := strconv.Atoi(fields[0])
		dots, _ := strconv.Atoi(fields[2])
		prefix := fields[1]
		if prefix == "-" {
			prefix = ""
		}
		if len(prefix) > 16 {
			// The prefix of a bare JWT is its header; record that it is one, not the value.
			prefix = "(the base64url header of a JWT, " + strconv.Itoa(len(prefix)) + " characters)"
		}
		f.Jobs = append(f.Jobs, jobTokenObserved{Pipeline: k, Length: length, Prefix: prefix, Dots: dots, ThreeParts: dots == 2})
	}
	return w.write("job-token.json", f)
}
