//go:build integration

package files

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_ARCH_009_ItemStoreCommitsCompanionsInOneTransaction: the files of one write
// (the lead and its companions) commit in one transaction. The hooks run per file in
// the order given, the lead's first, and a failure after the first file leaves
// nothing: no item, no ancestor folder, no audit row, no Extra row.
func TestR_ARCH_009_ItemStoreCommitsCompanionsInOneTransaction(t *testing.T) {
	f := newCommitFixture(t)
	e := f.e
	companion := func(t *testing.T, path, body string, generated bool) items.Companion {
		t.Helper()
		sp, err := f.r.items.Spool(context.Background(), strings.NewReader(body), items.IngestOptions{ContentLength: int64(len(body))})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { sp.Close() })
		return items.Companion{Path: repository.Cleaned{Path: path}, Spooled: sp, MediaType: "application/xml", Generated: generated, Payload: path}
	}

	t.Run("R-ARCH-009 a lead and its companions commit with one hook per file, the lead first, and one audit row each", func(t *testing.T) {
		f.log.take()
		before := len(f.r.rec.ofType(audit.EventArtifactDeployed))
		hooks := len(f.hooks.events())
		res, err := f.commit(t, "pkg/1.0/pkg.bin", "lead", func(s *items.CommitSpec) {
			s.Extra = f.extraRow("group")
			s.Companions = []items.Companion{companion(t, "pkg/1.0/pkg.meta", "<meta/>", true), companion(t, "pkg/1.0/pkg.sig", "sig", false)}
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(f.log.take(), ","); got != "extra,hook,audit:artifact.deployed,hook,audit:artifact.deployed,hook,audit:artifact.deployed" {
			t.Fatalf("order = %s", got)
		}
		ev := f.hooks.events()[hooks:]
		if len(ev) != 3 || ev[0].Item.ID != res.Item.ID || ev[0].Lead.ID != 0 {
			t.Fatalf("events = %+v", ev)
		}
		if len(res.Companions) != 2 {
			t.Fatalf("companions = %+v", res.Companions)
		}
		for n, p := range []string{"pkg/1.0/pkg.meta", "pkg/1.0/pkg.sig"} {
			if ev[n+1].Item.Path != p || ev[n+1].Lead.ID != res.Item.ID || ev[n+1].Payload != p || res.Companions[n].Item.Path != p || !res.Companions[n].Created {
				t.Errorf("companion %d: event %+v result %+v", n, ev[n+1], res.Companions[n])
			}
		}
		if got := len(f.r.rec.ofType(audit.EventArtifactDeployed)) - before; got != 3 {
			t.Errorf("%d deployed events, want 3", got)
		}
		if n := e.number("SELECT count(*) FROM items WHERE path = 'pkg/1.0/pkg.meta' AND generated"); n != 1 {
			t.Errorf("the derived companion is not marked generated")
		}
		if n := e.number("SELECT count(*) FROM items WHERE path IN ('pkg/1.0/pkg.bin', 'pkg/1.0/pkg.sig') AND generated"); n != 0 {
			t.Errorf("a file that was not derived is marked generated")
		}
		for _, seen := range f.hooks.rowInTx[hooks:] {
			if !seen {
				t.Error("a hook did not see its row in its own transaction")
			}
		}
	})

	t.Run("R-ARCH-009 a failure after the first file leaves nothing stored", func(t *testing.T) {
		f.log.take()
		before := len(f.r.rec.ofType(audit.EventArtifactDeployed))
		f.hooks.failPath = "fresh/2.0/pkg.meta"
		defer func() { f.hooks.failPath = "" }()
		_, err := f.commit(t, "fresh/2.0/pkg.bin", "lead two", func(s *items.CommitSpec) {
			s.Extra = f.extraRow("rolled-back-group")
			s.Companions = []items.Companion{companion(t, "fresh/2.0/pkg.meta", "<meta two/>", true)}
		})
		if err == nil || !strings.Contains(err.Error(), "the format refused fresh/2.0/pkg.meta") {
			t.Fatalf("err = %v", err)
		}
		if n := e.number("SELECT count(*) FROM items WHERE path LIKE 'fresh%'"); n != 0 {
			t.Errorf("%d items (lead, companion or ancestor folders) were left", n)
		}
		if n := e.number("SELECT count(*) FROM item_properties WHERE value = 'rolled-back-group'"); n != 0 {
			t.Errorf("%d Extra rows were left", n)
		}
		if n := len(f.r.rec.ofType(audit.EventArtifactDeployed)); n != before {
			t.Errorf("%d audit events of a rolled-back commit were recorded", n-before)
		}
	})

	t.Run("R-ARCH-009 a companion path that holds other content is refused, and the lead is not written", func(t *testing.T) {
		if _, err := f.commit(t, "taken/x.meta", "other", nil); err != nil {
			t.Fatal(err)
		}
		_, err := f.commit(t, "taken/x.bin", "lead three", func(s *items.CommitSpec) {
			s.MayOverwrite = false
			s.Companions = []items.Companion{companion(t, "taken/x.meta", "<meta three/>", true)}
		})
		if !errors.Is(err, items.ErrOverwriteDenied) {
			t.Fatalf("err = %v, want ErrOverwriteDenied", err)
		}
		if n := e.number("SELECT count(*) FROM items WHERE path = 'taken/x.bin'"); n != 0 {
			t.Errorf("the lead was written although its companion was refused")
		}
	})

	t.Run("R-STOR-007 an identical lead writes no companion and no hook runs", func(t *testing.T) {
		if _, err := f.commit(t, "same/y.bin", "lead four", nil); err != nil {
			t.Fatal(err)
		}
		hooks := len(f.hooks.events())
		res, err := f.commit(t, "same/y.bin", "lead four", func(s *items.CommitSpec) {
			s.Companions = []items.Companion{companion(t, "same/y.meta", "<meta four/>", true)}
		})
		if err != nil || res.Created || len(res.Companions) != 0 {
			t.Fatalf("%+v, %v", res, err)
		}
		if n := e.number("SELECT count(*) FROM items WHERE path = 'same/y.meta'"); n != 0 || len(f.hooks.events()) != hooks {
			t.Errorf("a companion of an identical lead was written (%d rows, %d hooks)", n, len(f.hooks.events())-hooks)
		}
	})

	t.Run("R-ARCH-009 two files of one commit cannot take one path", func(t *testing.T) {
		_, err := f.commit(t, "dup/z.bin", "lead five", func(s *items.CommitSpec) {
			s.Companions = []items.Companion{companion(t, "dup/z.bin", "again", false)}
		})
		if err == nil {
			t.Fatal("the commit succeeded")
		}
		if n := e.number("SELECT count(*) FROM items WHERE path LIKE 'dup%'"); n != 0 {
			t.Errorf("%d items were left", n)
		}
	})
}
