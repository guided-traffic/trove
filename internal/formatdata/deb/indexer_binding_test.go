package deb

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/jobs"
)

// asked records which repository ids a builder was asked to build.
type asked struct{ ids []int64 }

func (a *asked) IndexRepo(_ context.Context, id int64) (IndexRepo, bool, error) {
	a.ids = append(a.ids, id)
	return IndexRepo{}, false, nil // not found: the job ends cleanly, nothing is built
}

// R-DEB-010: the index job runs the builder of its own registry. A process that builds two replicas at once (the integration tests) must
// not run both on the builder of the one built last.
func TestR_DEB_010_TheIndexJobRunsTheBuilderOfItsOwnRegistry(t *testing.T) {
	first, second := &asked{}, &asked{}
	xFirst, xSecond := &Indexer{Repos: first}, &Indexer{Repos: second}
	run := &jobs.Run{TargetKey: "7"}

	if err := runIndexJob(context.Background(), run, func() *Indexer { return xFirst }); err != nil {
		t.Fatalf("the job of the first registry = %v, want nil", err)
	}
	if err := runIndexJob(context.Background(), &jobs.Run{TargetKey: "9"}, func() *Indexer { return xSecond }); err != nil {
		t.Fatalf("the job of the second registry = %v, want nil", err)
	}
	if len(first.ids) != 1 || first.ids[0] != 7 {
		t.Errorf("the first builder was asked for %v, want [7]", first.ids)
	}
	if len(second.ids) != 1 || second.ids[0] != 9 {
		t.Errorf("the second builder was asked for %v, want [9]", second.ids)
	}
}

// R-DEB-010: a registry whose replica has no builder (it claims no jobs) fails the job permanently, whatever another registry holds.
func TestR_DEB_010_AnIndexJobOfARegistryWithoutABuilderFailsPermanently(t *testing.T) {
	other := &asked{}
	xOther := &Indexer{Repos: other} // the builder of another replica of the process

	for name, indexer := range map[string]func() *Indexer{
		"no getter":        nil,
		"a getter of none": func() *Indexer { return nil },
	} {
		if err := runIndexJob(context.Background(), &jobs.Run{TargetKey: "7"}, indexer); !jobs.IsPermanent(err) {
			t.Errorf("%s: the job = %v, want a permanent error", name, err)
		}
	}
	if err := runIndexJob(context.Background(), &jobs.Run{TargetKey: "8"}, func() *Indexer { return xOther }); err != nil {
		t.Fatalf("the job of the registry that has a builder = %v, want nil", err)
	}
	if len(other.ids) != 1 || other.ids[0] != 8 {
		t.Errorf("the other replica's builder was asked for %v, want only its own job [8]", other.ids)
	}
}
