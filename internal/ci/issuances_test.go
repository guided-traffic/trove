package ci

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/errs"
)

// Q-CI-26: a project Owner's record shows the contributions of its project's tier only.
func TestQ_CI_26_AProjectsRecordShowsItsOwnMappingsOnly(t *testing.T) {
	var f claims.Frozen
	f.Version, f.Issuer, f.IssuerID, f.ProjectID = claims.FrozenVersion, "gitlab-main", "i", "4711"
	f.Add(claims.FrozenMapping{Ref: "ci:gitlab-main/acme-read", ID: "m1", Tier: "system"})
	f.Add(claims.FrozenMapping{Ref: "ci:gitlab-main/web-publish", ID: "m2", Tier: "project:web", Project: &claims.FrozenProject{Key: "web", ID: "p"}})
	is := onlyProject(Issuance{
		MappingIDs: f.MappingIDs, MappingNames: []string{"acme-read", "web-publish"}, Grants: f,
		RequestedScope: "read:npm-internal deploy:npm-web", GrantedScope: "read:npm-internal deploy:npm-web read:npm-acme",
		JTI: "jti-1", UserID: "7", UserLogin: "alice", ClientIP: "192.0.2.7", UserAgent: "curl/8",
		Claims: map[string]any{"project_id": "4711", "job_id": "77", "ref": "main", "user_id": "7", "user_login": "alice", "jti": "jti-1", "runner_id": 3.0},
	}, ProjectTier("web"))
	if !slices.Equal(is.MappingNames, []string{"web-publish"}) || !slices.Equal(is.MappingIDs, []string{"m2"}) ||
		len(is.Grants.Contributions) != 1 || is.Grants.Mappings[0] != "ci:gitlab-main/web-publish" || is.Grants.ProjectID != "4711" {
		t.Fatalf("narrowed %+v", is)
	}
	// Q-CI-52: the scope strings list every tier's items, so the project's view has none; the person, the token ID of
	// the exchange and the client are left out of the record and of its claim snapshot, the job's identity stays.
	if is.GrantedScope != "" || is.RequestedScope != "" {
		t.Errorf("the scope strings %q and %q survive", is.GrantedScope, is.RequestedScope)
	}
	if is.JTI != "" || is.UserID != "" || is.UserLogin != "" || is.ClientIP != "" || is.UserAgent != "" {
		t.Errorf("personal or client fields survive: %+v", is)
	}
	for _, name := range []string{"user_id", "user_login", "jti", "runner_id"} {
		if _, ok := is.Claims[name]; ok {
			t.Errorf("the claim snapshot keeps %s", name)
		}
	}
	if is.Claims["project_id"] != "4711" || is.Claims["job_id"] != "77" || is.Claims["ref"] != "main" {
		t.Errorf("the job's identity is gone: %v", is.Claims)
	}
}

// R-CI-020, Q-CI-52: a record is active only when its token row exists, is unrevoked and the record is unexpired; a
// token row that is gone while the record is unexpired reads as removed.
func TestR_CI_020_TheStateOfARecordIsOneValue(t *testing.T) {
	for state, active := range map[string]bool{StateActive: true, StateRevoked: false, StateExpired: false, StateRemoved: false} {
		if got := (Issuance{State: state}).Active(); got != active {
			t.Errorf("state %q: Active() = %v", state, got)
		}
	}
}

// R-API-010: the cursor of the list is the sort key of the last record, and anything else is invalid_cursor.
func TestTheIssuanceCursorIsTheSortKey(t *testing.T) {
	is := Issuance{TokenID: "0190a4c2-0000-7000-8000-000000000001", IssuedAt: time.UnixMicro(1767225600123456)}
	got, err := decodeAfter(strings.Join(IssuanceSortKey(is), ","))
	if err != nil || got.us != 1767225600123456 || got.id != is.TokenID {
		t.Fatalf("decoded %+v %v", got, err)
	}
	for _, bad := range []string{"x", "1,2", "0," + is.TokenID, "-5," + is.TokenID, "1767225600123456"} {
		if _, err := decodeAfter(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		} else if e, _ := errs.As(err); e == nil || e.Code != errs.CodeInvalidCursor {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// R-CI-020: the bulk revoke needs the issuer and at least one of projectId, pipelineId, jobId, mapping.
func TestR_CI_020_TheBulkRevokeNeedsAnIssuerAndAFilter(t *testing.T) {
	for name, f := range map[string]RevokeFilter{
		"nothing":        {},
		"no issuer":      {ProjectID: "1"},
		"only an issuer": {Issuer: "gitlab-main"},
	} {
		err := checkRevokeFilter(f)
		if e, _ := errs.As(err); e == nil || e.Kind != errs.Unprocessable {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, f := range []RevokeFilter{{Issuer: "g", ProjectID: "1"}, {Issuer: "g", PipelineID: "1"}, {Issuer: "g", JobID: "1"}, {Issuer: "g", Mapping: "m"}} {
		if err := checkRevokeFilter(f); err != nil {
			t.Errorf("%+v: %v", f, err)
		}
	}
	if got := (RevokeFilter{Issuer: "g", JobID: "9"}).record(); len(got) != 2 || got["jobId"] != "9" {
		t.Errorf("the event's filter %v", got)
	}
}
