//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// patchMember is a merge patch of one top-level member.
func patchMember(member string, value any) repository.Patcher {
	return func(cur []byte) ([]byte, error) {
		var doc map[string]any
		if err := json.Unmarshal(cur, &doc); err != nil {
			return nil, err
		}
		doc[member] = value
		return json.Marshal(doc)
	}
}

func virtualSection(members []string, rules ...map[string]any) map[string]any {
	pins := []map[string]any{}
	pins = append(pins, rules...)
	return map[string]any{"members": members, "deployTarget": nil, "mergedMetadataTtlSeconds": 7200, "pinning": map[string]any{"rules": pins}}
}

// since returns the events the recorder kept after the first n.
func (r *recorder) since(n int) []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events[n:])
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func eventTypes(evs []audit.Event) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.Type
	}
	return out
}

// narrowedOf returns the entry and the details parts of the one repository.narrowed among the events.
func narrowedOf(t *testing.T, evs []audit.Event) (entry map[string]json.RawMessage, parts []string) {
	t.Helper()
	for _, ev := range evs {
		if ev.Type != repository.EventNarrowed {
			continue
		}
		if entry != nil {
			t.Fatal("two repository.narrowed events in one update")
		}
		if err := json.Unmarshal(ev.Journal, &entry); err != nil {
			t.Fatalf("journal entry %s: %v", ev.Journal, err)
		}
		var d struct {
			Class, Format, Project string
			Parts                  []string
		}
		if err := json.Unmarshal(ev.Details, &d); err != nil {
			t.Fatal(err)
		}
		if d.Class == "" || d.Format == "" || d.Project != "it" {
			t.Errorf("details = %s", ev.Details)
		}
		parts = d.Parts
		var key string
		_ = json.Unmarshal(entry["key"], &key)
		if key == "" || ev.Repository != key {
			t.Errorf("repository column = %q, entry key = %s", ev.Repository, entry["key"])
		}
	}
	return entry, parts
}

// TestR_REPO_061_ANarrowingUpdateWritesItsEntryInTheSameTransaction: an update that narrows records repository.narrowed beside
// repository.updated with only the parts that narrowed, in the transaction of the update; an update that narrows nothing, one
// that only widens, a refused one and a create record none.
func TestR_REPO_061_ANarrowingUpdateWritesItsEntryInTheSameTransaction(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("narrowed-a", replicaOpts{})
	ctx := context.Background()
	a.mustCreate("npm-local", "local", "npm", `"local":{"autoClaim":false}`)
	a.mustCreate("npmjs", "remote", "npm", "")
	a.mustCreate("npm-all", "virtual", "npm", `"virtual":{"members":["npm-local","npmjs"]}`)
	if got := a.rec.types(); slices.Contains(got, repository.EventNarrowed) {
		t.Fatalf("a create recorded %v", got)
	}
	patch := func(key, member string, value any) ([]audit.Event, error) {
		n := a.rec.count()
		_, err := a.svc.Patch(ctx, caller, qr(key), repository.Precondition{Any: true}, patchMember(member, value))
		return a.rec.since(n), err
	}

	t.Run("R-REPO-061 an exclude pattern added is journaled with that part only", func(t *testing.T) {
		evs, err := patch("npmjs", "excludePatterns", []string{"@evil/*"})
		if err != nil {
			t.Fatal(err)
		}
		if got := eventTypes(evs); !slices.Equal(got, []string{repository.EventUpdated, repository.EventNarrowed}) {
			t.Fatalf("events = %v", got)
		}
		entry, parts := narrowedOf(t, evs)
		if !slices.Equal(parts, []string{"excludeAdded"}) || string(entry["excludeAdded"]) != `["@evil/*"]` || len(entry) != 3 {
			t.Fatalf("entry = %v parts = %v", entry, parts)
		}
	})
	t.Run("R-REPO-061 a description, a widening and a repeated value record no narrowing", func(t *testing.T) {
		for name, tc := range map[string]struct {
			key, member string
			value       any
		}{
			"a description":                  {"npmjs", "description", "the public registry"},
			"an exclude list emptied":        {"npmjs", "excludePatterns", []string{}},
			"an include list set to nothing": {"npmjs", "includePatterns", []string{}},
		} {
			evs, err := patch(tc.key, tc.member, tc.value)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got := eventTypes(evs); slices.Contains(got, repository.EventNarrowed) {
				t.Errorf("%s recorded %v", name, got)
			}
		}
	})
	t.Run("R-REPO-061 a mixed include update carries the new list, a widening one nothing", func(t *testing.T) {
		if evs, err := patch("npmjs", "includePatterns", []string{"@a/*", "@b/*"}); err != nil || !slices.Contains(eventTypes(evs), repository.EventNarrowed) {
			t.Fatalf("include from empty: %v %v", eventTypes(evs), err)
		}
		evs, err := patch("npmjs", "includePatterns", []string{"@a/*", "@c/*"})
		if err != nil {
			t.Fatal(err)
		}
		entry, parts := narrowedOf(t, evs)
		if !slices.Equal(parts, []string{"include"}) || string(entry["include"]) != `["@a/*","@c/*"]` {
			t.Fatalf("entry = %v parts = %v", entry, parts)
		}
		evs, err = patch("npmjs", "includePatterns", []string{"@a/*", "@c/*", "@d/*"})
		if err != nil || slices.Contains(eventTypes(evs), repository.EventNarrowed) {
			t.Fatalf("a pattern added recorded %v, %v", eventTypes(evs), err)
		}
	})
	t.Run("R-REPO-061 autoClaim switched on is journaled", func(t *testing.T) {
		evs, err := patch("npm-local", "local", map[string]any{"autoClaim": true})
		if err != nil {
			t.Fatal(err)
		}
		entry, parts := narrowedOf(t, evs)
		if !slices.Equal(parts, []string{"autoClaim"}) || string(entry["autoClaim"]) != "true" {
			t.Fatalf("entry = %v parts = %v", entry, parts)
		}
		if evs, _ := patch("npm-local", "local", map[string]any{"autoClaim": false}); slices.Contains(eventTypes(evs), repository.EventNarrowed) {
			t.Error("switching autoClaim off recorded a narrowing")
		}
	})
	t.Run("R-REPO-061 a member out and a pin rule added are journaled by repository ID", func(t *testing.T) {
		rule := map[string]any{"packages": []string{"@acme/*"}, "members": []string{"npm-local"}}
		evs, err := patch("npm-all", "virtual", virtualSection([]string{"npm-local", "npmjs"}, rule))
		if err != nil {
			t.Fatal(err)
		}
		entry, parts := narrowedOf(t, evs)
		localID := e.scalar(`SELECT id FROM repositories WHERE key = 'npm-local'`)
		var added []struct {
			Pattern string
			Members []struct{ RepositoryID, Key string }
		}
		_ = json.Unmarshal(entry["pinsAdded"], &added)
		if !slices.Equal(parts, []string{"pinsAdded"}) || len(added) != 1 || added[0].Pattern != "@acme/*" || len(added[0].Members) != 1 ||
			added[0].Members[0].RepositoryID != itoa(localID) || added[0].Members[0].Key != "it/npm-local" {
			t.Fatalf("entry = %v parts = %v", entry, parts)
		}
		evs, err = patch("npm-all", "virtual", virtualSection([]string{"npmjs"}))
		if err != nil {
			t.Fatal(err)
		}
		entry, parts = narrowedOf(t, evs)
		if !slices.Equal(parts, []string{"membersRemoved"}) {
			t.Fatalf("entry = %v parts = %v", entry, parts)
		}
		var removed []struct{ RepositoryID, Key string }
		_ = json.Unmarshal(entry["membersRemoved"], &removed)
		if len(removed) != 1 || removed[0].RepositoryID != itoa(localID) || removed[0].Key != "it/npm-local" {
			t.Fatalf("membersRemoved = %v", removed)
		}
		if evs, _ := patch("npm-all", "virtual", virtualSection([]string{"npmjs", "npm-local"})); slices.Contains(eventTypes(evs), repository.EventNarrowed) {
			t.Error("a member added recorded a narrowing")
		}
	})
	t.Run("R-REPO-061 a stronger vulnerabilities member is journaled with both values, a weaker one is not", func(t *testing.T) {
		if evs, err := patch("npmjs", "vulnerabilities", map[string]any{"blockMalicious": false}); err != nil || slices.Contains(eventTypes(evs), repository.EventNarrowed) {
			t.Fatalf("a weaker member: %v %v", eventTypes(evs), err)
		}
		evs, err := patch("npmjs", "vulnerabilities", map[string]any{"blockMalicious": true, "onMissingData": "block"})
		if err != nil {
			t.Fatal(err)
		}
		entry, parts := narrowedOf(t, evs)
		var v struct{ Before, After map[string]any }
		_ = json.Unmarshal(entry["vulnerabilities"], &v)
		if !slices.Equal(parts, []string{"vulnerabilities"}) || v.Before["blockMalicious"] != false || v.After["blockMalicious"] != true || v.After["onMissingData"] != "block" {
			t.Fatalf("entry = %v parts = %v", entry, parts)
		}
	})
	t.Run("R-REPO-061 a refused or failed update records nothing", func(t *testing.T) {
		n := a.rec.count()
		_, err := a.svc.Patch(ctx, caller, qr("npmjs"), repository.MatchVersion(1), patchMember("excludePatterns", []string{"@stale/*"}))
		wantCode(t, err, 412, errs.CodePreconditionFailed)
		a.rec.setFail(true)
		_, err = a.svc.Patch(ctx, caller, qr("npmjs"), repository.Precondition{Any: true}, patchMember("excludePatterns", []string{"@evil/*", "@new/*"}))
		a.rec.setFail(false)
		if err == nil {
			t.Fatal("an update whose audit record fails succeeded")
		}
		if evs := a.rec.since(n); len(evs) != 0 {
			t.Errorf("events = %v", eventTypes(evs))
		}
		st, err := a.svc.Get(ctx, qr("npmjs"))
		if err != nil || slices.Contains(st.Document.ExcludePatterns, "@new/*") {
			t.Errorf("the failed update left %v (%v)", st.Document.ExcludePatterns, err)
		}
	})
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// TestR_REPO_061_EveryClaimsChangeWritesItsEntry: a claims write of the API and the auto claim of a first publish carry a
// journal entry, a removal or a narrowing with an empty added list, and the audit row names the repository.
func TestR_REPO_061_EveryClaimsChangeWritesItsEntry(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("claims-entry", replicaOpts{})
	ctx := context.Background()
	a.mustCreate("npm-local", "local", "npm", `"local":{"autoClaim":true}`)
	id := itoa(e.scalar(`SELECT id FROM repositories WHERE key = 'npm-local'`))
	put := func(in repository.ClaimInput) []audit.Event {
		n := a.rec.count()
		if _, err := a.svc.PutClaims(ctx, caller, qr("npm-local"), repository.Precondition{Any: true}, in, allowGlobal); err != nil {
			t.Fatal(err)
		}
		return a.rec.since(n)
	}
	entryOf := func(evs []audit.Event) (e struct {
		RepositoryID, Key, Source string
		Added                     []struct{ Pattern, Scope string }
	},
	) {
		t.Helper()
		if len(evs) != 1 || evs[0].Type != repository.EventClaimsChanged {
			t.Fatalf("events = %v", eventTypes(evs))
		}
		if evs[0].Repository != "it/npm-local" {
			t.Errorf("repository column = %q", evs[0].Repository)
		}
		if err := json.Unmarshal(evs[0].Journal, &e); err != nil {
			t.Fatalf("entry %s: %v", evs[0].Journal, err)
		}
		return e
	}

	t.Run("R-REPO-061 a manual claim", func(t *testing.T) {
		got := entryOf(put(repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@acme/*"}, {Pattern: "left-*", Scope: "global"}}}))
		if got.RepositoryID != id || got.Key != "it/npm-local" || got.Source != "manual" || len(got.Added) != 2 {
			t.Fatalf("entry = %+v", got)
		}
	})
	t.Run("R-REPO-061 a widening to global is part of the entry, a narrowing is not", func(t *testing.T) {
		got := entryOf(put(repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@acme/*", Scope: "global"}, {Pattern: "left-*"}}}))
		if len(got.Added) != 1 || got.Added[0].Pattern != "@acme/*" || got.Added[0].Scope != "global" {
			t.Fatalf("entry = %+v", got)
		}
	})
	t.Run("R-REPO-061 a removal carries an empty added list", func(t *testing.T) {
		evs := put(repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@acme/*", Scope: "global"}}})
		got := entryOf(evs)
		if got.Added == nil || len(got.Added) != 0 || got.Source != "manual" {
			t.Fatalf("entry = %+v (%s)", got, evs[0].Journal)
		}
	})
	t.Run("R-REPO-061 the auto claim of a first publish", func(t *testing.T) {
		n := a.rec.count()
		res, err := a.autoClaim("npm-local", "fresh-lib")
		if err != nil || !res.Created {
			t.Fatalf("auto claim = %+v, %v", res, err)
		}
		got := entryOf(a.rec.since(n))
		want := []struct{ Pattern, Scope string }{{"fresh-lib", "members"}}
		if got.Source != "auto" || !reflect.DeepEqual(got.Added, want) {
			t.Fatalf("entry = %+v", got)
		}
	})
}
