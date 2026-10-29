package v1

import (
	"encoding/json"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// ifMatchExempt are the PUT and PATCH operations that take no If-Match, with the
// reason R-API-012 gives: the edit is a delta, binds one pair, or is file content.
var ifMatchExempt = map[string]string{
	"patchUserGroups":   "membership delta (R-API-012)",
	"patchGroupMembers": "membership delta (R-API-012)",
	"bindRole":          "binds one pair (R-API-012)",
	"deployFile":        "file content on /files (R-API-012)",
}

// TestR_API_012_OneMechanismForAMissingIfMatch holds the declaration side of
// Q-API-43: every PUT and PATCH either declares x-trove-if-match: required with
// the shared IfMatch parameter, or is on the exempt list; no operation spells the
// header inline on a PUT or PATCH.
func TestR_API_012_OneMechanismForAMissingIfMatch(t *testing.T) {
	type operation struct {
		OperationID string           `yaml:"operationId"`
		IfMatch     string           `yaml:"x-trove-if-match"`
		Parameters  []map[string]any `yaml:"parameters"`
		Responses   map[string]any   `yaml:"responses"`
	}
	var tree struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(EmbeddedYAML(), &tree); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for path, item := range tree.Paths {
		for method, node := range item {
			if method != "put" && method != "patch" {
				continue
			}
			var op operation
			if err := node.Decode(&op); err != nil {
				t.Fatal(err)
			}
			seen[op.OperationID] = true
			ref, inline := false, false
			for _, p := range op.Parameters {
				if p["$ref"] == "#/components/parameters/IfMatch" {
					ref = true
				}
				if p["name"] == "If-Match" {
					inline = true
				}
			}
			name := strings.ToUpper(method) + " " + path + " (" + op.OperationID + ")"
			switch {
			case op.IfMatch == "required" && (!ref || inline):
				t.Errorf("R-API-012 %s requires If-Match but does not reference the shared parameter alone", name)
			case op.IfMatch != "required":
				if _, ok := ifMatchExempt[op.OperationID]; !ok {
					t.Errorf("R-API-012 %s neither requires If-Match nor is exempt", name)
				}
				if ref || inline {
					t.Errorf("R-API-012 %s is exempt but declares an If-Match parameter", name)
				}
			case op.Responses["428"] == nil:
				t.Errorf("R-API-012 %s does not document the 428 answer", name)
			}
		}
	}
	var stale []string
	for id := range ifMatchExempt {
		if !seen[id] {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("exempt list names operations that are no PUT or PATCH: %v", stale)
	}
}

// TestR_API_012_MissingIfMatchIs428 sends every operation that requires the
// header without it, through the generated wrapper, and expects the one answer.
func TestR_API_012_MissingIfMatchIs428(t *testing.T) {
	s := newSetup(t, nil, tokenCaller)
	n := 0
	for pattern, op := range s.doc.Operations() {
		if !op.IfMatchRequired {
			continue
		}
		n++
		t.Run("R-API-012 "+pattern+" without If-Match answers 428", func(t *testing.T) {
			method, path, _ := strings.Cut(pattern, " ")
			path = strings.NewReplacer("{endpoint}", "default", "{principalRef}", "user:alice").Replace(path)
			for strings.Contains(path, "{") {
				i, j := strings.Index(path, "{"), strings.Index(path, "}")
				path = path[:i] + "x" + path[j+1:]
			}
			req := httptest.NewRequest(method, path, strings.NewReader("{}"))
			w := httptest.NewRecorder()
			s.handler.ServeHTTP(w, req)
			var p struct {
				Code   string `json:"code"`
				Detail string `json:"detail"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &p)
			if w.Code != 428 || p.Code != "precondition_required" || !strings.Contains(p.Detail, "If-Match") {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	if n < 8 {
		t.Fatalf("only %d operations require If-Match; the document lost declarations", n)
	}
}
