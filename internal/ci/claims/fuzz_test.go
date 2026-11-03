package claims_test

import (
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
)

// FuzzR_CI_010_Condition parses arbitrary bytes as a condition (R-CI-010): no panic; a refusal is
// condition_invalid (or, from Validate, catch_all_condition); a condition that parses and validates
// evaluates on the example claims with a trace whose every leaf has a pointer, marshals to JSON
// that parses back to the same JSON, and stays within the limits after the round trip; the compiled
// condition of the exchange decides as the traced evaluation (Q-CI-49).
//
//	go test ./internal/ci/claims -run '^$' -fuzz '^FuzzR_CI_010_Condition$' -fuzztime 1m
func FuzzR_CI_010_Condition(f *testing.F) {
	for _, s := range []string{
		publishMatch, deep(3), leaves(3), inList(3), `{}`, `{"not":{"claim":"a","present":false}}`,
		`{"any":[{"path":["a","b"],"regex":"(a+)+$","ignoreCase":true},{"claim":"ref","glob":"v*/**"}]}`,
		`{"claim":"x","equals":-0}`, `{"claim":"x","in":[1,"1",true]}`,
	} {
		f.Add([]byte(s))
	}
	c := tokenClaims()
	c["a"] = map[string]any{"b": []any{"x", json.Number("2"), nil}}
	f.Fuzz(func(t *testing.T, b []byte) {
		n, err := claims.ParseCondition(b)
		if err != nil {
			if r, _ := reasonOf(t, err); r != reason.ConditionInvalid {
				t.Fatalf("parse reason %s", r)
			}
			return
		}
		if err := n.Validate(claims.DefaultLimits()); err != nil {
			if r, _ := reasonOf(t, err); r != reason.ConditionInvalid && r != reason.CatchAllCondition {
				t.Fatalf("validate reason %s", r)
			}
			return
		}
		ev := n.Evaluate(c)
		if claims.CompileCondition(n).Matches(c) != ev.Matched {
			t.Fatal("the compiled condition of the exchange decides otherwise than the traced evaluation (Q-CI-49)")
		}
		if len(ev.Leaves) == 0 {
			t.Fatal("a valid condition evaluated without a leaf in its trace")
		}
		if !ev.Matched && ev.Detail == "" {
			t.Fatal("a failure without a detail")
		}
		out, err := json.Marshal(n)
		if err != nil {
			t.Fatal(err)
		}
		back, err := claims.ParseCondition(out)
		if err != nil {
			t.Fatalf("the stored form %s does not parse: %v", out, err)
		}
		if err := back.Validate(claims.DefaultLimits()); err != nil {
			t.Fatalf("the stored form %s does not validate: %v", out, err)
		}
		again, _ := json.Marshal(back)
		if string(again) != string(out) {
			t.Fatalf("%s became %s", out, again)
		}
		if back.Evaluate(c).Matched != ev.Matched {
			t.Fatal("the stored form evaluates differently")
		}
	})
}
