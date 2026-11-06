package npm

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/npm/npmtest"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// failingStore is a data port that must not be asked: the serve check of an item whose lookup read the rows decides on them.
type failingStore struct{ *npmtest.Store }

var errAsked = errors.New("the data port was asked")

func (failingStore) Tombstoned(context.Context, int64, string, string) (bool, error) {
	return false, errAsked
}

func (failingStore) TarballFacts(context.Context, int64, string, string) (rows.TarballFacts, error) {
	return rows.TarballFacts{}, errAsked
}

// R-ARCH-027 (Q-ARCH-291): the key of the rows the serve check wants is the tombstone of a local repository, the facts of a
// remote one, and nothing for a path that is not a file of a version or a class the check does not apply to.
func TestR_ARCH_027_TheServeFactsKeyNamesTheRowsOfTheCheck(t *testing.T) {
	f := New()
	local := formats.RepoRef{ID: 3, Name: qr("l"), Class: formats.Local, Format: ID}
	remote := formats.RepoRef{ID: 4, Name: qr("r"), Class: formats.Remote, Format: ID}
	virtual := formats.RepoRef{ID: 5, Name: qr("v"), Class: formats.Virtual, Format: ID}
	for _, c := range []struct {
		name string
		repo formats.RepoRef
		path formats.Path
		want formats.ServeKey
	}{
		{"local tarball", local, formats.Path{Path: "lodash/-/lodash-4.17.21.tgz"}, formats.ServeKey{Kind: formats.ServeNPMLocal, Package: "lodash", Version: "4.17.21"}},
		{"remote tarball", remote, formats.Path{Path: "lodash/-/lodash-4.17.21.tgz"}, formats.ServeKey{Kind: formats.ServeNPMRemote, Package: "lodash", Version: "4.17.21"}},
		{"scoped tarball", local, formats.Path{Path: "@acme/ui/-/ui-1.2.0.tgz"}, formats.ServeKey{Kind: formats.ServeNPMLocal, Package: "@acme/ui", Version: "1.2.0"}},
		{"package document", local, formats.Path{Path: "lodash"}, formats.ServeKey{}},
		{"virtual repository", virtual, formats.Path{Path: "lodash/-/lodash-4.17.21.tgz"}, formats.ServeKey{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := f.ServeFactsKey(c.repo, c.path); got != c.want {
				t.Errorf("key = %+v, want %+v", got, c.want)
			}
		})
	}
	if got := formats.CapabilitiesOf(f).ServeFacts; got == nil {
		t.Error("the format does not offer the key as a capability")
	}
}

// R-ARCH-027: a tombstone read with the item refuses the file without a query of the check's own; facts read for another key
// are not used, the data port is asked.
func TestR_ARCH_027_ALocalServeCheckDecidesOnTheFactsOfTheLookup(t *testing.T) {
	f := New()
	h := formattest.NewHost(f, formattest.HostOptions{Data: failingStore{npmtest.New()}})
	ref := h.Ref()
	path := "lodash/-/lodash-4.17.21.tgz"
	key := f.ServeFactsKey(ref, formats.Path{Path: path})
	check := func(facts formats.ServeFacts) error {
		return f.ServeCheck(context.Background(), h, ref, formats.ItemRef{Repo: ref, Path: path, Facts: facts}, formats.ServeStored)
	}
	if err := check(formats.ServeFacts{Key: key}); err != nil {
		t.Errorf("facts without a tombstone refuse: %v", err)
	}
	err := check(formats.ServeFacts{Key: key, Tombstoned: true})
	if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound {
		t.Errorf("a tombstone read with the item = %v, want 404", err)
	}
	other := formats.ServeKey{Kind: formats.ServeNPMLocal, Package: "lodash", Version: "4.17.20"}
	if err := check(formats.ServeFacts{Key: other, Tombstoned: true}); !errors.Is(err, errAsked) {
		t.Errorf("facts of another version were used: %v", err)
	}
	if err := check(formats.ServeFacts{}); !errors.Is(err, errAsked) {
		t.Errorf("no facts: the data port is to be asked, got %v", err)
	}
}

// R-ARCH-027, R-NPM-026: the serve check of a remote repository decides on the facts of the lookup as it does on the data port's:
// a version the document stopped listing and a package the upstream withdrew are refused unless serveRemovedVersions is set.
func TestR_ARCH_027_ARemoteServeCheckDecidesOnTheFactsOfTheLookup(t *testing.T) {
	f := New()
	h := formattest.NewHost(f, formattest.HostOptions{Class: formats.Remote, Data: failingStore{npmtest.New()}})
	ref := h.Ref()
	path := "evil/-/evil-1.0.0.tgz"
	key := f.ServeFactsKey(ref, formats.Path{Path: path})
	if key.Kind != formats.ServeNPMRemote {
		t.Fatalf("key = %+v", key)
	}
	check := func(n formats.ServeNPMRemoteFacts) error {
		return f.ServeCheck(context.Background(), h, ref,
			formats.ItemRef{Repo: ref, Path: path, Facts: formats.ServeFacts{Key: key, NPMRemote: n}}, formats.ServeStored)
	}
	for _, c := range []struct {
		name    string
		facts   formats.ServeNPMRemoteFacts
		refused bool
	}{
		{"listed", formats.ServeNPMRemoteFacts{Known: true, Listed: true}, false},
		{"unknown to the repository", formats.ServeNPMRemoteFacts{}, false},
		{"removed from the document", formats.ServeNPMRemoteFacts{Known: true, Listed: false}, true},
		{"removed, served on purpose", formats.ServeNPMRemoteFacts{Known: true, Listed: false, ServeRemoved: true}, false},
		{"package withdrawn", formats.ServeNPMRemoteFacts{Known: true, Listed: true, DocGoneUs: 1700000000000000}, true},
		{"package withdrawn, served on purpose", formats.ServeNPMRemoteFacts{Known: true, Listed: true, DocGoneUs: 1700000000000000, ServeRemoved: true}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := check(c.facts)
			if e, ok := errs.As(err); c.refused != (ok && e.Kind == errs.NotFound) || (!c.refused && err != nil) {
				t.Errorf("err = %v, want refused %v", err, c.refused)
			}
		})
	}
}

// R-ARCH-027: the facts of the gate are the ones a revalidation read again; facts read for the lookup decide when the document
// is current, and the data port is asked when they were read for another tarball.
func TestR_ARCH_027_TheGateDecidesOnTheFactsOfTheLookupAndReturnsThem(t *testing.T) {
	eng := &scriptedRemote{ensure: func(string) (*formats.RemoteDocument, formats.Currency, error) { return nil, formats.Revalidated, nil }}
	h, twin, f := remoteHost(t, eng)
	_ = twin
	ref := h.Ref()
	path := formats.Path{Path: "evil/-/evil-1.0.0.tgz"}
	key := f.ServeFactsKey(ref, path)
	rq := requestFor(t, h, path.Path)
	gate := f.Remote().(formats.FactsGate)
	have := formats.ServeFacts{Key: key, NPMRemote: formats.ServeNPMRemoteFacts{
		Known: true, Listed: true, Integrity: sriOf("evil"), DocStored: true, DocCurrent: true,
	}}
	d, got, err := gate.BeforeArtifactWith(context.Background(), h, rq, path, have)
	if err != nil {
		t.Fatal(err)
	}
	if len(eng.ensured) != 0 {
		t.Errorf("a current document was revalidated: %v", eng.ensured)
	}
	if string(d.SHA512) != string(mustDigest(sriOf("evil"))) {
		t.Errorf("digests = %+v", d)
	}
	if got.Key != key || !got.NPMRemote.Known || got.NPMRemote.Integrity != have.NPMRemote.Integrity {
		t.Errorf("facts returned = %+v", got)
	}
	if got.Refreshed {
		t.Error("the facts of a current document are marked refreshed")
	}
	// A stale document is revalidated, and the facts say that they were read after the upstream was asked.
	stale := have
	stale.NPMRemote.DocCurrent = false
	_, got, _ = gate.BeforeArtifactWith(context.Background(), h, rq, path, stale)
	if len(eng.ensured) == 0 || !got.Refreshed {
		t.Errorf("a stale document: revalidations %v, facts refreshed %v", eng.ensured, got.Refreshed)
	}
	// A version the document withdrew is refused, and the refusal carries the facts it was decided on.
	have.NPMRemote.Listed = false
	if _, _, err := gate.BeforeArtifactWith(context.Background(), h, rq, path, have); errs.KindOf(err) != errs.NotFound {
		t.Errorf("a removed version = %v, want 404", err)
	}
}
