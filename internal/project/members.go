package project

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// parseAdminRef reads the reference of an administrator. A malformed reference and a
// kind that cannot administer are 422 (Q-AUTHZ-183); the second is reported by ok.
func parseAdminRef(ref string) (kind PrincipalKind, name string, ok bool, err error) {
	r, perr := authz.ParseRef(ref)
	if perr != nil {
		return "", "", false, errs.E(errs.Invalid, errs.CodeValidation, perr.Error(), errs.WithFields(errs.FieldError{
			Pointer: "/principalRef", Code: "invalid_principal", Detail: perr.Error(),
		}))
	}
	switch r.Kind {
	case principal.KindUser:
		return KindUser, r.Name, true, nil
	case principal.KindGroup:
		return KindGroup, r.Name, true, nil
	}
	return "", r.Name, false, nil
}

func notAdministrator(ref string) error {
	if strings.HasPrefix(ref, "bastion:") {
		return principal.BastionNotAllowed("the administrator of a project") // R-BAS-004
	}
	detail := fmt.Sprintf("%q cannot administer a project: only users and groups do.", ref)
	return errs.E(errs.Invalid, errs.CodeValidation, detail, errs.WithFields(errs.FieldError{
		Pointer: "/principalRef", Code: "principal_kind_not_allowed", Detail: detail,
	}))
}

// principalOf reads the user or group a reference names, in the writer's
// transaction.
func principalOf(ctx context.Context, qs *q.Queries, kind PrincipalKind, name string) (PrincipalRef, bool, error) {
	rows, err := qs.ProjectPrincipalByRef(ctx, q.ProjectPrincipalByRefParams{Kind: string(kind), Name: name})
	if err != nil || len(rows) == 0 {
		return PrincipalRef{}, false, err
	}
	return PrincipalRef{Kind: kind, Name: rows[0].Name, ID: rows[0].ID}, true, nil
}

// LookupPrincipals answers the lookup of Q-AUTHZ-182. The bounds are the service's
// own, so that no caller can widen them: a prefix of at least MinLookupPrefix
// characters, at most MaxLookup results.
func (s *service) LookupPrincipals(ctx context.Context, prefix string, kind PrincipalKind, limit int) ([]PrincipalRef, error) {
	var fields []errs.FieldError
	if utf8.RuneCountInString(prefix) < MinLookupPrefix {
		fields = append(fields, errs.FieldError{
			Pointer: "/q", Code: "prefix_too_short",
			Detail: fmt.Sprintf("The prefix has at least %d characters.", MinLookupPrefix),
		})
	}
	if kind != "" && kind != KindUser && kind != KindGroup {
		fields = append(fields, errs.FieldError{Pointer: "/kind", Code: "invalid_kind", Detail: "The kind is user or group."})
	}
	if limit < 0 {
		fields = append(fields, errs.FieldError{Pointer: "/limit", Code: "invalid_limit", Detail: fmt.Sprintf("The limit is 1 to %d.", MaxLookup)})
	}
	if len(fields) > 0 {
		return nil, errs.E(errs.Invalid, errs.CodeValidation, fields[0].Detail, errs.WithFields(fields...))
	}
	if limit == 0 || limit > MaxLookup {
		limit = MaxLookup
	}
	var out []PrincipalRef
	err := s.inTx(ctx, func(_ meta.Tx, qs *q.Queries) error {
		rows, err := qs.ProjectLookupPrincipals(ctx, q.ProjectLookupPrincipalsParams{Kind: string(kind), Prefix: prefix, MaxRows: int32(limit)}) //nolint:gosec // at most MaxLookup
		if err != nil {
			return err
		}
		out = make([]PrincipalRef, 0, len(rows))
		for _, r := range rows {
			out = append(out, PrincipalRef{Kind: PrincipalKind(r.Kind), Name: r.Name})
		}
		return nil
	})
	return out, err
}
