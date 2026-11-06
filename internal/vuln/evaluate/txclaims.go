package evaluate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// txClaims is the claim index of one writing transaction: the claims of the local repositories of a format that cover a name and
// the holders' settings, read in the transaction with one statement per format and name, so that the commit's evaluation takes no
// second pool connection (the replica's index and snapshot load through the pool on a miss). It sees a claim the transaction
// wrote itself, such as the auto claim of a first publish, and a repository setting committed before the statement. The patterns
// are matched by [repository.MatchClaims], the rule is [ClaimRule]'s (R-VULN-010, Q-VULN-70). It is used by one goroutine.
type txClaims struct {
	ev      *store.Eval
	holders map[string]ClaimHolder
	read    map[string][]repository.ClaimMatch
}

var _ HolderIndex = (*txClaims)(nil)

func newTxClaims(ev *store.Eval) *txClaims {
	return &txClaims{ev: ev, holders: map[string]ClaimHolder{}, read: map[string][]repository.ClaimMatch{}}
}

// ClaimsFor implements [ClaimIndex]. The name is the stored, normalized name.
func (t *txClaims) ClaimsFor(ctx context.Context, format, name string) ([]repository.ClaimMatch, error) {
	k := format + "\x00" + name
	if m, ok := t.read[k]; ok {
		return m, nil
	}
	rows, err := t.ev.ClaimHolders(ctx, format, name)
	if err != nil {
		return nil, err
	}
	if err := t.prime(format, name, rows); err != nil {
		return nil, err
	}
	return t.read[k], nil
}

// prime stores the holders a caller read together with its other statements (the commit hook's one round trip) as if ClaimsFor
// had read them.
func (t *txClaims) prime(format, name string, rows []store.ClaimHolder) error {
	k := format + "\x00" + name
	claims := make([]repository.StoredClaim, len(rows))
	for i, r := range rows {
		var cfg rowConfig
		if err := json.Unmarshal([]byte(r.Config), &cfg); err != nil {
			return fmt.Errorf("the document of repository %d: %w", r.RepoID, err)
		}
		h := ClaimHolder{Key: r.Key, Format: format, Class: repository.Local, Settings: vuln.DefaultSettings()}
		if cfg.Vulnerabilities != nil {
			h.Settings = *cfg.Vulnerabilities
		}
		t.holders[r.Key] = h
		claims[i] = repository.StoredClaim{RepoID: r.RepoID, Repository: r.Key, Pattern: r.Pattern, Scope: r.Scope, Source: r.Source}
	}
	t.read[k] = repository.MatchClaims(claims, name)
	return nil
}

// Holder implements [HolderIndex].
func (t *txClaims) Holder(key string) (ClaimHolder, bool) {
	h, ok := t.holders[key]
	return h, ok
}
