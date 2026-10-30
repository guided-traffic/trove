package virtual

import (
	"context"
	"slices"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/repository"
)

// The decisions of the resolution explanation (R-REPO-019). The first four are the reasons of
// the route (repository.Drop*), the others come from the caller's rights and the state of the
// member.
const (
	DecisionDisabled         = repository.DropDisabled
	DecisionNotAdmitted      = repository.DropNotAdmitted
	DecisionPinnedOut        = repository.DropPinnedOut
	DecisionClaimedElsewhere = repository.DropClaimedElsewhere
	DecisionRefusedClaimed   = "refused-claimed"
	DecisionNoRead           = "no-read"
	DecisionNoCacheFill      = "no-cache-fill"
	DecisionHit              = "hit"
	DecisionMiss             = "miss"
	DecisionNegative         = "negative"
	DecisionOffline          = "offline"
	DecisionCircuitOpen      = "circuit-open"
)

// Explanation is the answer of GET /api/v1/repositories/{key}/resolution?path= (R-REPO-019): per
// member the decision and its reason, for the calling principal, without transferring content and
// without contacting an upstream.
type Explanation struct {
	Repository string           `json:"repository"`
	Descriptor ExplainedPath    `json:"descriptor"`
	DecidedBy  *Decided         `json:"decidedBy,omitempty"`
	Members    []MemberDecision `json:"members"`
	// UnreadableMembers counts the members the caller cannot read; their keys appear only to a
	// holder of repositories:read-config, who gets them as decisions `no-read` and a count of 0.
	UnreadableMembers int `json:"unreadableMembers"`
}

// ExplainedPath is the descriptor of the explained path.
type ExplainedPath struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Package string `json:"package,omitempty"`
}

// Decided says what narrowed the members: the pattern of a claim or a pin rule and, for a claim,
// the claimants the caller can read.
type Decided struct {
	Claim     string   `json:"claim,omitempty"`
	Pin       string   `json:"pin,omitempty"`
	ClaimedBy []string `json:"claimedBy,omitempty"`
}

// MemberDecision is one member's line.
type MemberDecision struct {
	Key      string `json:"key"`
	Class    string `json:"class"`
	Decision string `json:"decision"`
}

// Explain evaluates R-REPO-019 steps 1 to 6 for the caller and reports per member of the virtual
// repository, in the order of its member list, what would happen to it. A member the caller cannot
// read is not named (Security notes of repositories-and-proxy.md): it counts in UnreadableMembers,
// and its key, like the keys of the repositories that hold a claim, only reaches a holder of
// repositories:read-config. The permission is the gate of step 1, which the operation declares.
func (r *Resolver) Explain(ctx context.Context, c Caller, v repository.Repository, d Descriptor) (Explanation, error) {
	p, err := r.plan(ctx, c, v, d, true)
	if err != nil {
		return Explanation{}, err
	}
	readConfig := r.o.Decider.HoldsOn(c.Index, c.Subject, string(authz.RepositoriesRead))
	ex := Explanation{
		Repository: v.Key,
		Descriptor: ExplainedPath{Path: d.Path, Kind: string(d.Kind), Package: d.Package},
		Members:    []MemberDecision{},
	}
	if !readConfig {
		ex.UnreadableMembers = p.Unreadable
	}
	ex.DecidedBy = r.decidedBy(ctx, c, p, readConfig)
	for _, m := range v.Members() {
		line := MemberDecision{Key: m.Key, Class: string(m.Class)}
		if !p.usable[m.Key].read {
			if !readConfig {
				continue
			}
			line.Decision = DecisionNoRead
		} else if line.Decision, err = r.decide(ctx, c, p, m); err != nil {
			return Explanation{}, err
		}
		ex.Members = append(ex.Members, line)
	}
	return ex, nil
}

// decidedBy reports the claim or the pin rule that narrowed the members, with the claimants the
// caller may know.
func (r *Resolver) decidedBy(ctx context.Context, c Caller, p *Plan, readConfig bool) *Decided {
	rd := p.Route.Decision
	switch rd.Kind {
	case repository.DecidedByPin:
		return &Decided{Pin: rd.Pattern}
	case repository.DecidedByClaim:
		claimedBy := rd.ClaimedBy
		if !readConfig {
			claimedBy, _ = r.o.Decider.ReadableOn(ctx, c.Index, c.Subject, claimedBy)
		}
		return &Decided{Claim: rd.Pattern, ClaimedBy: slices.Clone(claimedBy)}
	}
	return nil
}

// decide is the line of a member the caller can read.
func (r *Resolver) decide(ctx context.Context, c Caller, p *Plan, m repository.Repository) (string, error) {
	switch {
	case p.Virtual.Disabled:
		return DecisionDisabled, nil
	case !p.Route.Admitted:
		return DecisionNotAdmitted, nil
	}
	if why, dropped := p.Route.Dropped[m.Key]; dropped {
		return why, nil
	}
	d := p.Descriptor
	if m.Class == repository.Local {
		held, err := r.o.Locals.Holds(ctx, m, d)
		if err != nil {
			return "", err
		}
		return hitOrMiss(held), nil
	}
	if r.o.Remotes == nil {
		return DecisionOffline, nil
	}
	if name := d.Name(); name != "" {
		refused, err := r.o.Router.RemoteRefused(ctx, m, name, &p.Virtual)
		if err != nil {
			return "", err
		}
		if refused {
			return DecisionRefusedClaimed, nil
		}
	}
	mem := Member{Repo: m, ID: repoID(m), MayFill: p.usable[m.Key].fill}
	rc := r.call(c, p.Virtual, mem, d)
	cached, err := r.o.Remotes.Cached(ctx, rc)
	if err != nil {
		return "", err
	}
	if cached {
		return DecisionHit, nil
	}
	if !mem.MayFill {
		return DecisionNoCacheFill, nil
	}
	st, err := r.o.Remotes.EntryState(ctx, mem.ID, d.Path)
	if err != nil {
		return "", err
	}
	switch {
	case st.Negative:
		return DecisionNegative, nil
	case st.Offline:
		return DecisionOffline, nil
	case st.CircuitOpenFor > 0:
		return DecisionCircuitOpen, nil
	}
	return DecisionMiss, nil
}

func hitOrMiss(held bool) string {
	if held {
		return DecisionHit
	}
	return DecisionMiss
}
