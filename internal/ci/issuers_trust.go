package ci

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The issuer changes that move trust (Q-CI-50), checked and applied in the transaction of an issuer write that holds
// the issuer row FOR UPDATE: a new key location, and a pin setting that the mappings were saved against.

// keyLocationChanged reports whether a write moves the location the key set comes from: jwks_uri, or discovery_url
// while no jwks_uri overrides it (R-CI-001).
func keyLocationChanged(before, next Issuer) bool {
	return derefStr(before.JWKSURI) != derefStr(next.JWKSURI) || derefStr(before.DiscoveryURL) != derefStr(next.DiscoveryURL)
}

// keyLocation is the URL a key row of the issuer names before its first fetch from it.
func keyLocation(is Issuer) string {
	for _, u := range []string{derefStr(is.JWKSURI), derefStr(is.DiscoveryURL)} {
		if u != "" {
			return u
		}
	}
	return is.Issuer
}

// resetKeysTx replaces the stored set of an issuer whose key location changed with the fail-closed placeholder (an
// empty set, fetched_at at the epoch, the next refresh due at once) and drops the key copies of every replica: the set
// came from the old location, and no key of it may validate a token once the write committed (R-CI-003, Q-CI-50). An
// issuer without a row has nothing to reset; its first fetch reads the new location.
func (s *Service) resetKeysTx(ctx context.Context, tx meta.Tx, qs *q.Queries, next Issuer) error {
	n, err := qs.CiKeysReset(ctx, q.CiKeysResetParams{IssuerID: next.ID, JwksUri: keyLocation(next)})
	if err != nil || n == 0 {
		return err
	}
	return s.o.Changes.Notify(tx, cluster.AreaCIIssuerKeys, changePayloadVersion, issuerChange{Issuer: next.ID})
}

// pinsNarrowed reports whether a write could unpin a saved mapping: public_issuer switched on (pin_required starts to
// apply) or a claim removed from pin_claims (a pin on it no longer counts).
func pinsNarrowed(before, next Issuer) bool {
	if next.PublicIssuer && !before.PublicIssuer {
		return true
	}
	for _, c := range before.PinClaims {
		if !slices.Contains(next.PinClaims, c) {
			return true
		}
	}
	return false
}

// checkPinsTx refuses an issuer write that would leave a mapping the pin rules of a save refuse under the new settings
// (R-CI-005, R-CI-010, Q-CI-50): pin_required on a public issuer, unpinned_write on a private one, judged with the
// actions the mapping's policies and groups give it now. The answer is 409 mappings_unpinned with the names, so the
// administrator pins or deletes them first; trove migrate stops with the same error for a declared issuer.
func (s *Service) checkPinsTx(ctx context.Context, qs *q.Queries, before, next Issuer) error {
	if !pinsNarrowed(before, next) {
		return nil
	}
	names, err := unpinnedMappings(ctx, qs, next)
	if err != nil || len(names) == 0 {
		return err
	}
	return errs.E(errs.Conflict, errs.CodeMappingsUnpinned,
		fmt.Sprintf("The change would leave %d mappings of the issuer %q unpinned (%s): pin them on one of %s, or delete them, first.",
			len(names), next.Name, strings.Join(names, ", "), strings.Join(next.PinClaims, ", ")),
		errs.WithExtension("mappings", names))
}

// unpinnedMappings lists the mappings of the issuer that fail claims.PinViolation under its settings next, in
// evaluation order.
func unpinnedMappings(ctx context.Context, qs *q.Queries, next Issuer) ([]string, error) {
	rows, err := qs.CiMappingsOfIssuerID(ctx, next.ID)
	if err != nil {
		return nil, err
	}
	ms, err := readMappings(ctx, qs, listRows(rows))
	if err != nil || len(ms) == 0 {
		return nil, err
	}
	pids := make([]string, 0, len(ms))
	for _, m := range ms {
		pids = append(pids, m.PrincipalID)
	}
	binds, err := qs.CiMappingPolicyBindings(ctx, pids)
	if err != nil {
		return nil, err
	}
	facts := claims.IssuerFacts{Kind: next.Kind, PublicIssuer: next.PublicIssuer, PinClaims: next.PinClaims, RequiredClaims: next.RequiredClaims}
	var out []string
	for _, m := range ms {
		var acts []claims.Action
		for _, b := range binds {
			// A project tier is granted by its project's own policies only (Q-CI-26), as the evaluator counts them.
			if b.PrincipalID == m.PrincipalID && (m.Project == "" || b.Project == m.Project) {
				for _, a := range b.Actions {
					acts = append(acts, claims.Action(a))
				}
			}
		}
		if len(m.Grants.Groups) > 0 && m.Project == "" {
			groupActs, err := qs.CiGroupActions(ctx, m.Grants.Groups)
			if err != nil {
				return nil, err
			}
			for _, a := range groupActs {
				acts = append(acts, claims.Action(a))
			}
		}
		if claims.PinViolation(facts, claims.SaveInput{Name: m.Name, Match: m.Match, Grants: m.Grants, PolicyActions: acts}) != nil {
			out = append(out, m.Name)
		}
	}
	return out, nil
}
