package formathost

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
)

// viewOnly implements the reads of the remote engine and nothing else: a Metadata or a Probe would not compile against it, which
// is the property of the role api (Q-ARCH-294).
type viewOnly struct{ entryStates, peeks int }

func (v *viewOnly) Cached(context.Context, proxy.Request) (items.Item, bool, error) {
	return items.Item{}, true, nil
}

func (v *viewOnly) EntryState(context.Context, int64, string) (proxy.EntryState, error) {
	v.entryStates++
	return proxy.EntryState{Stored: true}, nil
}

func (v *viewOnly) Peek(context.Context, proxy.Remote, string, string) (proxy.Peek, error) {
	v.peeks++
	return proxy.Peek{Offline: true}, nil
}

func (v *viewOnly) CachedArtifacts(context.Context, int64, string) ([]string, error) {
	return []string{"a/-/a-1.tgz"}, nil
}

type noRepos struct{ repository.Reader }

func (noRepos) List(context.Context) ([]repository.Repository, error) { return nil, nil }

func TestR_ARCH_001_VirtualRemotesOverAReadOnlyView(t *testing.T) {
	d := &fakeDecider{}
	bind := func(o VirtualRemotesOptions) (virtual.Remotes, error) {
		o.Repos, o.Decider = noRepos{}, d
		return NewVirtualRemotes(o)
	}

	t.Run("R-ARCH-001 the binding takes the engine or its read-only view, never both and never neither", func(t *testing.T) {
		if _, err := bind(VirtualRemotesOptions{}); err == nil {
			t.Error("neither was accepted")
		}
		if _, err := bind(VirtualRemotesOptions{Engine: &proxy.Engine{}, View: &viewOnly{}}); err == nil {
			t.Error("both were accepted")
		}
		if _, err := bind(VirtualRemotesOptions{View: &viewOnly{}}); err != nil {
			t.Errorf("a view alone: %v", err)
		}
	})

	t.Run("Q-ARCH-294 the view answers the reads and refuses Metadata and Probe with Unavailable, never contacting an upstream", func(t *testing.T) {
		view := &viewOnly{}
		r, err := bind(VirtualRemotesOptions{View: view})
		if err != nil {
			t.Fatal(err)
		}
		st, err := r.EntryState(t.Context(), 7, "a")
		if err != nil || !st.Stored || view.entryStates != 1 {
			t.Errorf("EntryState = %+v, %v (%d reads)", st, err, view.entryStates)
		}
		if paths, err := r.CachedArtifacts(t.Context(), 7, "a/"); err != nil || len(paths) != 1 {
			t.Errorf("CachedArtifacts = %v, %v", paths, err)
		}
		if _, err := r.Metadata(t.Context(), virtual.RemoteCall{}); errs.KindOf(err) != errs.Unavailable {
			t.Errorf("Metadata on a view: %v, want Unavailable", err)
		}
		if _, err := r.Probe(t.Context(), virtual.RemoteCall{}); errs.KindOf(err) != errs.Unavailable {
			t.Errorf("Probe on a view: %v, want Unavailable", err)
		}
	})
}
