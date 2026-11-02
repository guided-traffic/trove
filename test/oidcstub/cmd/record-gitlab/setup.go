package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type user struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	PublicEmail string `json:"public_email"`
	CommitEmail string `json:"commit_email"`
}

type group struct {
	ID       int64  `json:"id"`
	FullPath string `json:"full_path"`
}

type project struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	ImportStatus      string `json:"import_status"`
}

// setup is what the cases run in.
type setup struct {
	top, billing, partner group
	parent, fork          project
	scheduleID            int64
	// partnerUser opens the merge request from the fork. It is a Reporter of the parent, so
	// GitLab cannot run its merge request pipeline in the parent and runs it in the fork.
	partnerUser user
	// licensePlan is the plan of the instance's licence ("" without one): protected environments
	// need a paid plan, and environment_protected is "false" without one.
	licensePlan string
}

const (
	projectName         = "invoice-api"
	partnerUsername     = "trove-oidcv-partner"
	scheduleDescription = "trove-oidc-validation: pipeline_source schedule (played by record-gitlab, never active)"
	groupDescription    = "trove: records how GitLab issues CI ID tokens, for the fixtures of the issuer double (test/oidcstub/testdata, make oidc-fixtures). Safe to delete; the next run creates it again."
)

// Access levels of the members API.
const (
	reporterAccess   = 20
	maintainerAccess = 40
)

func pathOf(segments ...string) string {
	p := ""
	for i, s := range segments {
		if i > 0 {
			p += "/"
		}
		p += s
	}
	return url.PathEscape(p)
}

func projectPath(id int64) string { return "/projects/" + strconv.FormatInt(id, 10) }

func ensureGroup(ctx context.Context, c *client, parent *group, name string) (group, error) {
	full := name
	if parent != nil {
		full = parent.FullPath + "/" + name
	}
	var g group
	err := c.call(ctx, http.MethodGet, "/groups/"+pathOf(full), nil, &g)
	if err == nil || !isNotFound(err) {
		return g, err
	}
	in := map[string]any{"name": name, "path": name, "visibility": "private", "description": groupDescription}
	if parent != nil {
		in["parent_id"] = parent.ID
	}
	logf("creating group %s", full)
	err = c.call(ctx, http.MethodPost, "/groups", in, &g)
	return g, err
}

func ensureProject(ctx context.Context, c *client, ns group) (project, error) {
	var p project
	err := c.call(ctx, http.MethodGet, "/projects/"+pathOf(ns.FullPath, projectName), nil, &p)
	if err == nil || !isNotFound(err) {
		return p, err
	}
	logf("creating project %s/%s", ns.FullPath, projectName)
	err = c.call(ctx, http.MethodPost, "/projects", map[string]any{
		"name": projectName, "path": projectName, "namespace_id": ns.ID, "visibility": "private",
		"initialize_with_readme": true, "default_branch": "main", "description": groupDescription,
	}, &p)
	return p, err
}

func ensureFork(ctx context.Context, c *client, parent project, ns group) (project, error) {
	var p project
	err := c.call(ctx, http.MethodGet, "/projects/"+pathOf(ns.FullPath, projectName), nil, &p)
	if err != nil && !isNotFound(err) {
		return p, err
	}
	if err != nil {
		logf("forking %s into %s", parent.PathWithNamespace, ns.FullPath)
		if err := c.call(ctx, http.MethodPost, projectPath(parent.ID)+"/fork", map[string]any{
			"namespace_id": ns.ID, "path": projectName, "name": projectName,
		}, &p); err != nil {
			return p, err
		}
	}
	for p.ImportStatus != "" && p.ImportStatus != "finished" && p.ImportStatus != "none" {
		if p.ImportStatus == "failed" {
			return p, fmt.Errorf("the fork %s failed to import", p.PathWithNamespace)
		}
		if err := pause(ctx, 2*time.Second); err != nil {
			return p, err
		}
		if err := c.call(ctx, http.MethodGet, projectPath(p.ID), nil, &p); err != nil {
			return p, err
		}
	}
	return p, nil
}

// ensure creates the object at createPath unless getPath finds it.
func ensure(ctx context.Context, c *client, getPath, createPath string, in map[string]any) error {
	err := c.call(ctx, http.MethodGet, getPath, nil, nil)
	if err == nil || !isNotFound(err) {
		return err
	}
	logf("creating %s", createPath)
	return c.call(ctx, http.MethodPost, createPath, in, nil)
}

func ensureSetup(ctx context.Context, c *client, top string) (setup, error) {
	var s setup
	var err error
	if s.top, err = ensureGroup(ctx, c, nil, top); err != nil {
		return s, err
	}
	if s.billing, err = ensureGroup(ctx, c, &s.top, "billing"); err != nil {
		return s, err
	}
	if s.partner, err = ensureGroup(ctx, c, &s.top, "partner"); err != nil {
		return s, err
	}
	if s.parent, err = ensureProject(ctx, c, s.billing); err != nil {
		return s, err
	}
	pp := projectPath(s.parent.ID)
	if err := ensure(ctx, c, pp+"/protected_branches/main", pp+"/protected_branches",
		map[string]any{"name": "main", "push_access_level": 40, "merge_access_level": 40}); err != nil {
		return s, err
	}
	if err := ensure(ctx, c, pp+"/protected_tags/"+url.PathEscape("v*"), pp+"/protected_tags",
		map[string]any{"name": "v*", "create_access_level": 40}); err != nil {
		return s, err
	}
	for _, e := range []struct{ name, tier string }{{"production", "production"}, {"staging", "staging"}} {
		var found []struct{ ID int64 }
		if err := c.call(ctx, http.MethodGet, pp+"/environments?name="+url.QueryEscape(e.name), nil, &found); err != nil {
			return s, err
		}
		if len(found) == 0 {
			logf("creating environment %s", e.name)
			if err := c.call(ctx, http.MethodPost, pp+"/environments", map[string]any{"name": e.name, "tier": e.tier}, nil); err != nil {
				return s, err
			}
		}
	}
	var lic struct {
		Plan string `json:"plan"`
	}
	if err := c.call(ctx, http.MethodGet, "/license", nil, &lic); err != nil && !isNotFound(err) && !isStatus(err, http.StatusForbidden) {
		return s, err
	}
	s.licensePlan = lic.Plan
	err = ensure(ctx, c, pp+"/protected_environments/production", pp+"/protected_environments",
		map[string]any{"name": "production", "deploy_access_levels": []map[string]any{{"access_level": maintainerAccess}}})
	if err != nil && !isStatus(err, http.StatusForbidden) && !isNotFound(err) {
		return s, err
	}
	if err != nil || s.licensePlan == "" {
		logf("protected environments need a licence (plan %q); environment_protected is \"false\" on this instance", s.licensePlan)
	}
	if s.scheduleID, err = ensureSchedule(ctx, c, s.parent); err != nil {
		return s, err
	}
	if s.fork, err = ensureFork(ctx, c, s.parent, s.partner); err != nil {
		return s, err
	}
	if s.partnerUser, err = ensurePartner(ctx, c, s); err != nil {
		return s, fmt.Errorf("the partner user of the fork cases (the token's user must be an administrator; with Admin Mode on, the token needs the scope admin_mode): %w", err)
	}
	return s, nil
}

// ensurePartner creates the user that opens the merge request from the fork, a Reporter of the
// parent project and a Maintainer of the fork's group. It gets a random password nobody knows.
func ensurePartner(ctx context.Context, c *client, s setup) (user, error) {
	var found []user
	if err := c.call(ctx, http.MethodGet, "/users?username="+url.QueryEscape(partnerUsername), nil, &found); err != nil {
		return user{}, err
	}
	var u user
	if len(found) > 0 {
		u = found[0]
	} else {
		logf("creating user %s", partnerUsername)
		if err := c.call(ctx, http.MethodPost, "/users", map[string]any{
			"username": partnerUsername, "name": "trove-oidcv partner", "email": partnerUsername + "@example.com",
			"force_random_password": true, "skip_confirmation": true,
		}, &u); err != nil {
			return u, err
		}
	}
	for _, m := range []struct {
		path  string
		level int
	}{{projectPath(s.parent.ID) + "/members", reporterAccess}, {"/groups/" + strconv.FormatInt(s.partner.ID, 10) + "/members", maintainerAccess}} {
		err := c.call(ctx, http.MethodGet, m.path+"/"+strconv.FormatInt(u.ID, 10), nil, nil)
		if err == nil {
			continue
		}
		if !isNotFound(err) {
			return u, err
		}
		if err := c.call(ctx, http.MethodPost, m.path, map[string]any{"user_id": u.ID, "access_level": m.level}, nil); err != nil {
			return u, err
		}
	}
	return u, nil
}

// impersonate returns a client that acts as u through an impersonation token, and the function
// that revokes the token. The token expires the next day at the latest.
func impersonate(ctx context.Context, c *client, u user) (*client, func(), error) {
	var tok struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	base := "/users/" + strconv.FormatInt(u.ID, 10) + "/impersonation_tokens"
	if err := c.call(ctx, http.MethodPost, base, map[string]any{
		"name": "trove-oidc-validation", "scopes": []string{"api"}, "expires_at": time.Now().AddDate(0, 0, 1).Format(time.DateOnly),
	}, &tok); err != nil {
		return nil, nil, err
	}
	revoke := func() {
		if err := c.call(context.WithoutCancel(ctx), http.MethodDelete, base+"/"+strconv.FormatInt(tok.ID, 10), nil, nil); err != nil {
			logf("WARNING: revoking the impersonation token %d of %s failed: %v", tok.ID, u.Username, err)
		}
	}
	return &client{base: c.base, token: tok.Token, hc: c.hc}, revoke, nil
}

func ensureSchedule(ctx context.Context, c *client, p project) (int64, error) {
	var list []struct {
		ID          int64  `json:"id"`
		Description string `json:"description"`
	}
	if err := c.call(ctx, http.MethodGet, projectPath(p.ID)+"/pipeline_schedules?per_page=100", nil, &list); err != nil {
		return 0, err
	}
	for _, sc := range list {
		if sc.Description == scheduleDescription {
			return sc.ID, nil
		}
	}
	var created struct {
		ID int64 `json:"id"`
	}
	logf("creating the pipeline schedule (inactive)")
	err := c.call(ctx, http.MethodPost, projectPath(p.ID)+"/pipeline_schedules", map[string]any{
		"description": scheduleDescription, "ref": "main", "cron": "0 4 1 1 *", "cron_timezone": "UTC", "active": false,
	}, &created)
	return created.ID, err
}

func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
