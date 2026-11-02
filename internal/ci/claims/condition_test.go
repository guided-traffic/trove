package claims_test

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
)

// tokenClaims is the example job of R-CI-006 in GitLab's spelling: IDs and booleans as strings.
func tokenClaims() claims.Claims {
	return claims.Claims{
		"iss": "https://gitlab.example.com", "aud": "https://packages.example.com", "sub": "project_path:acme/billing/invoice-api:ref_type:tag:ref:v1.4.0",
		"project_id": "4242", "namespace_id": "4711", "project_path": "acme/billing/invoice-api", "namespace_path": "acme/billing",
		"ref": "v1.4.0", "ref_type": "tag", "ref_protected": "true", "pipeline_source": "push",
		"environment_action": "start", "runner_id": json.Number("17"), "user_login": "jdoe",
		"jti": "x", "exp": json.Number("1792940000"), "iat": json.Number("1792936400"),
	}
}

func parse(t testing.TB, s string) claims.Node {
	t.Helper()
	n, err := claims.ParseCondition([]byte(s))
	if err != nil {
		t.Fatalf("ParseCondition(%s): %v", s, err)
	}
	return n
}

func reasonOf(t testing.TB, err error) (reason.Reason, string) {
	t.Helper()
	var e *claims.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not a *claims.Error", err)
	}
	return e.Reason, e.Pointer
}

func matches(t testing.TB, cond string, c claims.Claims) bool {
	t.Helper()
	n := parse(t, cond)
	if err := n.Validate(claims.Limits{}); err != nil {
		t.Fatalf("Validate(%s): %v", cond, err)
	}
	return n.Evaluate(c).Matched
}

func TestR_CI_010_TheExampleConditionMatches(t *testing.T) {
	cond := `{"all": [
		{"claim": "namespace_id", "in": ["4711", "4712"]},
		{"claim": "project_path", "glob": "acme/**"},
		{"claim": "ref_type", "equals": "tag"},
		{"claim": "ref", "regex": "v[0-9]+\\.[0-9]+\\.[0-9]+"},
		{"claim": "ref_protected", "equals": true},
		{"any": [{"claim": "pipeline_source", "equals": "push"}, {"claim": "pipeline_source", "equals": "web"}]},
		{"not": {"claim": "environment_action", "in": ["prepare", "verify", "access"]}}
	]}`
	if !matches(t, cond, tokenClaims()) {
		t.Fatal("the example condition of R-CI-010 does not match the example job")
	}
	c := tokenClaims()
	c["environment_action"] = "prepare"
	if matches(t, cond, c) {
		t.Fatal("matched an environment_action prepare")
	}
}

func TestR_CI_010_Normalization(t *testing.T) {
	cases := []struct {
		name  string
		cond  string
		value any
		want  bool
	}{
		{"the literal true and the claim \"true\"", `{"claim":"v","equals":true}`, "true", true},
		{"the literal \"true\" and the claim true", `{"claim":"v","equals":"true"}`, true, true},
		{"the literal true and the claim true", `{"claim":"v","equals":true}`, true, true},
		{"the literal false and the claim \"true\"", `{"claim":"v","equals":false}`, "true", false},
		{"the literal 20 and the claim \"20\"", `{"claim":"v","equals":20}`, "20", true},
		{"the literal \"20\" and the claim 20", `{"claim":"v","equals":"20"}`, json.Number("20"), true},
		{"the literal 20 and the claim 20 decoded as a float", `{"claim":"v","equals":20}`, float64(20), true},
		{"the literal 20 and the claim \"020\"", `{"claim":"v","equals":20}`, "020", false},
		{"the literal -0 and the claim 0", `{"claim":"v","equals":0}`, json.Number("-0"), true},
		{"a big integer stays exact", `{"claim":"v","equals":"123456789012345678901234567890"}`, json.Number("123456789012345678901234567890"), true},
		{"a non-integer claim has no value (Q-CI-28)", `{"claim":"v","equals":"20.5"}`, json.Number("20.5"), false},
		{"an exponent is not an integer (Q-CI-28)", `{"claim":"v","equals":"2e1"}`, json.Number("2e1"), false},
		{"a non-integer claim is present (Q-CI-28)", `{"claim":"v","present":true}`, json.Number("20.5"), true},
		{"strings compare byte for byte", `{"claim":"v","equals":"Café"}`, "Café", false},
		{"case-sensitive by default", `{"claim":"v","equals":"Push"}`, "push", false},
		{"ignoreCase folds", `{"claim":"v","equals":"PUSH","ignoreCase":true}`, "push", true},
		{"ignoreCase folds simply, not fully (ß is not ss)", `{"claim":"v","equals":"SS","ignoreCase":true}`, "ß", false},
		{"ignoreCase on in", `{"claim":"v","in":["A","B"],"ignoreCase":true}`, "b", true},
	}
	for _, c := range cases {
		t.Run("R-CI-010 "+c.name, func(t *testing.T) {
			if got := matches(t, c.cond, claims.Claims{"v": c.value}); got != c.want {
				t.Fatalf("matched %v, want %v", got, c.want)
			}
		})
	}
}

func TestR_CI_010_ArrayClaims(t *testing.T) {
	c := claims.Claims{
		"aud":    []any{"https://packages.example.com", "npm:packages.example.com"},
		"groups": []any{map[string]any{"name": "a"}, []any{"a"}, nil, "b", true, json.Number("7")},
		"empty":  []any{},
	}
	cases := []struct {
		cond string
		want bool
	}{
		{`{"claim":"aud","equals":"npm:packages.example.com"}`, true},
		{`{"claim":"aud","glob":"https://*"}`, true},
		{`{"claim":"aud","equals":"https://vault.example.com"}`, false},
		{`{"not":{"claim":"aud","equals":"https://vault.example.com"}}`, true},
		{`{"claim":"groups","equals":"a"}`, false},
		{`{"claim":"groups","in":["b"]}`, true},
		{`{"claim":"groups","equals":true}`, true},
		{`{"claim":"groups","equals":7}`, true},
		{`{"claim":"empty","present":true}`, true},
		{`{"claim":"empty","glob":"*"}`, false},
	}
	for _, tc := range cases {
		t.Run("R-CI-010 an array claim is multi-valued: "+tc.cond, func(t *testing.T) {
			if got := matches(t, tc.cond, c); got != tc.want {
				t.Fatalf("matched %v, want %v", got, tc.want)
			}
		})
	}
	vals, ok := c.Normalized("groups", nil)
	if !ok || strings.Join(vals, ",") != "b,true,7" {
		t.Fatalf("R-CI-010 objects and arrays in an array never contribute: %v %v", vals, ok)
	}
}

func TestR_CI_010_NullAndMissingClaims(t *testing.T) {
	c := claims.Claims{"environment": nil, "ref": "main"}
	cases := []struct {
		name string
		cond string
		want bool
	}{
		{"a leaf on a missing claim is false", `{"claim":"environment_action","equals":"start"}`, false},
		{"not around a leaf on a missing claim is true", `{"not":{"claim":"environment_action","equals":"start"}}`, true},
		{"not around in on a missing claim is true", `{"not":{"claim":"environment_action","in":["prepare"]}}`, true},
		{"a leaf on a null claim is false", `{"claim":"environment","glob":"*"}`, false},
		{"not around a leaf on a null claim is true", `{"not":{"claim":"environment","regex":".*"}}`, true},
		{"present false holds on a missing claim", `{"claim":"environment_action","present":false}`, true},
		{"present false holds on a null claim", `{"claim":"environment","present":false}`, true},
		{"present true fails on a null claim", `{"claim":"environment","present":true}`, false},
		{"present true holds on a present claim", `{"claim":"ref","present":true}`, true},
		{"a regex that matches the empty string still needs the claim", `{"claim":"nothing","regex":".*"}`, false},
	}
	for _, tc := range cases {
		t.Run("R-CI-010 "+tc.name, func(t *testing.T) {
			if got := matches(t, tc.cond, c); got != tc.want {
				t.Fatalf("matched %v, want %v", got, tc.want)
			}
		})
	}
}

func TestR_CI_010_ObjectsOnlyThroughPath(t *testing.T) {
	c := claims.Claims{
		"job_config": map[string]any{"url": "https://gitlab.example.com/acme/ci.yml", "deep": map[string]any{"x": "1"}, "nil": nil},
		"a.b":        "dotted",
		"a":          map[string]any{"b": "nested"},
	}
	cases := []struct {
		name string
		cond string
		want bool
	}{
		{"a path reaches a nested value", `{"path":["job_config","url"],"glob":"https://gitlab.example.com/**"}`, true},
		{"two levels", `{"path":["job_config","deep","x"],"equals":1}`, true},
		{"a leaf on an object is false", `{"claim":"job_config","glob":"*"}`, false},
		{"an object is present", `{"claim":"job_config","present":true}`, true},
		{"a path to a null member is absent", `{"path":["job_config","nil"],"present":false}`, true},
		{"a path through a string is absent", `{"path":["job_config","url","x"],"present":false}`, true},
		{"dots in a claim name are never interpreted", `{"claim":"a.b","equals":"dotted"}`, true},
		{"a path of member names reads the nested value, not the dotted name", `{"path":["a","b"],"equals":"nested"}`, true},
		{"a path of one member is the claim", `{"path":["a.b"],"equals":"dotted"}`, true},
	}
	for _, tc := range cases {
		t.Run("R-CI-010 "+tc.name, func(t *testing.T) {
			if got := matches(t, tc.cond, c); got != tc.want {
				t.Fatalf("matched %v, want %v", got, tc.want)
			}
		})
	}
}

func TestR_CI_010_RegularExpressions(t *testing.T) {
	c := claims.Claims{"ref": "v1.4.0", "name": "ΣΊΣΥΦΟΣ"}
	cases := []struct {
		name string
		cond string
		want bool
	}{
		{"a regex is anchored at both ends", `{"claim":"ref","regex":"1\\.4"}`, false},
		{"a regex matches the whole value", `{"claim":"ref","regex":"v[0-9.]+"}`, true},
		{"an alternation stays inside the anchors", `{"claim":"ref","regex":"x|v1.4.0"}`, true},
		{"an alternation cannot escape the anchors", `{"claim":"ref","regex":"x|1\\.4"}`, false},
		{"a glob literal is never a regex", `{"claim":"ref","glob":"v1.+"}`, false},
		{"an equals literal is never a regex", `{"claim":"ref","equals":"v.*"}`, false},
		{"ignoreCase folds Unicode in a regex", `{"claim":"name","regex":"σίσυφος","ignoreCase":true}`, true},
		{"a regex is case-sensitive by default", `{"claim":"name","regex":"σίσυφος"}`, false},
	}
	for _, tc := range cases {
		t.Run("R-CI-010 "+tc.name, func(t *testing.T) {
			if got := matches(t, tc.cond, c); got != tc.want {
				t.Fatalf("matched %v, want %v", got, tc.want)
			}
		})
	}
}

// TestR_CI_010_ARegexDoesNotBacktrack evaluates {"claim": "ref", "regex": "(a+)+$"} against a value
// of 10,000 characters that does not match, the classic catastrophic input of a backtracking
// engine. RE2 runs it in linear time. As for the other latency targets of the repository
// (Q-ARCH-250), the per-change run asserts the median where the race detector is off; the p99 is
// enforced with TROVE_PERF_GATE=1 and logged otherwise.
func TestR_CI_010_ARegexDoesNotBacktrack(t *testing.T) {
	n := parse(t, `{"claim":"ref","regex":"(a+)+$"}`)
	if err := n.Validate(claims.Limits{}); err != nil {
		t.Fatal(err)
	}
	c := claims.Claims{"ref": strings.Repeat("a", 9999) + "!"}
	if n.Evaluate(c).Matched {
		t.Fatal("matched")
	}
	const runs = 200
	took := make([]time.Duration, 0, runs)
	for range runs {
		start := time.Now()
		n.Evaluate(c)
		took = append(took, time.Since(start))
	}
	slices.Sort(took)
	p50, p99 := took[runs/2], took[runs*99/100]
	t.Logf("(a+)+$ against 10,000 characters: p50 %s, p99 %s", p50, p99)
	if raceDetector {
		return
	}
	if p50 > time.Millisecond {
		t.Errorf("R-CI-010: median %s is above 1 ms", p50)
	}
	switch {
	case p99 <= time.Millisecond:
	case os.Getenv("TROVE_PERF_GATE") == "1":
		t.Errorf("R-CI-010: p99 %s is above 1 ms", p99)
	default:
		t.Logf("p99 %s is above 1 ms; enforced with TROVE_PERF_GATE=1 (Q-ARCH-250)", p99)
	}
}

// deep nests a leaf under n combinators: all → any → not → all → …
func deep(n int) string {
	s := `{"claim":"ref","equals":"main"}`
	for i := range n {
		switch i % 3 {
		case 0:
			s = `{"not":` + s + `}`
		case 1:
			s = `{"any":[` + s + `]}`
		default:
			s = `{"all":[` + s + `]}`
		}
	}
	return s
}

func leaves(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = `{"claim":"c` + strconv.Itoa(i) + `","present":true}`
	}
	return `{"all":[` + strings.Join(parts, ",") + `]}`
}

func inList(n int) string {
	vals := make([]string, n)
	for i := range vals {
		vals[i] = strconv.Itoa(i)
	}
	return `{"claim":"project_id","in":[` + strings.Join(vals, ",") + `]}`
}

func TestR_CI_010_TheLimitsOfACondition(t *testing.T) {
	ok := []struct{ name, cond string }{
		{"depth 4", deep(3)},
		{"64 leaves", leaves(64)},
		{"256 in values", inList(256)},
		{"a regex of 512 characters", `{"claim":"ref","regex":"` + strings.Repeat("a", 512) + `"}`},
		{"a glob of 1024 characters (Q-CI-28)", `{"claim":"ref","glob":"` + strings.Repeat("a", 1024) + `"}`},
	}
	for _, c := range ok {
		t.Run("R-CI-010 "+c.name+" is valid", func(t *testing.T) {
			if err := parse(t, c.cond).Validate(claims.DefaultLimits()); err != nil {
				t.Fatal(err)
			}
		})
	}
	bad := []struct{ name, cond, pointer string }{
		{"depth 5", deep(4), "/not/all/0/any/0/not"},
		{"65 leaves", leaves(65), ""},
		{"257 in values", inList(257), "/in"},
		{"a regex of 513 characters", `{"claim":"ref","regex":"` + strings.Repeat("a", 513) + `"}`, "/regex"},
		{"a regex that does not compile", `{"claim":"ref","regex":"(a"}`, "/regex"},
		{"a regex that would escape the anchors", `{"claim":"ref","regex":"a)|(b"}`, "/regex"},
		{"an invalid glob", `{"all":[{"claim":"ref","glob":"a**"}]}`, "/all/0/glob"},
		{"a glob of 1025 characters (Q-CI-28)", `{"claim":"ref","glob":"` + strings.Repeat("a", 1025) + `"}`, "/glob"},
	}
	for _, c := range bad {
		t.Run("R-CI-010 "+c.name+" is condition_invalid", func(t *testing.T) {
			err := parse(t, c.cond).Validate(claims.DefaultLimits())
			r, ptr := reasonOf(t, err)
			if r != reason.ConditionInvalid || ptr != c.pointer {
				t.Fatalf("%s at %q, want condition_invalid at %q (%v)", r, ptr, c.pointer, err)
			}
		})
	}
}

func TestR_CI_010_TheShapeOfANode(t *testing.T) {
	cases := []struct{ name, cond, pointer string }{
		{"an empty all", `{"all":[]}`, "/all"},
		{"an empty any", `{"all":[{"claim":"a","present":true},{"any":[]}]}`, "/all/1/any"},
		{"an unknown member", `{"claim":"ref","equals":"x","ignore_case":true}`, "/ignore_case"},
		{"a combinator and a leaf", `{"all":[{"claim":"a","present":true}],"claim":"b"}`, ""},
		{"two combinators", `{"all":[{"claim":"a","present":true}],"any":[{"claim":"a","present":true}]}`, ""},
		{"a leaf without operator", `{"claim":"ref"}`, ""},
		{"a leaf with two operators", `{"claim":"ref","equals":"a","glob":"b"}`, ""},
		{"a leaf with claim and path", `{"claim":"ref","path":["ref"],"present":true}`, ""},
		{"an operator without claim", `{"equals":"a"}`, ""},
		{"a null literal", `{"claim":"ref","equals":null}`, "/equals"},
		{"an object literal", `{"claim":"ref","in":["a",{"b":1}]}`, "/in/1"},
		{"a fractional literal (Q-CI-28)", `{"claim":"project_id","equals":20.0}`, "/equals"},
		{"an exponent literal (Q-CI-28)", `{"claim":"project_id","in":[2e1]}`, "/in/0"},
		{"ignoreCase with present", `{"claim":"ref","present":true,"ignoreCase":true}`, "/ignoreCase"},
		{"a node that is not an object", `{"all":["ref"]}`, "/all/0"},
		{"an empty path", `{"path":[],"present":true}`, "/path"},
		{"an empty path member", `{"path":["a",""],"present":true}`, "/path/1"},
		{"a glob that is not a string", `{"claim":"ref","glob":1}`, "/glob"},
		{"data after the condition", `{"claim":"ref","present":true} {}`, ""},
	}
	for _, c := range cases {
		t.Run("R-CI-010 "+c.name+" is condition_invalid", func(t *testing.T) {
			_, err := claims.ParseCondition([]byte(c.cond))
			r, ptr := reasonOf(t, err)
			if r != reason.ConditionInvalid || ptr != c.pointer {
				t.Fatalf("%s at %q, want condition_invalid at %q (%v)", r, ptr, c.pointer, err)
			}
		})
	}
	t.Run("R-CI-010 a condition without a leaf is catch_all_condition", func(t *testing.T) {
		for _, cond := range []string{`{}`, `{"not":{}}`, `{"all":[{}]}`} {
			r, _ := reasonOf(t, parse(t, cond).Validate(claims.Limits{}))
			if r != reason.CatchAllCondition {
				t.Fatalf("%s: %s", cond, r)
			}
		}
	})
	t.Run("R-CI-010 Validate holds a node built in Go to the same rules", func(t *testing.T) {
		yes := true
		glob := "a"
		for _, n := range []claims.Node{
			{Claim: "a", Present: &yes, Glob: &glob},
			{Claim: "a", All: []claims.Node{{Claim: "b", Present: &yes}}},
			{All: []claims.Node{{Claim: "b", Present: &yes}, {}}},
			{Claim: "a", Equals: json.RawMessage(`null`)},
		} {
			if r, _ := reasonOf(t, n.Validate(claims.Limits{})); r != reason.ConditionInvalid {
				t.Fatalf("%+v: %s", n, r)
			}
		}
	})
	t.Run("R-CI-010 a parsed condition marshals to the stored form and parses back the same", func(t *testing.T) {
		cond := `{"all":[{"claim":"namespace_id","in":["4711",4712]},{"path":["a","b"],"glob":"x/**","ignoreCase":true},{"not":{"claim":"e","present":true}},{"claim":"ref_protected","equals":true}]}`
		n := parse(t, cond)
		b, err := json.Marshal(n)
		if err != nil {
			t.Fatal(err)
		}
		var back claims.Node
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		b2, _ := json.Marshal(back)
		if string(b) != string(b2) {
			t.Fatalf("%s != %s", b, b2)
		}
		if err := json.Unmarshal([]byte(`{"claim":"x","equals":"a","in":["b"]}`), &back); err == nil {
			t.Fatal("encoding/json decoded a node with two operators")
		}
	})
}

func TestTheTraceOfAnEvaluation(t *testing.T) {
	cond := parse(t, `{"all": [
		{"claim": "namespace_id", "in": ["4711", "4712"]},
		{"claim": "pipeline_source", "equals": "merge_request_event"},
		{"claim": "ref_protected", "equals": true},
		{"not": {"claim": "environment_action", "in": ["start"]}}
	]}`)
	ev := cond.Evaluate(tokenClaims())
	if ev.Matched || ev.FailedLeaf != "/all/1" || ev.Detail != `pipeline_source equals "merge_request_event": value "push"` {
		t.Fatalf("%+v", ev)
	}
	if len(ev.Leaves) != 4 {
		t.Fatalf("the trace lists %d leaves, want every one of 4", len(ev.Leaves))
	}
	want := []struct {
		ptr    string
		passed bool
	}{{"/all/0", true}, {"/all/1", false}, {"/all/2", true}, {"/all/3/not", true}}
	for i, w := range want {
		if l := ev.Leaves[i]; l.Pointer != w.ptr || l.Passed != w.passed {
			t.Fatalf("leaf %d: %+v", i, l)
		}
	}
	if l := ev.Leaves[2]; l.Operator != "equals" || l.Expected[0] != "true" || l.Actual[0] != "true" || l.Absent {
		t.Fatalf("%+v", l)
	}

	c := tokenClaims()
	c["pipeline_source"] = "merge_request_event"
	c["ref_protected"] = "false"
	ev = cond.Evaluate(c)
	if ev.FailedLeaf != "/all/2" || ev.Detail != `ref_protected equals "true": value "false"` {
		t.Fatalf("R-CI-019 the same claim set with ref_protected false shows the failing leaf: %+v", ev)
	}
	c["ref_protected"] = "true"
	ev = cond.Evaluate(c)
	if ev.FailedLeaf != "/all/3/not" || !strings.HasPrefix(ev.Detail, `not environment_action in ["start"]: value "start"`) {
		t.Fatalf("a failing not: %+v", ev)
	}
	delete(c, "namespace_id")
	ev = cond.Evaluate(c)
	if ev.FailedLeaf != "/all/0" || ev.Detail != `namespace_id in ["4711", "4712"]: absent` || !ev.Leaves[0].Absent {
		t.Fatalf("an absent claim: %+v", ev)
	}
	ev = parse(t, `{"claim":"ref","equals":"main"}`).Evaluate(c)
	if ev.Matched || ev.FailedLeaf != "" || ev.Detail != `ref equals "main": value "v1.4.0"` {
		t.Fatalf("a failing root leaf: %+v", ev)
	}
	ev = parse(t, `{"claim":"ref","glob":"x"}`).Evaluate(claims.Claims{"ref": strings.Repeat("y", 300)})
	if len(ev.Detail) > 200 || !strings.Contains(ev.Detail, "…") {
		t.Fatalf("a long value is cut in the detail: %d %q", len(ev.Detail), ev.Detail)
	}
}

func TestR_CI_010_TopLevelFacts(t *testing.T) {
	pins := []string{"project_id", "namespace_id", "job_project_id", "job_namespace_id"}
	cases := []struct {
		cond                 string
		pinned, protectedRef bool
		testsPipeline        bool
		guarded              string
	}{
		{`{"claim":"project_id","equals":"4242"}`, true, false, false, ""},
		{`{"all":[{"claim":"namespace_id","in":["4711"]},{"claim":"ref_protected","equals":"true"},{"claim":"environment","present":true}]}`, true, true, false, "environment"},
		{`{"all":[{"any":[{"claim":"project_id","equals":"1"}]},{"not":{"claim":"ref_protected","equals":true}}]}`, false, false, false, ""},
		{`{"all":[{"all":[{"claim":"project_id","equals":"1"}]},{"claim":"project_path","glob":"acme/**"}]}`, false, false, false, ""},
		{`{"all":[{"claim":"project_id","glob":"4242"},{"claim":"ref_protected","equals":false}]}`, false, false, false, ""},
		{`{"all":[{"path":["project_id"],"equals":"1"},{"not":{"claim":"pipeline_source","equals":"trigger"}}]}`, true, false, true, ""},
		{`{"any":[{"claim":"project_id","equals":"1"}]}`, false, false, false, ""},
	}
	for _, c := range cases {
		t.Run("R-CI-010 top-level facts of "+c.cond, func(t *testing.T) {
			n := parse(t, c.cond)
			if n.Pins(pins) != c.pinned || n.RequiresProtectedRef() != c.protectedRef || n.TestsPipelineSource() != c.testsPipeline || strings.Join(n.GuardedClaims(), ",") != c.guarded {
				t.Fatalf("pins %v protected %v pipeline %v guarded %v", n.Pins(pins), n.RequiresProtectedRef(), n.TestsPipelineSource(), n.GuardedClaims())
			}
		})
	}
}
