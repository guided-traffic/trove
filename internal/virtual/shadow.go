package virtual

import (
	"context"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// DefaultShadowRetryAfter is the Retry-After of shadow_check_unavailable when no circuit says how
// long it stays open (R-REPO-048).
const DefaultShadowRetryAfter = 30 * time.Second

// ShadowOutcome is the result of the shadowing check of R-REPO-048.
type ShadowOutcome struct {
	// Proceed says that the write may go on to its commit.
	Proceed bool
	// Code is shadows_remote_package or shadow_check_unavailable when Proceed is false.
	Code       string
	RetryAfter time.Duration
	// Undetermined is set when the write proceeds although a remote member could not be asked:
	// the publish's audit event records `shadowCheck: undetermined`.
	Undetermined bool
	// Shadowed are the pairs (V, R) in which R serves the name, for the security audit event
	// repository.remote_package_shadowed that the commit transaction records.
	Shadowed []repository.Exposure
	// Virtuals are the keys of the virtual repositories the write would shadow in and the caller
	// holds read on; the error body names them and never a remote member.
	Virtuals []string
	// Outcome is the label of trove_shadow_checks_total; empty when the check did not run.
	Outcome string
}

// Err is the refusal of an outcome that does not proceed: 403 shadows_remote_package naming the
// package and the virtual repositories the caller can read, or 503 shadow_check_unavailable with
// Retry-After. It is nil for an outcome that proceeds.
func (o ShadowOutcome) Err(pkg string) error {
	if o.Proceed {
		return nil
	}
	if o.Code == errs.CodeShadowCheckUnavailable {
		return errs.E(errs.Unavailable, errs.CodeShadowCheckUnavailable,
			"Whether a remote repository already serves the package "+pkg+" could not be determined; retry later.",
			errs.WithRetryAfter(o.RetryAfter))
	}
	return errs.E(errs.Forbidden, errs.CodeShadowsRemotePackage,
		"Publishing the package "+pkg+" would replace a package that a remote repository serves for the clients of the virtual repositories that contain this repository; a holder of manage decides that, or claims the name first.",
		errs.WithExtension("package", pkg), errs.WithExtension("virtualRepositories", o.Virtuals))
}

// shadowResult is what one remote repository of the exposure set answered.
type shadowResult struct {
	outcome ProbeOutcome
	retry   time.Duration
	err     error
}

// ShadowCheck is the shadowing check of R-REPO-048 for a write that would give the local repository
// a version of the package name. docPath is the canonical path of the package's document upstream
// (Format.PackageDocumentPath). It runs after the write was authorized and before the commit, and
// it holds no transaction: every read it makes is its own, and the probes run concurrently.
//
// The check does nothing when the local repository already holds a version or a claim of its own
// covers the name. Otherwise it asks each remote repository of the exposure set once, however many
// virtual repositories contain it: stored state first (a stored document that is not gone serves the
// name), then the engine's probe. A negative entry is never read, and nothing is stored: a probe
// that stored the document would advertise versions on behalf of a publisher who need not hold the
// cache-fill action.
func (r *Resolver) ShadowCheck(ctx context.Context, c Caller, local repository.Repository, name, docPath string) (ShadowOutcome, error) {
	proceed := ShadowOutcome{Proceed: true}
	if local.Class != repository.Local {
		return proceed, nil
	}
	if held, err := r.o.Locals.HoldsVersion(ctx, local, name); err != nil || held {
		return proceed, err
	}
	covered, err := r.o.Router.CoveredByOwnClaim(ctx, local, name)
	if err != nil || covered {
		return proceed, err
	}
	set, err := r.o.Router.ExposureSet(ctx, local, name)
	if err != nil {
		return ShadowOutcome{}, err
	}
	if len(set) == 0 {
		r.m.checked(local.Key, OutcomeNoExposure)
		proceed.Outcome = OutcomeNoExposure
		return proceed, nil
	}

	var remotes []Exposure
	seen := map[string]bool{}
	for _, x := range set {
		if !seen[x.Remote.Key] {
			seen[x.Remote.Key] = true
			remotes = append(remotes, x)
		}
	}
	results := make(map[string]*shadowResult, len(remotes))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, x := range remotes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := r.serves(ctx, c, x, name, docPath)
			mu.Lock()
			results[x.Remote.Key] = &res
			mu.Unlock()
		}()
	}
	wg.Wait()

	var served, undetermined bool
	var retry time.Duration
	for _, x := range remotes {
		res := results[x.Remote.Key]
		if res.err != nil {
			return ShadowOutcome{}, res.err
		}
		switch res.outcome {
		case Served:
			served = true
		case Undetermined:
			undetermined = true
			retry = max(retry, res.retry)
		}
	}
	if !served && !undetermined {
		r.m.checked(local.Key, OutcomeClear)
		proceed.Outcome = OutcomeClear
		return proceed, nil
	}

	manage := r.holdsManage(c, local, name)
	out := ShadowOutcome{}
	switch {
	case served:
		for _, x := range set {
			if results[x.Remote.Key].outcome == Served {
				out.Shadowed = append(out.Shadowed, x)
			}
		}
		out.Virtuals = r.readableVirtuals(ctx, c, out.Shadowed)
		if manage {
			out.Proceed, out.Outcome = true, OutcomeShadowingApproved
		} else {
			out.Code, out.Outcome = errs.CodeShadowsRemotePackage, OutcomeRefused
		}
	default:
		out.Undetermined = true
		if manage {
			out.Proceed, out.Outcome = true, OutcomeUndeterminedAllowed
		} else {
			out.Code, out.Outcome = errs.CodeShadowCheckUnavailable, OutcomeUnavailable
			out.RetryAfter = retry
			if out.RetryAfter <= 0 {
				out.RetryAfter = DefaultShadowRetryAfter
			}
		}
	}
	r.m.checked(local.Key, out.Outcome)
	return out, nil
}

// Exposure is a pair (V, R) of the exposure set.
type Exposure = repository.Exposure

// serves decides whether one remote repository serves the name (R-REPO-048 "Whether R serves n").
func (r *Resolver) serves(ctx context.Context, c Caller, x Exposure, name, docPath string) shadowResult {
	if r.o.Remotes == nil {
		// Without the remote engine nothing can be asked, and an unreachable upstream fails closed.
		return shadowResult{outcome: Undetermined}
	}
	st, err := r.o.Remotes.EntryState(ctx, repoID(x.Remote), docPath)
	if err != nil {
		return shadowResult{err: err}
	}
	switch {
	case st.Stored && st.GoneAt.IsZero():
		return shadowResult{outcome: Served}
	case st.Stored:
		return shadowResult{outcome: NotServed}
	}
	pr, err := r.o.Remotes.Probe(ctx, RemoteCall{
		Remote: x.Remote, Via: x.Virtual, Path: docPath, Package: name, Subject: c.Subject, Endpoint: c.Subject.Endpoint,
	})
	if err != nil {
		if ctx.Err() != nil {
			return shadowResult{err: ctx.Err()}
		}
		// Every failure the engine did not classify is undetermined (step 5).
		return shadowResult{outcome: Undetermined}
	}
	return shadowResult{outcome: pr.Outcome, retry: pr.RetryAfter}
}

// holdsManage reports whether the caller holds manage on the name in the local repository, or
// content:bypass, which the decision point folds into the same question.
func (r *Resolver) holdsManage(c Caller, local repository.Repository, name string) bool {
	ok, err := r.o.Decider.CheckOn(c.Index, c.Subject, []authz.CheckItem{{Repository: local.Key, Package: name, Action: authz.Manage}})
	return err == nil && len(ok) == 1 && ok[0]
}

// readableVirtuals are the keys of the virtual repositories of the pairs that the caller holds read
// on (R-REPO-048 "The error body names the package and the virtual repositories").
func (r *Resolver) readableVirtuals(ctx context.Context, c Caller, pairs []Exposure) []string {
	var keys []string
	seen := map[string]bool{}
	for _, x := range pairs {
		if !seen[x.Virtual.Key] {
			seen[x.Virtual.Key] = true
			keys = append(keys, x.Virtual.Key)
		}
	}
	readable, err := r.o.Decider.ReadableOn(ctx, c.Index, c.Subject, keys)
	if err != nil {
		return []string{}
	}
	if readable == nil {
		readable = []string{}
	}
	return readable
}
