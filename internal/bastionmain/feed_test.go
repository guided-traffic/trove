package bastionmain

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// TestR_BAS_015_TheMappingTableOfTheFeed: every row of the table of R-BAS-015, from the stored event to the
// entries it yields.
func TestR_BAS_015_TheMappingTableOfTheFeed(t *testing.T) {
	const (
		tok1, tok2 = "0192f1c4-0000-7000-8000-0000000000a1", "0192f1c4-0000-7000-8000-0000000000a2"
		u1, sa1    = "0192f1c4-0000-7000-8000-0000000000c1", "0192f1c4-0000-7000-8000-0000000000c2"
	)
	cases := []struct {
		name string
		ev   FeedEvent
		want []rawEntry
	}{
		{"token.revoked names its token", FeedEvent{Action: audit.EventTokenRevoked, ResourceID: tok1}, []rawEntry{{kind: iface.FeedToken, tokens: []string{tok1}}}},
		{
			"token.rotated names the predecessor",
			FeedEvent{Action: audit.EventTokenRotated, ResourceID: tok2, Details: []byte(`{"predecessor":"` + tok1 + `"}`)},
			[]rawEntry{{kind: iface.FeedToken, tokens: []string{tok1}}},
		},
		{
			"ci.token.revoked names every token",
			FeedEvent{Action: audit.EventCITokenRevoked, Details: []byte(`{"tokenIds":["` + tok1 + `","` + tok2 + `"]}`)},
			[]rawEntry{{kind: iface.FeedToken, tokens: []string{tok1, tok2}}},
		},
		{"a revoked token whose ID is missing drops every answer", FeedEvent{Action: audit.EventTokenRevoked}, []rawEntry{{kind: iface.FeedGrants}}},
		{"user.disabled", FeedEvent{Action: audit.EventUserDisabled, ResourceID: u1}, []rawEntry{{kind: iface.FeedPrincipal, principals: []string{u1}}}},
		{"user.deleted", FeedEvent{Action: audit.EventUserDeleted, ResourceID: u1}, []rawEntry{{kind: iface.FeedPrincipal, principals: []string{u1}}}},
		{"serviceaccount.disabled", FeedEvent{Action: audit.EventSADisabled, ResourceID: sa1}, []rawEntry{{kind: iface.FeedPrincipal, principals: []string{sa1}}}},
		{"serviceaccount.deleted", FeedEvent{Action: audit.EventSADeleted, ResourceID: sa1}, []rawEntry{{kind: iface.FeedPrincipal, principals: []string{sa1}}}},
		{
			"user.groups.synced that removed a group",
			FeedEvent{Action: audit.EventUserGroupsSynced, ResourceID: u1, Details: []byte(`{"added":[],"removed":["devs"]}`)},
			[]rawEntry{{kind: iface.FeedPrincipal, principals: []string{u1}}},
		},
		{"user.groups.synced that only added groups widens", FeedEvent{Action: audit.EventUserGroupsSynced, ResourceID: u1, Details: []byte(`{"added":["devs"],"removed":[]}`)}, nil},
		{
			"group.members.changed names the removed members",
			FeedEvent{
				Action:  audit.EventGroupMembersChanged,
				Details: []byte(`{"added":[{"member":"carol","source":"api"}],"removed":[{"member":"alice","source":"api"},{"member":"ci-bot","source":"api"},{"member":"alice","source":"scim"}]}`),
			},
			[]rawEntry{{kind: iface.FeedPrincipal, names: []string{"alice", "ci-bot"}}},
		},
		{"group.members.changed that only added members widens", FeedEvent{Action: audit.EventGroupMembersChanged, Details: []byte(`{"added":[{"member":"carol","source":"api"}],"removed":[]}`)}, nil},
		{
			"authz.role.unbound from a user",
			FeedEvent{Action: "authz.role.unbound", Details: []byte(`{"role":"admin","principal":"user:alice","principalId":"` + u1 + `"}`)},
			[]rawEntry{{kind: iface.FeedPrincipal, principals: []string{u1}}},
		},
		{
			"authz.role.unbound from a service account",
			FeedEvent{Action: "authz.role.unbound", Details: []byte(`{"role":"admin","principal":"sa:ci","principalId":"` + sa1 + `"}`)},
			[]rawEntry{{kind: iface.FeedPrincipal, principals: []string{sa1}}},
		},
		{"authz.role.unbound from a group", FeedEvent{Action: "authz.role.unbound", Details: []byte(`{"role":"admin","principal":"group:devs","principalId":"` + u1 + `"}`)}, []rawEntry{{kind: iface.FeedGrants}}},
		{"authz.role.unbound from authenticated", FeedEvent{Action: "authz.role.unbound", Details: []byte(`{"role":"reader","principal":"authenticated","principalId":"x"}`)}, []rawEntry{{kind: iface.FeedGrants}}},
		{"group.deleted", FeedEvent{Action: audit.EventGroupDeleted}, []rawEntry{{kind: iface.FeedGrants}}},
		{"authz.policy.deleted", FeedEvent{Action: "authz.policy.deleted"}, []rawEntry{{kind: iface.FeedGrants}}},
		{"authz.policy.updated", FeedEvent{Action: "authz.policy.updated"}, []rawEntry{{kind: iface.FeedGrants}}},
		{"authz.settings.changed", FeedEvent{Action: "authz.settings.changed"}, []rawEntry{{kind: iface.FeedGrants}}},
		{"ci.issuer.updated", FeedEvent{Action: audit.EventCIIssuerUpdated}, []rawEntry{{kind: iface.FeedGrants}}},
		{"ci.issuer.disabled", FeedEvent{Action: audit.EventCIIssuerDisabled}, []rawEntry{{kind: iface.FeedGrants}}},
		{"ci.issuer.deleted", FeedEvent{Action: audit.EventCIIssuerDeleted}, []rawEntry{{kind: iface.FeedGrants}}},
		{"ci.mapping.updated", FeedEvent{Action: audit.EventCIMappingUpdated}, []rawEntry{{kind: iface.FeedGrants}}},
		{"ci.mapping.disabled", FeedEvent{Action: audit.EventCIMappingDisabled}, []rawEntry{{kind: iface.FeedGrants}}},
		{"ci.mapping.deleted", FeedEvent{Action: audit.EventCIMappingDeleted}, []rawEntry{{kind: iface.FeedGrants}}},
		{"the exposure of the default endpoint", FeedEvent{Action: "endpoint.exposure.updated", Details: []byte(`{"endpoint":"default","name":"x","repository":"r","epoch":1}`)}, []rawEntry{{kind: iface.FeedGrants}}},
		{"the exposure of the default endpoint, deleted", FeedEvent{Action: "endpoint.exposure.deleted", Details: []byte(`{"endpoint":"default"}`)}, []rawEntry{{kind: iface.FeedGrants}}},
		{"the exposure of another endpoint is none of a Bastion's business", FeedEvent{Action: "endpoint.exposure.updated", Details: []byte(`{"endpoint":"customers"}`)}, nil},
		{"settings.auth.changed that leaves anonymous access as it was", FeedEvent{
			Action:  audit.EventSettingsAuthChanged,
			Details: []byte(`{"before":{"anonymousAccess":true},"after":{"anonymousAccess":true,"patMaxTTLSeconds":60}}`),
		}, []rawEntry{{kind: iface.FeedSettings}}},
		{"settings.auth.changed that switches anonymous access on", FeedEvent{
			Action:  audit.EventSettingsAuthChanged,
			Details: []byte(`{"before":{"anonymousAccess":false},"after":{"anonymousAccess":true}}`),
		}, []rawEntry{{kind: iface.FeedSettings}}},
		{"settings.auth.changed that switches anonymous access off", FeedEvent{
			Action:  audit.EventSettingsAuthChanged,
			Details: []byte(`{"before":{"anonymousAccess":true},"after":{"anonymousAccess":false}}`),
		}, []rawEntry{{kind: iface.FeedSettings}, {kind: iface.FeedGrants}}},
		{"npm.unpublish names the repository", FeedEvent{Action: audit.EventNPMUnpublish, Repository: "npm-local"}, []rawEntry{{kind: iface.FeedContent, repository: "npm-local"}}},
		{"package.deleted names the repository", FeedEvent{Action: audit.EventPackageDeleted, Repository: "nuget-local"}, []rawEntry{{kind: iface.FeedContent, repository: "nuget-local"}}},
		{"a withdrawal without a repository yields no entry", FeedEvent{Action: audit.EventPackageDeleted}, nil},
		{"authz.ownership.removed bounds writes", FeedEvent{Action: "authz.ownership.removed"}, nil},
		{"package.published reserves numbers", FeedEvent{Action: audit.EventPackagePublished, Repository: "npm-local"}, nil},
		{"an event of no table row", FeedEvent{Action: "project.updated"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mapEvent(c.ev, "default")
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("entries = %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestR_BAS_015_EveryJournaledEventIsClassified is driven by the audit registry: every event the revocation journal
// copies is either read by the feed (FeedActions) or listed with its reason in the set of events that yield no
// entry (NoFeedEntry), never both, so a new journaled event fails here until someone decides whether it narrows
// what a Bastion may keep. Every action the feed reads is a registered transactional event, and an event of the
// no-entry set yields no entry.
func TestR_BAS_015_EveryJournaledEventIsClassified(t *testing.T) {
	read := map[string]bool{}
	for _, name := range FeedActions() {
		read[name] = true
		typ, ok := audit.Lookup(name)
		if !ok {
			t.Errorf("%s is read by the feed and is not a registered audit event", name)
			continue
		}
		if typ.Mode != audit.Transactional {
			t.Errorf("%s is %s: the feed reads committed state changes", name, typ.Mode)
		}
	}
	none := NoFeedEntry()
	journaled := 0
	for _, typ := range audit.Types() {
		if typ.Journal != audit.JournalRevocation {
			continue
		}
		journaled++
		reason, listed := none[typ.Name]
		switch {
		case read[typ.Name] && listed:
			t.Errorf("%s is read by the feed and listed as yielding no entry", typ.Name)
		case !read[typ.Name] && !listed:
			t.Errorf("%s is journaled as a revocation and not classified: map it in mapEvent and FeedActions, or list it in noFeedEntry with the reason it narrows nothing a Bastion keeps", typ.Name)
		case listed && strings.TrimSpace(reason) == "":
			t.Errorf("%s is listed without a reason", typ.Name)
		}
	}
	if journaled == 0 {
		t.Fatal("the registry holds no journaled event: the test reads the wrong registry")
	}
	for name := range none {
		typ, ok := audit.Lookup(name)
		if !ok || typ.Journal != audit.JournalRevocation {
			t.Errorf("%s is listed as yielding no entry but is no journaled event: drop it from the list", name)
		}
		if got := mapEvent(FeedEvent{Action: name, Repository: "npm-local", ResourceID: "0192f1c4-0000-7000-8000-0000000000a1"}, "default"); got != nil {
			t.Errorf("%s is listed as yielding no entry and yields %+v", name, got)
		}
	}
	if !slices.Contains(FeedActions(), audit.EventNPMUnpublish) {
		t.Error("npm.unpublish is not in the feed")
	}
}

// TestR_BAS_015_UnassigningARepositoryDropsEveryPermissionAnswer: a repository that leaves its project leaves the
// project's policies behind (R-AUTHZ-026), so every caller's read of it may have narrowed. A content entry would
// not do: the Bastion drops the URL answers of a repository on it, not the shape answers (R-BAS-026), so a full
// answer the unassignment ended would be kept (Q-BAS-120).
func TestR_BAS_015_UnassigningARepositoryDropsEveryPermissionAnswer(t *testing.T) {
	ev := FeedEvent{
		Action: audit.EventProjectRepositoryUnassigned, Repository: "acme-npm",
		Details: []byte(`{"epoch":7,"project":"acme","projectId":"p","repository":"acme-npm","repositoryId":"r","moved":false}`),
	}
	if got := mapEvent(ev, "default"); !reflect.DeepEqual(got, []rawEntry{{kind: iface.FeedGrants}}) {
		t.Errorf("entries = %+v, want one grants entry", got)
	}
	if !slices.Contains(FeedActions(), audit.EventProjectRepositoryUnassigned) {
		t.Error("the feed's query does not read authz.project.repository_unassigned")
	}
}

// TestR_BAS_015_TheEntriesOfOneEventHaveIDsOfTheirOwn: the second entry of an event is "<event ID>:1" (Q-BAS-49).
func TestR_BAS_015_TheEntriesOfOneEventHaveIDsOfTheirOwn(t *testing.T) {
	ev := FeedEvent{
		ID: "0192f2a0-0000-7000-8000-000000000001", Action: audit.EventSettingsAuthChanged,
		Details: []byte(`{"before":{"anonymousAccess":true},"after":{"anonymousAccess":false}}`),
	}
	got := lookups{}.entries(ev, mapEvent(ev, "default"))
	if len(got) != 2 || got[0].ID != ev.ID || got[0].Kind != iface.FeedSettings || got[1].ID != ev.ID+":1" || got[1].Kind != iface.FeedGrants {
		t.Errorf("entries = %+v", got)
	}
	// Members that no longer resolve to an ID drop every answer instead of naming nobody.
	gone := FeedEvent{ID: "0192f2a0-0000-7000-8000-000000000002", Action: audit.EventGroupMembersChanged, Details: []byte(`{"removed":[{"member":"ghost"}]}`)}
	if es := (lookups{ids: map[string][]string{}}).entries(gone, mapEvent(gone, "default")); len(es) != 1 || es[0].Kind != iface.FeedGrants {
		t.Errorf("a removed member without an ID: %+v", es)
	}
}
