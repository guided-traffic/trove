package v1

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
)

// recordingCaller counts the capability checks that refuse: each is the audit event
// authz.denied of the capability (R-AUTHZ-029).
type recordingCaller struct {
	projectCaller
	refusals *[]string
}

func (c recordingCaller) Require(ctx context.Context, permission string) error {
	err := c.projectCaller.Require(ctx, permission)
	if err != nil {
		*c.refusals = append(*c.refusals, permission)
	}
	return err
}

// A refusal of a project administrator's narrowing is audited like the refusal of
// the capability, because the shared step admitted the caller and no layer above
// the handler saw the refusal (R-AUTHZ-029, Q-AUTHZ-201).
func TestR_AUTHZ_029_AProjectAdministratorsRefusalIsAudited(t *testing.T) {
	web := projectCaller{
		projects:     []authz.AdministeredProject{{Key: "web", ManageRepositories: true}},
		repositories: map[string]string{"web-npm": "web", "shop-npm": "shop", "npm-internal": ""},
	}
	var got []string
	s := projectServer(web)
	s.o.Access = recordingCaller{projectCaller: web, refusals: &got}
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		run  func() error
		want int // refusals recorded
	}{
		{"a project the caller does not administer", func() error {
			return s.requireProject(ctx, "repositories:write-config", "shop", authz.ProjectManageRepositories)
		}, 1},
		{"a switch that is off", func() error {
			return s.requireProject(ctx, "policies:write", "web", authz.ProjectManagePolicies)
		}, 1},
		{"a repository of another project", func() error {
			_, err := s.requireProjectOfRepository(ctx, "repositories:write-config", "shop-npm", authz.ProjectManageRepositories)
			return err
		}, 1},
		{"a repository outside every project", func() error {
			_, err := s.requireProjectOfRepository(ctx, "repositories:write-config", "npm-internal", authz.ProjectManageRepositories)
			return err
		}, 1},
		{"a policy of another project", func() error {
			return s.narrowPolicyRead(ctx, authz.Policy{PolicyInput: authz.PolicyInput{Name: "shop-a", Project: strp("shop")}})
		}, 1},
		{"an administered project passes and records nothing", func() error {
			_, err := s.requireProjectOfRepository(ctx, "repositories:write-config", "web-npm", authz.ProjectManageRepositories)
			return err
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got = got[:0]
			err := tc.run()
			if (err != nil) != (tc.want > 0) {
				t.Fatalf("error %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("%d refusals recorded, want %d", len(got), tc.want)
			}
		})
	}
	t.Run("the holder of the capability is never refused by the narrowing", func(t *testing.T) {
		got = got[:0]
		holder := web
		holder.caps = map[string]bool{"policies:write": true}
		s.o.Access = recordingCaller{projectCaller: holder, refusals: &got}
		if err := s.requireProject(ctx, "policies:write", "shop", authz.ProjectManagePolicies); err != nil || len(got) != 0 {
			t.Fatalf("%v %v", err, got)
		}
	})
}

func strp(s string) *string { return &s }

// The filter of a project administrator tests nothing outside its project
// (R-AUTHZ-021, Q-AUTHZ-197).
func TestR_AUTHZ_026_ThePolicyListOfAProjectAdministratorDoesNotExpandGroups(t *testing.T) {
	web := projectCaller{projects: []authz.AdministeredProject{{Key: "web", ManagePolicies: true}}}
	s := projectServer(web)
	ctx := context.Background()
	t.Run("the default does not expand and marks the filter delegated", func(t *testing.T) {
		f := authz.ListFilter{Project: "web", Principal: "user:alice", ExpandGroups: true}
		if err := s.narrowPolicyList(ctx, &f, ""); err != nil || f.ExpandGroups || !f.Delegated {
			t.Fatalf("%v %+v", err, f)
		}
	})
	t.Run("expandGroups=true is 403", func(t *testing.T) {
		f := authz.ListFilter{Project: "web", Principal: "user:alice", ExpandGroups: true}
		if kind, code := codeOf(t, s.narrowPolicyList(ctx, &f, "true")); kind != errs.Forbidden || code != errs.CodeForbidden {
			t.Fatalf("%v %v", kind, code)
		}
	})
	t.Run("the holder of the capability keeps the expansion", func(t *testing.T) {
		holder := web
		holder.caps = map[string]bool{"policies:read": true}
		f := authz.ListFilter{Project: "web", ExpandGroups: true}
		if err := projectServer(holder).narrowPolicyList(ctx, &f, "true"); err != nil || !f.ExpandGroups || f.Delegated {
			t.Fatalf("%v %+v", err, f)
		}
	})
}
