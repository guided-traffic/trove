//go:build integration

package endpoints

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
)

// contract runs the convention tests of E1.6 on the exposure rules of customers:
// pagination, ETag with If-Match, merge patch, PUT round trip and idempotency
// keys, the requests spread over the replicas and every answer validated against
// the document.
func contract(t *testing.T, e *env) {
	t.Helper()
	apitest.RunContract(t, apitest.Resource{
		Name: "exposure-rules",
		Do: func(req *http.Request) (*http.Response, error) {
			r := e.pick()
			var body []byte
			if req.Body != nil {
				body, _ = io.ReadAll(req.Body)
			}
			u, err := url.Parse(req.URL.String())
			if err != nil {
				return nil, err
			}
			u.Scheme, u.Host = "http", r.def
			out, err := http.NewRequestWithContext(req.Context(), req.Method, u.String(), bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			out.Header = req.Header.Clone()
			out.Host = "trove.test"
			out.Header.Set("Authorization", "Bearer "+e.opsPAT)
			res, err := client.Do(out)
			if err != nil {
				return nil, err
			}
			b, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			check, _ := http.NewRequest(req.Method, "http://trove.test"+u.RequestURI(), nil)
			if _, verr := e.val.Check(check, res.StatusCode, res.Header, b); verr != nil {
				t.Errorf("response validation: %v\n%s", verr, b)
			}
			res.Body = io.NopCloser(bytes.NewReader(b))
			return res, nil
		},
		Collection: "http://trove.test/api/v1/endpoints/customers/exposure-rules",
		Create: func(n int) map[string]any {
			return map[string]any{
				"name": fmt.Sprintf("contract-%d", n), "description": "contract rule", "repository": "generic-local",
				"paths": map[string]any{"include": []string{fmt.Sprintf("contract/%d/**", n)}},
			}
		},
		Key:          func(m map[string]any) string { s, _ := m["name"].(string); return s },
		Patch:        map[string]any{"enabled": false},
		PatchMember:  "enabled",
		PatchValue:   false,
		ClearMember:  "description",
		ClearedValue: "",
		// The repository of a rule is immutable (Q-EP-8); a different value is 422.
		ImmutableMember: "repository",
		ImmutableValue:  "generic-other",
		OtherQuery:      "sort=-name",
		Put:             true,
	})
}
