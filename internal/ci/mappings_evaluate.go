package ci

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
	"github.com/guided-traffic/trove/internal/ci/subject"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The dry run (R-CI-019, R-UI-036): a claim set in, the decision trace out. It evaluates the same Select
// the exchange does, on the mapping set read from PostgreSQL (not the copy, so an administrator who just
// saved a mapping sees it), and issues nothing, records no jti and writes no audit event.

// WarnScopeNotChecked is the warning of a dry run whose `scope` asks for an action only a listed policy or
// group may give: the dry run reads no policy, so it takes the item as held and says so.
const WarnScopeNotChecked reason.Reason = "scope_policy_grants_not_checked"

// Evaluate is the dry run over every tier of the issuer.
func (s *MappingService) Evaluate(ctx context.Context, issuer string, req EvaluateRequest) (EvaluateResult, error) {
	return s.evaluate(ctx, issuer, "", req)
}

// EvaluateIn is the dry run over the project's tier only, so the trace shows nothing of another tier
// (Q-CI-26).
func (s *MappingService) EvaluateIn(ctx context.Context, project, issuer string, req EvaluateRequest) (EvaluateResult, error) {
	found := false
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		ids, err := q.New(db).CiProjectID(ctx, project)
		found = len(ids) > 0
		return err
	})
	if err != nil {
		return EvaluateResult{}, err
	}
	if !found {
		return EvaluateResult{}, projectNotFound(project)
	}
	return s.evaluate(ctx, issuer, project, req)
}

func (s *MappingService) evaluate(ctx context.Context, issuerName, project string, req EvaluateRequest) (EvaluateResult, error) {
	var is Issuer
	err := s.o.DB.Read(ctx, func(db q.DBTX) error {
		rows, err := q.New(db).CiIssuerByName(ctx, issuerName)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return notFound(issuerName)
		}
		is = toIssuer(q.CiIssuerByIDRow(rows[0]))
		return nil
	})
	if err != nil {
		return EvaluateResult{}, err
	}
	kind := req.SubjectKind
	if kind == "" {
		kind = SubjectIDToken
	}
	if kind != SubjectIDToken {
		return EvaluateResult{}, mappingViolation([]errs.FieldError{field("/subjectKind", errs.CodeJobTokenFallbackNone, "The job-token fallback is P2: the dry run accepts id_token only.")})
	}
	res := EvaluateResult{Trace: []TraceEntry{}, Selected: []string{}, Warnings: []claims.Warning{}}
	if failure, detail := checkDryRunTimes(req, is); failure != "" {
		res.Failure, res.Detail = failure, detail
		return res, nil
	}
	set, err := s.loadSet(ctx, is.ID)
	if err != nil {
		return EvaluateResult{}, err
	}
	if project != "" {
		set = set.OnlyTier(ProjectTier(project))
	}
	sel := Select(set, kind, req.Claims)
	res.CrossProject, res.Trace = sel.CrossProject, append(res.Trace, sel.Trace...)
	if sel.Failure != "" {
		res.Failure, res.Detail = sel.Failure, sel.Detail
		return res, nil
	}
	bundle := s.bundleOf(is, sel, req.Claims)
	items, err := claims.ParseScope(req.Scope)
	if err != nil {
		return failed(res, err), nil
	}
	policyItems := []string(nil)
	held := func(a claims.Action, selector string) bool {
		if !hasPolicyGrants(sel.Contributions) {
			return false
		}
		item := claims.ScopeItem{Action: a, Selector: selector}
		if selector == "@any" {
			item.Selector = ""
		}
		policyItems = append(policyItems, item.String())
		return true
	}
	narrowed, err := claims.NarrowFrozen(bundle, items, held)
	if err != nil {
		return failed(res, err), nil
	}
	if len(policyItems) > 0 {
		res.Warnings = append(res.Warnings, claims.Warning{
			Code: WarnScopeNotChecked, Pointer: "/scope",
			Detail: "the scope asks for " + strings.Join(policyItems, ", ") + ", which only a listed policy or group may give; the dry run reads no policy and took the item as held",
		})
	}
	res.Selected = make([]string, 0, len(sel.Contributions))
	for _, c := range sel.Contributions {
		res.Selected = append(res.Selected, c.Mapping.Name)
	}
	grants := unionRendered(sel.Contributions)
	grants.Inline = nil
	for _, m := range narrowed.Contributions {
		grants.Inline = append(grants.Inline, m.Inline...)
	}
	res.Grants = &grants
	res.Scope = strings.Join(append(slices.Clone(policyItems), strings.Fields(narrowed.InlineScope())...), " ")
	ttl, failure := s.dryRunTTL(is, sel.Contributions, req)
	if failure != "" {
		res.Failure, res.Detail = failure, "the subject token has less lifetime left than ci.token.min_ttl"
		res.Grants, res.Scope = nil, ""
		res.Selected = []string{}
		return res, nil
	}
	res.TTLSeconds = int64(ttl / time.Second)
	return res, nil
}

// failed is a dry run that stops at a refusal of the scope.
func failed(res EvaluateResult, err error) EvaluateResult {
	var ce *claims.Error
	if errors.As(err, &ce) {
		res.Failure, res.Detail = ce.Reason, ce.Detail
		return res
	}
	res.Failure, res.Detail = reason.Internal, err.Error()
	return res
}

func hasPolicyGrants(cs []Contribution) bool {
	return slices.ContainsFunc(cs, func(c Contribution) bool { return len(c.Rendered.Policies) > 0 || len(c.Rendered.Groups) > 0 })
}

// bundleOf builds the frozen bundle of a selection, the way the exchange does: one entry per contribution.
func (s *MappingService) bundleOf(is Issuer, sel Selection, c claims.Claims) claims.Frozen {
	projectID, _ := c.First(claims.ClaimProjectID)
	projectPath, _ := c.First("project_path")
	f := claims.Frozen{Version: claims.FrozenVersion, Issuer: is.Name, IssuerID: is.ID, ProjectID: projectID, ProjectPath: projectPath, CrossProject: sel.CrossProject}
	for _, ct := range sel.Contributions {
		f.Add(ct.Frozen())
	}
	return f
}

// checkDryRunTimes is the time check of the dry run: only when the request carries `now` and the `exp` and
// `iat` claims (R-CI-019), with the issuer's clock skew, by the rule of the exchange.
func checkDryRunTimes(req EvaluateRequest, is Issuer) (reason.Reason, string) {
	if req.Now == nil {
		return "", ""
	}
	if _, hasExp := req.Claims["exp"]; !hasExp {
		return "", ""
	}
	if _, hasIat := req.Claims["iat"]; !hasIat {
		return "", ""
	}
	err := subject.CheckTimes(req.Claims, time.Duration(is.ClockSkewSeconds)*time.Second, time.Unix(*req.Now, 0))
	var f *subject.Failure
	if errors.As(err, &f) {
		return f.Reason, f.Detail
	}
	return "", ""
}

// dryRunTTL is the lifetime formula of R-CI-016 for a request without `ttl`: the minimum of the default,
// the mappings' and the issuer's max_ttl, ci.token.max_ttl and, when the request carries `now` and `exp`,
// the remaining life of the subject token. A result below ci.token.min_ttl is
// subject_token_expires_too_soon.
func (s *MappingService) dryRunTTL(is Issuer, cs []Contribution, req EvaluateRequest) (time.Duration, reason.Reason) {
	cfg := s.o.Config.Token
	ttl := min(cfg.DefaultTTL, cfg.MaxTTL)
	if is.MaxTTLSeconds != nil {
		ttl = min(ttl, time.Duration(*is.MaxTTLSeconds)*time.Second)
	}
	for _, c := range cs {
		if c.Mapping.MaxTTLSeconds != nil {
			ttl = min(ttl, time.Duration(*c.Mapping.MaxTTLSeconds)*time.Second)
		}
	}
	if req.Now != nil {
		if exp, ok := req.Claims.First("exp"); ok {
			if n, err := strconv.ParseInt(exp, 10, 64); err == nil {
				ttl = min(ttl, time.Duration(n-*req.Now)*time.Second)
			}
		}
	}
	if ttl < cfg.MinTTL {
		return 0, reason.SubjectTokenExpiresTooSoon
	}
	return ttl, ""
}
