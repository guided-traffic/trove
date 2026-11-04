package v1

import (
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
)

// TestQ_API_194_RoleMembersOfTheCut: the enums of the released contract stay closed at web, worker and all, so an api or
// packages replica is listed with the class web and its exact role in roleName, and the pool is carried (R-OPS-003,
// R-OPS-004, Q-OPS-161, Q-API-194).
func TestQ_API_194_RoleMembersOfTheCut(t *testing.T) {
	pool := "ep-customers"
	reps := fakeReplicas{
		replica("trove-api-1", "api", "n1", gen.Ready, false),
		replica("trove-packages-1", "packages", "n2", gen.Ready, false),
		replica("trove-worker-1", "worker", "n3", gen.Ready, false),
		replica("trove-web-1", "web", "n4", gen.Ready, false),
		replica("trove-all-1", "all", "n5", gen.Ready, false),
	}
	reps[1].Pool = &pool
	s := newSetup(t, func(o *Options) {
		o.Replicas = reps
		o.Identity = Identity{Replica: "trove-api-1", Role: "api"}
	}, authed)

	t.Run("R-OPS-003 the list names the class and the exact role of every replica, and the pool", func(t *testing.T) {
		w := s.get("/api/v1/system/replicas")
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var list gen.ReplicaList
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		got := map[string][2]string{}
		for _, r := range list.Items {
			if len(r.Roles) != 1 {
				t.Fatalf("%s roles = %v", r.Name, r.Roles)
			}
			got[r.Name] = [2]string{string(r.Roles[0]), r.RoleName}
		}
		for name, want := range map[string][2]string{
			"trove-api-1": {"web", "api"}, "trove-packages-1": {"web", "packages"}, "trove-worker-1": {"worker", "worker"},
			"trove-web-1": {"web", "web"}, "trove-all-1": {"all", "all"},
		} {
			if got[name] != want {
				t.Errorf("%s = %v, want %v", name, got[name], want)
			}
		}
		for _, r := range list.Items {
			if r.Name == "trove-packages-1" && (r.Pool == nil || *r.Pool != pool) {
				t.Errorf("pool = %v", r.Pool)
			}
		}
	})

	t.Run("R-OPS-004 the summary has a member per exact role, and answeredBy names the role of the replica that answered", func(t *testing.T) {
		var info gen.SystemInfo
		if err := json.Unmarshal(s.get("/api/v1/system/info").Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
		for _, role := range []string{"api", "packages", "worker", "web", "all"} {
			if info.Replicas.Roles[role].Live != 1 {
				t.Errorf("replicas.roles[%s] = %+v", role, info.Replicas.Roles[role])
			}
		}
		if info.AnsweredBy.Role != "web" || info.AnsweredBy.RoleName != "api" {
			t.Errorf("answeredBy = %+v", info.AnsweredBy)
		}
	})
}
