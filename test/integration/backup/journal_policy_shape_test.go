//go:build integration

package backup

import (
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/journal"
)

// producerState is a policy's state in the shape the producer journals it (stateOf in internal/authz): a selector of kind key names
// the repository by ID and also carries its key and class for display, which the stored row does not hold.
func producerState(repoID, repoKey, repoClass string, include []string, entries map[string][]string) string {
	type ent struct {
		PrincipalID string   `json:"principalId"`
		Actions     []string `json:"actions"`
	}
	var es []ent
	for p, a := range entries {
		es = append(es, ent{p, a})
	}
	if include == nil {
		include = []string{}
	}
	b, _ := json.Marshal(map[string]any{
		"resourceType": "repository",
		"selectors":    []map[string]any{{"kind": "key", "repositoryId": atoi(repoID), "key": repoKey, "class": repoClass}},
		"packages":     map[string]any{"include": include, "exclude": []string{}},
		"paths":        map[string]any{"include": []string{}, "exclude": []string{}},
		"entries":      es,
	})
	return string(b)
}

// TestR_AUTHZ_031_APolicyUpdateAlreadyInEffectIsNotSuspended: the replay window starts at the newest audit event, before the restore
// point, so it replays updates the restored database already holds. An update whose after-state is the stored state, in the shape the
// producer writes it, is already in effect and leaves the policy's entries alone, whatever the before-state was; a narrowing is applied and
// an after-state that names another repository is not one and suspends the policy (fail closed).
func TestR_AUTHZ_031_APolicyUpdateAlreadyInEffectIsNotSuspended(t *testing.T) {
	e := newJEnv(t)
	policy, repo, team, lead := e.teamWeb()
	full := []string{"annotate", "deploy", "read"}
	cur := producerState(repo, "npm-internal", "local", []string{"@web/*"}, map[string][]string{team: full, lead: full})

	t.Run("the stored state as the producer journals it is already in effect and the entries stay", func(t *testing.T) {
		_, out := e.replayEvents(policyUpdated(policy, cur, cur))
		e.want(out, journal.AlreadyInEffect)
		if got := e.policyActions(policy, team); got != "annotate,deploy,read" {
			t.Errorf("team-web holds %q after the replay, want its three actions", got)
		}
		if n := e.count("SELECT count(*)::text FROM authz_policy_entry WHERE policy_id = $1::uuid", policy); n != "2" {
			t.Errorf("the policy keeps %s entries, want 2", n)
		}
	})

	t.Run("a before-state wider than the after-state does not matter when the after-state is the stored one", func(t *testing.T) {
		wider := producerState(repo, "npm-internal", "local", nil, map[string][]string{team: full, lead: full})
		_, out := e.replayEvents(policyUpdated(policy, wider, cur))
		e.want(out, journal.AlreadyInEffect)
		if n := e.count("SELECT count(*)::text FROM authz_policy_entry WHERE policy_id = $1::uuid", policy); n != "2" {
			t.Errorf("the policy keeps %s entries, want 2", n)
		}
	})

	t.Run("a narrowing in the producer's shape is applied", func(t *testing.T) {
		after := producerState(repo, "npm-internal", "local", []string{"@web/*"}, map[string][]string{team: {"read"}, lead: full})
		_, out := e.replayEvents(policyUpdated(policy, cur, after))
		e.want(out, journal.Applied)
		if got := e.policyActions(policy, team); got != "read" {
			t.Errorf("team-web holds %q, want read", got)
		}
	})

	t.Run("another repository is not a narrowing and suspends the policy", func(t *testing.T) {
		after := producerState("999999", "other", "local", []string{"@web/*"}, map[string][]string{team: {"read"}, lead: full})
		_, out := e.replayEvents(policyUpdated(policy, cur, after))
		e.want(out, journal.Suspended)
	})
}
