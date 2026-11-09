package v1

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// maintenanceAllowed is the list of R-OPS-024 "still allowed" as the document declares it: credentials and sessions, the CI token
// exchange, the invalidation of a remote cache, the switch itself, and the operations that only evaluate. A new write is refused in
// maintenance mode until it is added here and in the document on purpose.
var maintenanceAllowed = []string{
	"bastionCheck", "bastionDownloads", "checkExposure", "checkPermissions", "createBastionToken", "createProjectServiceAccountToken",
	"createServiceAccountToken", "createToken", "endOwnSession", "endOwnSessions", "endUserSessions", "evaluateCiMappings",
	"evaluateProjectCiMappings", "exchangeCiToken", "invalidateRepositoryCache", "putSystemMaintenance", "revokeBastionToken",
	"revokeCiIssuances", "revokeCurrentToken", "revokeOwnTokens", "revokeProjectServiceAccountTokens", "revokeServiceAccountTokens",
	"revokeToken", "revokeTokenByValue", "revokeUserTokens", "rotateCurrentToken", "rotateToken", "testRepositoryUpstream", "testUpstream",
	"validatePermissionPolicy",
}

func TestR_OPS_024_OperationsAllowedInMaintenanceMode(t *testing.T) {
	doc, err := LoadDocument(EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	writes := 0
	for pattern, op := range doc.ops {
		method, _, _ := strings.Cut(pattern, " ")
		if refusedInMaintenance(method) {
			writes++
		}
		if op.MaintenanceAllowed {
			got = append(got, op.ID)
			if !refusedInMaintenance(method) {
				t.Errorf("%s declares x-trove-maintenance on a method that is never refused", pattern)
			}
		}
	}
	slices.Sort(got)
	want := slices.Clone(maintenanceAllowed)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("operations allowed in maintenance mode:\n got %v\nwant %v", got, want)
	}
	if writes < 100 || len(got) >= writes/2 {
		t.Errorf("%d of %d writes are allowed: the default must be the refusal", len(got), writes)
	}
}

func TestR_OPS_024_RefusedMethods(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if refusedInMaintenance(m) {
			t.Errorf("%s is refused", m)
		}
	}
	for _, m := range []string{http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		if !refusedInMaintenance(m) {
			t.Errorf("%s is allowed", m)
		}
	}
}

func TestR_OPS_024_MaintenanceDeclarationIsValidated(t *testing.T) {
	for name, tc := range map[string]struct {
		op     map[string]any
		method string
		ok     bool
	}{
		"absent":      {map[string]any{}, "post", true},
		"allowed":     {map[string]any{"x-trove-maintenance": "allowed"}, "post", true},
		"other word":  {map[string]any{"x-trove-maintenance": "refused"}, "post", false},
		"boolean":     {map[string]any{"x-trove-maintenance": true}, "post", false},
		"on a read":   {map[string]any{"x-trove-maintenance": "allowed"}, "get", false},
		"on a delete": {map[string]any{"x-trove-maintenance": "allowed"}, "delete", true},
	} {
		if _, err := parseMaintenanceAllowed(tc.op, tc.method); (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}
