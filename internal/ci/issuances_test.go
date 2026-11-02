package ci

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/errs"
)

// Q-CI-26: a project administrator's record shows the contributions of its project's tier only.
func TestQ_CI_26_AProjectsRecordShowsItsOwnMappingsOnly(t *testing.T) {
	var f claims.Frozen
	f.Version, f.Issuer, f.IssuerID, f.ProjectID = claims.FrozenVersion, "gitlab-main", "i", "4711"
	f.Add(claims.FrozenMapping{Ref: "ci:gitlab-main/acme-read", ID: "m1", Tier: "system"})
	f.Add(claims.FrozenMapping{Ref: "ci:gitlab-main/web-publish", ID: "m2", Tier: "project:web", Project: &claims.FrozenProject{Key: "web", ID: "p"}})
	is := onlyProject(Issuance{MappingIDs: f.MappingIDs, MappingNames: []string{"acme-read", "web-publish"}, Grants: f}, ProjectTier("web"))
	if !slices.Equal(is.MappingNames, []string{"web-publish"}) || !slices.Equal(is.MappingIDs, []string{"m2"}) ||
		len(is.Grants.Contributions) != 1 || is.Grants.Mappings[0] != "ci:gitlab-main/web-publish" || is.Grants.ProjectID != "4711" {
		t.Fatalf("narrowed %+v", is)
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
