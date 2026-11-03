package sync

import (
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/vuln/match"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// R-VULN-006, Q-VULN-61 (S1 of the fix wave): a withdrawn record is never the canonical record of a group that has another, even
// a malicious one; a group of withdrawn records only still has one.
func TestR_VULN_006_WithdrawnRecordIsNeverCanonical(t *testing.T) {
	row := func(id string, withdrawn bool) store.GroupRow { return store.GroupRow{ID: id, Withdrawn: withdrawn} }
	for _, tt := range []struct {
		name    string
		members []store.GroupRow
		want    string
	}{
		{"a withdrawn MAL- yields to GHSA-", []store.GroupRow{row("GHSA-aaaa", false), row("MAL-2026-1", true)}, "GHSA-aaaa"},
		{"an active MAL- still wins", []store.GroupRow{row("GHSA-aaaa", false), row("MAL-2026-2", false), row("MAL-2026-1", true)}, "MAL-2026-2"},
		{"a withdrawn GHSA- yields to a CVE", []store.GroupRow{row("CVE-2026-9", false), row("GHSA-aaaa", true)}, "CVE-2026-9"},
		{"withdrawn records only keep the order", []store.GroupRow{row("GHSA-aaaa", true), row("MAL-2026-1", true)}, "MAL-2026-1"},
	} {
		if got := canonicalOfRows(tt.members); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}

// R-VULN-004 (C8 of the fix wave): the batches of the second pass hold at most BatchSize records and at most BatchBytes of record
// bytes; a record above the bound is a batch of its own, and every record is in exactly one batch.
func TestR_VULN_004_BatchesAreCutByBytesAsWellAsCount(t *testing.T) {
	var metas []entryMeta
	for range 2500 {
		metas = append(metas, entryMeta{size: 1 << 10})
	}
	if ends := batchEnds(metas); !slices.Equal(ends, []int{1000, 2000, 2500}) {
		t.Errorf("small records: %v", ends)
	}
	// 20 records of 3 MiB: ten fit in 32 MiB, the eleventh starts the next batch.
	metas = nil
	for range 20 {
		metas = append(metas, entryMeta{size: 3 << 20})
	}
	if ends := batchEnds(metas); !slices.Equal(ends, []int{10, 20}) {
		t.Errorf("3 MiB records: %v", ends)
	}
	// One record above the bound stands alone, between the others.
	metas = []entryMeta{{size: 1 << 10}, {size: BatchBytes + 1}, {size: 1 << 10}}
	if ends := batchEnds(metas); !slices.Equal(ends, []int{1, 2, 3}) {
		t.Errorf("a huge record: %v", ends)
	}
	if ends := batchEnds(nil); len(ends) != 0 {
		t.Errorf("no records: %v", ends)
	}
}

// R-VULN-004, Q-VULN-63 (A10 of the fix wave): a state whose adapter version is below the release's is stale, a current one is not.
func TestR_VULN_004_AdapterVersionForcesAnImport(t *testing.T) {
	if !adapterStale("npm", store.SyncState{}) || !adapterStale("NuGet", store.SyncState{AdapterVersion: 0}) {
		t.Error("a state stored before the column is not stale")
	}
	if adapterStale("npm", store.SyncState{AdapterVersion: match.AdapterVersion("npm")}) {
		t.Error("a current state is stale")
	}
}
