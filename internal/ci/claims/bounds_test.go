package claims_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
)

// The bounds of the exchange's evaluation (Q-CI-49) and the bundle of version 2 (Q-CI-47).

// R-CI-010, Q-CI-49: a literal, a claim name, a path and a whole condition have a size bound at save, so a stored
// condition costs the exchange a bounded number of bytes to compare.
func TestR_CI_010_TheSizeBoundsOfACondition(t *testing.T) {
	big := strings.Repeat("a", claims.MaxLiteralLength)
	ok := []struct{ name, cond string }{
		{"an equals literal of 1024 characters", `{"claim":"ref","equals":"` + big + `"}`},
		{"an in literal of 1024 characters", `{"claim":"ref","in":["x","` + big + `"]}`},
		{"a claim name of 256 characters", `{"claim":"` + strings.Repeat("c", claims.MaxClaimNameLength) + `","present":true}`},
		{"a path of 16 members", `{"path":[` + strings.TrimSuffix(strings.Repeat(`"p",`, claims.MaxPathMembers), ",") + `],"present":true}`},
	}
	for _, c := range ok {
		t.Run("Q-CI-49 "+c.name+" is valid", func(t *testing.T) {
			if err := parse(t, c.cond).Validate(claims.DefaultLimits()); err != nil {
				t.Fatal(err)
			}
		})
	}
	// 64 leaves of 256 values of 64 characters each are 1 MiB of literals: more than a condition may hold.
	var huge []string
	for i := 0; i < 64; i++ {
		huge = append(huge, `{"claim":"project_path","in":[`+strings.TrimSuffix(strings.Repeat(`"`+strings.Repeat("p", 64)+`",`, 256), ",")+`]}`)
	}
	bad := []struct{ name, cond, pointer string }{
		{"an equals literal of 1025 characters", `{"claim":"ref","equals":"` + big + `a"}`, "/equals"},
		{"an in literal of 1025 characters", `{"claim":"ref","in":["x","` + big + `a"]}`, "/in/1"},
		{"a claim name of 257 characters", `{"claim":"` + strings.Repeat("c", claims.MaxClaimNameLength+1) + `","present":true}`, "/claim"},
		{"a path member of 257 characters", `{"path":["a","` + strings.Repeat("c", claims.MaxClaimNameLength+1) + `"],"present":true}`, "/path/1"},
		{"a path of 17 members", `{"path":[` + strings.TrimSuffix(strings.Repeat(`"p",`, claims.MaxPathMembers+1), ",") + `],"present":true}`, "/path"},
		{"a condition of more than 64 KiB", `{"any":[` + strings.Join(huge, ",") + `]}`, ""},
	}
	for _, c := range bad {
		t.Run("Q-CI-49 "+c.name+" is condition_invalid", func(t *testing.T) {
			err := parse(t, c.cond).Validate(claims.DefaultLimits())
			if err == nil {
				t.Fatal("valid")
			}
			r, ptr := reasonOf(t, err)
			if r != reason.ConditionInvalid || ptr != c.pointer {
				t.Fatalf("%s at %q, want condition_invalid at %q (%v)", r, ptr, c.pointer, err)
			}
		})
	}
}

// R-CI-010, Q-CI-49: the exchange evaluates a compiled condition without a trace; it decides exactly as the traced
// evaluation of the dry run does, on every operator, on arrays, on missing and null claims and on the nodes Validate
// would refuse.
func TestR_CI_010_TheCompiledConditionDecidesAsTheTrace(t *testing.T) {
	c := tokenClaims()
	c["groups"] = []any{"dev", "ops"}
	c["nested"] = map[string]any{"a": map[string]any{"b": "x"}}
	c["nothing"] = nil
	conds := []string{
		publishMatch,
		`{"claim":"ref","regex":"v[0-9]+\\.[0-9]+\\.[0-9]+"}`,
		`{"claim":"ref","regex":"V1.*","ignoreCase":true}`,
		`{"claim":"ref","regex":"V1.*"}`,
		`{"claim":"project_path","glob":"acme/**"}`,
		`{"claim":"project_path","glob":"ACME/**","ignoreCase":true}`,
		`{"claim":"groups","in":["ops"]}`,
		`{"claim":"groups","equals":"OPS","ignoreCase":true}`,
		`{"claim":"runner_id","equals":17}`,
		`{"claim":"ref_protected","equals":true}`,
		`{"claim":"absent","present":false}`,
		`{"claim":"nothing","present":true}`,
		`{"not":{"claim":"absent","equals":"x"}}`,
		`{"path":["nested","a","b"],"equals":"x"}`,
		`{"path":["nested","a"],"present":true}`,
		`{"any":[{"claim":"ref","equals":"main"},{"claim":"ref_type","equals":"tag"}]}`,
		`{"all":[{"claim":"ref","equals":"main"},{"claim":"ref_type","equals":"tag"}]}`,
		`{"all":[{"any":[{"claim":"a","present":true},{"not":{"claim":"b","present":true}}]},{"claim":"pipeline_source","in":["push","web"]}]}`,
	}
	for _, s := range conds {
		n := parse(t, s)
		if got, want := claims.CompileCondition(n).Matches(c), n.Evaluate(c).Matched; got != want {
			t.Errorf("%s: compiled %v, traced %v", s, got, want)
		}
	}
	// Nodes built in Go that Validate refuses evaluate false both ways.
	for name, n := range map[string]claims.Node{
		"the empty node":          {},
		"an empty all":            {All: []claims.Node{}},
		"an empty any":            {Any: []claims.Node{}},
		"a leaf and a combiner":   {Claim: "ref", Not: &claims.Node{Claim: "x", Present: new(bool)}},
		"a leaf without operator": {Claim: "ref"},
	} {
		if claims.CompileCondition(n).Matches(c) || n.Evaluate(c).Matched {
			t.Errorf("%s matched", name)
		}
	}
	if (claims.Condition{}).Matches(c) {
		t.Error("a condition that was never compiled matched")
	}
}

// Q-CI-49: the compiled condition holds its regular expressions; an evaluation compiles nothing, so a regex leaf on
// a scalar claim costs no allocation.
func TestR_CI_010_TheCompiledConditionCompilesNothingPerEvaluation(t *testing.T) {
	c := tokenClaims()
	cond := claims.CompileCondition(parse(t, `{"all":[{"claim":"ref","regex":"v[0-9]+\\.[0-9]+\\.[0-9]+"},{"claim":"namespace_id","in":["4711","4712"]}]}`))
	if !cond.Matches(c) {
		t.Fatal("no match")
	}
	if n := testing.AllocsPerRun(100, func() { cond.Matches(c) }); n > 2 {
		t.Fatalf("%v allocations per evaluation", n)
	}
}

// Q-CI-17 as amended by Q-CI-47: the bundle is version 2 with a per-contribution expiry and the authorization epoch
// of the exchange; a token issued before carries version 1, which still decodes, and nothing else does.
func TestQ_CI_47_TheBundleOfVersionTwoAndTheBundleOfVersionOne(t *testing.T) {
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	sys := claims.Freeze("ci:gitlab-com/acme-read", "m-1", "system", nil, claims.Rendered{Groups: []string{"readers"}}, nil)
	proj := claims.Freeze("ci:gitlab-com/web-read", "m-2", "project:web", &claims.FrozenProject{Key: "web", ID: "p-7"}, claims.Rendered{Groups: []string{"ci"}}, nil)
	proj.ExpiresAt = &at
	f := frozenOf(t, sys, proj)
	f.AuthzEpoch = 42
	if claims.FrozenVersion != 2 || f.Version != 2 {
		t.Fatalf("the bundle this code writes is version %d", f.Version)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	back, err := claims.DecodeFrozen(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back.AuthzEpoch != 42 || back.Contributions[0].ExpiresAt != nil || !back.Contributions[1].ExpiresAt.Equal(at) {
		t.Fatalf("%+v", back)
	}
	if back.Contributions[1].ExpiredAt(at.Add(-time.Second)) || !back.Contributions[1].ExpiredAt(at) || back.Contributions[0].ExpiredAt(at.Add(time.Hour)) {
		t.Fatal("ExpiredAt")
	}
	v1 := `{"version":1,"issuer":"gitlab-com","issuerId":"i-1","projectId":"4242","mappings":["ci:gitlab-com/acme-read"],"mappingIds":["m-1"],` +
		`"contributions":[{"ref":"ci:gitlab-com/acme-read","id":"m-1","tier":"system","groups":["readers"]}]}`
	old, err := claims.DecodeFrozen([]byte(v1))
	if err != nil {
		t.Fatalf("a bundle of version 1 does not decode: %v", err)
	}
	if old.Version != claims.FrozenVersion1 || old.AuthzEpoch != 0 || old.Contributions[0].ExpiresAt != nil {
		t.Fatalf("%+v", old)
	}
	for name, s := range map[string]string{
		"version 3":                       strings.Replace(v1, `"version":1`, `"version":3`, 1),
		"version 1 with an epoch":         strings.Replace(v1, `"version":1`, `"version":1,"authzEpoch":7`, 1),
		"version 1 with an expiry":        strings.Replace(v1, `"tier":"system"`, `"tier":"system","expiresAt":"2026-11-03T12:00:00Z"`, 1),
		"version 2 with a negative epoch": strings.Replace(v1, `"version":1`, `"version":2,"authzEpoch":-1`, 1),
	} {
		if _, err := claims.DecodeFrozen([]byte(s)); err == nil {
			t.Errorf("%s decoded", name)
		}
	}
}
