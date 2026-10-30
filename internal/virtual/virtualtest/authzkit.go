package virtualtest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// RepoSpec describes a repository of a decision point: its key, class, the members and deploy
// target of a virtual one, and the cacheFillAction of a remote one ("" is read).
type RepoSpec struct {
	Key, Class   string
	Members      []string
	DeployTarget string
	Fill         string
}

// Grant gives a principal actions on repositories, by key, or binds a system role to it.
type Grant struct {
	Repos     []string
	Principal string
	Actions   []string
	Role      string
}

// The principals of the fixtures.
const (
	Alice = "user-alice"
	Bob   = "user-bob"
	Carol = "user-carol"
)

// NewDecider builds the real decision point (authz.Engine) over a snapshot made from the specs and
// grants, with anonymous access on. Repository ids are 1.. in the order of the specs, which is how
// the test repositories are numbered too.
func NewDecider(tb testing.TB, repos []RepoSpec, grants []Grant) *authz.Engine {
	tb.Helper()
	snap := authz.Snapshot{Label: authz.Label{AuthzEpoch: 1, AuthzAt: time.Unix(1000, 0), RepoEpoch: 1, RepoAt: time.Unix(1000, 0)}}
	ids := map[string]int64{}
	for i, r := range repos {
		id := int64(i + 1)
		ids[r.Key] = id
		cfg := "{}"
		if r.Class == "remote" {
			fill := r.Fill
			if fill == "" {
				fill = "read"
			}
			cfg = fmt.Sprintf(`{"remote":{"cacheFillAction":%q}}`, fill)
		}
		snap.Repositories = append(snap.Repositories, q.AuthzBuildRepositoriesRow{ID: id, Key: r.Key, Class: r.Class, Format: Format, Config: []byte(cfg)})
	}
	for _, r := range repos {
		for _, m := range r.Members {
			snap.VirtualMembers = append(snap.VirtualMembers, q.AuthzBuildVirtualMembersRow{VirtualID: ids[r.Key], MemberKey: m})
		}
		if r.DeployTarget != "" {
			snap.DeployTargets = append(snap.DeployTargets, q.AuthzBuildDeployTargetsRow{VirtualID: ids[r.Key], TargetKey: r.DeployTarget})
		}
	}
	for i, g := range grants {
		if g.Role != "" {
			snap.Bindings = append(snap.Bindings, q.AuthzBuildBindingsRow{Role: g.Role, PrincipalID: g.Principal})
			continue
		}
		pid := fmt.Sprintf("policy-%d", i)
		snap.Policies = append(snap.Policies, q.AuthzBuildPoliciesRow{ID: pid, Name: pid, ResourceType: "repository"})
		for j, k := range g.Repos {
			snap.Selectors = append(snap.Selectors, q.AuthzBuildSelectorsRow{PolicyID: pid, Ordinal: int16(j), Kind: "key", RepositoryID: ids[k]})
		}
		kind, name := "user", strings.TrimPrefix(g.Principal, "user-")
		if g.Principal == authz.AnonymousID {
			kind, name = "anonymous", "anonymous"
		}
		snap.Entries = append(snap.Entries, q.AuthzBuildEntriesRow{PolicyID: pid, PrincipalID: g.Principal, PrincipalKind: kind, PrincipalName: name, Actions: g.Actions})
	}
	e, err := authz.New(authz.Options{
		Source: &source{snap: snap}, Changes: &changes{}, RetryBudget: time.Second, Formats: Catalog(),
		AnonymousAccess: func(context.Context) (bool, error) { return true, nil },
		Config: config.Authz{
			HideExistence: true,
			Cache:         config.AuthzCache{MaxStaleness: 10 * time.Second, SafetyTTL: 10 * time.Minute},
			Audit:         config.AuthzAudit{Reads: config.AuthzReadsDenied},
		},
	})
	if err != nil {
		tb.Fatal(err)
	}
	if err := e.Refresh(context.Background()); err != nil {
		tb.Fatal(err)
	}
	return e
}

type source struct{ snap authz.Snapshot }

func (s *source) Snapshot(context.Context) (*authz.Snapshot, error) {
	cp := s.snap
	cp.Started = time.Now()
	return &cp, nil
}

func (s *source) Epochs(context.Context) (authz.Label, error) { return s.snap.Label, nil }

type changes struct{}

func (*changes) AddListener(cluster.Listener)           {}
func (*changes) AreaState(string) (int64, time.Time)    { return 0, time.Time{} }
func (*changes) FreshWithin(time.Duration) bool         { return false }
func (*changes) Notify(meta.Tx, string, int, any) error { return nil }
