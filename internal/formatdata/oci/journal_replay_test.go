package oci

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
)

// TestR_OCI_033_TheReplayHandlerIsRegisteredForTheDeletionEvent: the journal replays oci.manifest.deleted through the handler of this
// package, so the binary holds a handler for every journaled event (R-HA-054).
func TestR_OCI_033_TheReplayHandlerIsRegisteredForTheDeletionEvent(t *testing.T) {
	h, ok := journal.HandlerFor(audit.EventOCIManifestDeleted)
	if !ok || h.Domain != "oci" || h.Apply == nil {
		t.Fatalf("handler %+v, registered %v", h, ok)
	}
}

func TestR_OCI_033_AnEntryIsAClaimTheReplayChecksBeforeItReachesAStatement(t *testing.T) {
	d := strings.Repeat("ab", 32)
	cases := []struct {
		name, entry, why string
	}{
		{"complete", `{"repository":3,"name":"team/app","digest":"sha256:` + d + `","actorId":"x"}`, ""},
		{"no actor is fine", `{"repository":3,"name":"app","digest":"sha256:` + d + `"}`, ""},
		{"not JSON", `nope`, "is not an oci.manifest.deleted entry"},
		{"a list", `[1]`, "is not an oci.manifest.deleted entry"},
		{"no repository", `{"name":"app","digest":"sha256:` + d + `"}`, "no repository"},
		{"a negative repository", `{"repository":-1,"name":"app","digest":"sha256:` + d + `"}`, "no repository"},
		{"no name", `{"repository":3,"digest":"sha256:` + d + `"}`, "no image name"},
		{"a name of 256 bytes", `{"repository":3,"name":"` + strings.Repeat("a", 256) + `","digest":"sha256:` + d + `"}`, "no image name"},
		{"no digest", `{"repository":3,"name":"app"}`, "no digest"},
		{"another algorithm", `{"repository":3,"name":"app","digest":"sha512:` + d + `"}`, "no digest"},
		{"upper case", `{"repository":3,"name":"app","digest":"sha256:` + strings.ToUpper(d) + `"}`, "no digest"},
		{"too short", `{"repository":3,"name":"app","digest":"sha256:abcd"}`, "no digest"},
	}
	for _, c := range cases {
		e, digest, why := decodeDeleted(json.RawMessage(c.entry))
		if (c.why == "") != (why == "") || (c.why != "" && !strings.Contains(why, c.why)) {
			t.Errorf("%s: why = %q, want %q", c.name, why, c.why)
		}
		if why == "" && (hex.EncodeToString(digest[:]) != d || e.Repository != 3) {
			t.Errorf("%s: %+v %x", c.name, e, digest)
		}
	}
}

func TestR_OCI_033_TheJournalEntryNamesTheRepositoryTheNameTheDigestAndThePrincipal(t *testing.T) {
	in := rows.ManifestDeleted{RepoID: 7, RepoKey: "web/docker-local", Name: "team/app", Digest: [32]byte{1, 2, 3}, Deleted: 2, Actor: "5b2a0c0e-0000-4000-8000-000000000001"}
	raw, err := journalEntry(in)
	if err != nil {
		t.Fatal(err)
	}
	e, digest, why := decodeDeleted(raw)
	if why != "" || e.Repository != 7 || e.Name != "team/app" || e.ActorID != in.Actor || digest != in.Digest || e.Digest != "sha256:"+hex.EncodeToString(in.Digest[:]) {
		t.Fatalf("%s decodes to %+v %x (%q)", raw, e, digest, why)
	}
	// The entry carries the schema's fields and no secret.
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil || len(generic) != 4 {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestR_OCI_019_TheSweeperNeedsItsPartsAndAPositiveGrace(t *testing.T) {
	ok := SweepOptions{Store: &Store{}, DB: nopRunner{}, Items: func() Remover { return nil }, Grace: time.Hour}
	if _, err := NewSweeper(ok); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*SweepOptions){
		"no store": func(o *SweepOptions) { o.Store = nil },
		"no db":    func(o *SweepOptions) { o.DB = nil },
		"no items": func(o *SweepOptions) { o.Items = nil },
		"no grace": func(o *SweepOptions) { o.Grace = 0 },
		"negative": func(o *SweepOptions) { o.Grace = -time.Second },
	} {
		o := ok
		mod(&o)
		if _, err := NewSweeper(o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	spec := SweepJobSpec(nil)
	if spec.Queue != "maintenance" || SweepJobKind != "oci-sweep" || SweepSchedule != "0 * * * *" {
		t.Errorf("%+v %s %s", spec, SweepJobKind, SweepSchedule)
	}
}

func TestR_OCI_019_ASweepWithoutTheItemStoreFailsAndRemovesNothing(t *testing.T) {
	sw, err := NewSweeper(SweepOptions{Store: &Store{}, DB: nopRunner{}, Items: func() Remover { return nil }, Grace: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sw.Sweep(t.Context()); err == nil {
		t.Fatal("a sweep without the item store succeeded")
	}
}

// nopRunner is a transaction runner that runs nothing: the tests above never reach the database.
type nopRunner struct{}

func (nopRunner) InTx(context.Context, func(meta.Tx) error) error { return nil }
