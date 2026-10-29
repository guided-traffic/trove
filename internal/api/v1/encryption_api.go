package v1

import (
	"context"
	"net/http"
	"sort"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/secrets"
)

// The status of the stored-secret key ring (R-API-049, R-ARCH-021). The
// implementation of the per-key counts is internal/secrets (shared with
// `trove admin secrets status`); this file adds the replicas' rings from the
// registry, the canary state and the compatibility rule.

// KindSecretsReencrypt is the job kind and task name of the re-encryption
// (R-ARCH-021, R-API-049).
const KindSecretsReencrypt = "secrets-reencrypt" //nolint:gosec // a job kind, not a credential

// RingsCompatible is the rule of R-API-049: every live replica's ring holds
// every key ID that is active on any live replica or referenced by a sealed
// value. False is the state in which a value sealed by one replica cannot be
// opened by another. A lost replica is left out; so are roles that open no
// sealed value.
func RingsCompatible(rings []ReplicaRing, referenced []string) bool {
	need := map[string]bool{}
	for _, id := range referenced {
		need[id] = true
	}
	var live []ReplicaRing
	for _, r := range rings {
		if r.Lost || !opensSecrets(r.Role) {
			continue
		}
		live = append(live, r)
		need[r.ActiveKeyID] = true
	}
	for _, r := range live {
		have := map[string]bool{}
		for _, id := range r.KeyIDs {
			have[id] = true
		}
		for id := range need {
			if !have[id] {
				return false
			}
		}
	}
	return true
}

func opensSecrets(role string) bool { return role == "web" || role == "worker" || role == "all" }

// keyRingPrecondition builds the precondition of the secrets-reencrypt trigger:
// 409 key_ring_incompatible while RingsCompatible is false (R-OPS-019).
func (s *Server) keyRingPrecondition(ctx context.Context) error {
	st, rings, err := s.ringState(ctx)
	if err != nil {
		return err
	}
	if !RingsCompatible(rings, referencedKeys(st)) {
		return errs.E(errs.Conflict, errs.CodeKeyRingIncompatible,
			"A live replica's key ring lacks a key that is active on another replica or referenced by a sealed value; roll the key out everywhere first.")
	}
	return nil
}

func referencedKeys(st secrets.Status) []string {
	var ids []string
	for _, k := range st.Keys {
		if k.SealedValues != nil && *k.SealedValues > 0 {
			ids = append(ids, k.KeyID)
		}
	}
	return ids
}

// ringState reads the answering replica's status with the sealed counts, and the
// rings of the registry.
func (s *Server) ringState(ctx context.Context) (secrets.Status, []ReplicaRing, error) {
	if s.o.API.KeyRing == nil || s.o.API.Sealed == nil || s.o.API.Rings == nil {
		return secrets.Status{}, nil, unavailable("The key-ring status")
	}
	st, err := s.o.API.KeyRing.StatusWithCounts(ctx, s.o.API.Sealed)
	if err != nil {
		return secrets.Status{}, nil, err
	}
	rings, err := s.o.API.Rings.Rings(ctx)
	if err != nil {
		return secrets.Status{}, nil, err
	}
	return st, rings, nil
}

// GetEncryptionStatus implements GET /api/v1/system/encryption (R-API-049).
func (s *Server) GetEncryptionStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, rings, err := s.ringState(ctx)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	canaries := map[string]string{}
	var last *gen.LastReencryption
	err = s.o.API.Store.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		rows, err := qs.ListSecretKeyCanaries(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			canaries[row.KeyID] = row.Ciphertext
		}
		runs, err := qs.LastJobPerKind(ctx, []string{KindSecretsReencrypt})
		if err != nil || len(runs) == 0 {
			return err
		}
		l := gen.LastReencryption{JobId: parseUUID(runs[0].ID.String()), State: gen.JobState(runs[0].State)}
		if runs[0].FinishedAt.Valid {
			t := runs[0].FinishedAt.Time.UTC()
			l.FinishedAt = &t
		}
		last = &l
		return nil
	})
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := gen.EncryptionStatus{
		AnsweredBy: s.o.Identity.Replica, ActiveKeyId: st.ActiveKeyID, Keys: make([]gen.EncryptionKey, 0, len(st.Keys)),
		Replicas: []gen.EncryptionReplica{}, RingsCompatible: RingsCompatible(rings, referencedKeys(st)), LastReencryption: last,
	}
	for _, k := range st.Keys {
		ek := gen.EncryptionKey{Id: k.KeyID, Active: k.Active, Canary: s.canaryState(k, canaries)}
		if k.SealedValues != nil {
			ek.SealedValues = *k.SealedValues
		}
		out.Keys = append(out.Keys, ek)
	}
	for _, rep := range rings {
		if rep.Lost || !opensSecrets(rep.Role) {
			continue
		}
		ids := append([]string{}, rep.KeyIDs...)
		sort.Strings(ids)
		out.Replicas = append(out.Replicas, gen.EncryptionReplica{
			Name: rep.Name, Role: gen.EncryptionReplicaRole(rep.Role), ActiveKeyId: rep.ActiveKeyID, KeyIds: ids,
		})
	}
	sort.Slice(out.Replicas, func(i, j int) bool { return out.Replicas[i].Name < out.Replicas[j].Name })
	writeJSON(w, r, http.StatusOK, out)
}

// canaryState is the canary of one key ID as the answering replica sees it: ok
// when its ring opens the stored canary, failed when the row exists and the ring
// cannot open it (a missing key, or other key material under the same ID),
// absent when no row exists yet.
func (s *Server) canaryState(k secrets.KeyStatus, canaries map[string]string) gen.EncryptionKeyCanary {
	sealed, has := canaries[k.KeyID]
	switch {
	case !has:
		return gen.EncryptionKeyCanaryAbsent
	case !k.Loaded || s.o.API.KeyRing.CheckCanary(k.KeyID, sealed) != nil:
		return gen.EncryptionKeyCanaryFailed
	}
	return gen.EncryptionKeyCanaryOk
}
