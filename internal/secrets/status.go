package secrets

import (
	"context"
	"fmt"
	"sort"
)

// KeyStatus describes one key ID.
type KeyStatus struct {
	KeyID  string `json:"keyId"`
	Active bool   `json:"active"`
	// Loaded is true for a key this replica holds. A key ID that sealed values
	// still reference but the ring lacks appears with Loaded false.
	Loaded bool `json:"loaded"`
	// SealedValues counts the values under this key ID over every registered
	// column; nil when the counts were not asked for.
	SealedValues *int64 `json:"sealedValues,omitempty"`
}

// Status is the key-ring status that `trove admin secrets status` and
// GET /api/v1/system/encryption share (R-ARCH-021). It carries key IDs and
// counts only, never key material, fingerprints, ciphertext or plaintext.
type Status struct {
	ActiveKeyID string      `json:"activeKeyId"`
	KeyIDs      []string    `json:"keyIds"`
	Keys        []KeyStatus `json:"keys"`
	// Columns lists the registered sealed columns the counts walk.
	Columns []string `json:"columns"`
}

// ValueCounter counts the sealed values of a column per key ID. The
// PostgreSQL layer implements it; the secrets package stays free of SQL.
type ValueCounter interface {
	CountSealed(ctx context.Context, c Column) (map[string]int64, error)
}

// Status reports the ring of this replica without database access.
func (r *KeyRing) Status() Status {
	st := Status{ActiveKeyID: r.active, KeyIDs: r.KeyIDs()}
	for _, id := range r.order {
		st.Keys = append(st.Keys, KeyStatus{KeyID: id, Active: id == r.active, Loaded: true})
	}
	for _, c := range Columns() {
		st.Columns = append(st.Columns, c.String())
	}
	return st
}

// StatusWithCounts adds the sealed values per key ID, summed over the registry.
// Key IDs that values reference but the ring lacks are listed with Loaded false.
func (r *KeyRing) StatusWithCounts(ctx context.Context, counter ValueCounter) (Status, error) {
	st := r.Status()
	totals := map[string]int64{}
	for _, c := range Columns() {
		per, err := counter.CountSealed(ctx, c)
		if err != nil {
			return Status{}, fmt.Errorf("secrets: count sealed values of %s: %w", c, err)
		}
		for id, n := range per {
			totals[id] += n
		}
	}
	index := map[string]int{}
	for i, k := range st.Keys {
		index[k.KeyID] = i
	}
	var extra []string
	for id := range totals {
		if _, ok := index[id]; !ok {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	for _, id := range extra {
		st.Keys = append(st.Keys, KeyStatus{KeyID: id})
		index[id] = len(st.Keys) - 1
	}
	for i := range st.Keys {
		n := totals[st.Keys[i].KeyID]
		st.Keys[i].SealedValues = &n
	}
	return st, nil
}
