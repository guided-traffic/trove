//go:build integration

package files

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/meta"
)

// countingRunner counts the transactions, one per query of a listing.
type countingRunner struct {
	meta.Runner
	n atomic.Int64
}

func (c *countingRunner) InTx(ctx context.Context, fn func(tx meta.Tx) error) error {
	c.n.Add(1)
	return c.Runner.InTx(ctx, fn)
}

// TestR_STOR_017_AListingOfAMostlyHiddenFolder lists a folder whose first
// 120,000 entries the caller may not read, one entry per page: a page costs a
// bounded number of queries, and the cursor of a page that ends at the scan
// limit does not name the hidden entry it ended on (R-STOR-017, R-AUTHZ-016).
func TestR_STOR_017_AListingOfAMostlyHiddenFolder(t *testing.T) {
	e := newEnv(t)
	e.breakGlass()
	r := e.newReplica("a", replicaOpts{})
	r.createRepository("generic-wide", nil)
	reader := e.user("reader")
	e.grant(r, "wide-pub", []string{"generic-wide"}, authz.PatternLists{Include: []string{"pub-*"}}, map[string][]string{"user:reader": {"read"}})
	rid := e.number("SELECT id FROM repositories WHERE key = 'generic-wide'")
	e.exec(`INSERT INTO items (repo_id, path, parent_path, kind)
		SELECT $1, 'h-' || lpad(g::text, 6, '0'), '', 'folder' FROM generate_series(1, 120000) g`, rid)
	e.exec(`INSERT INTO items (repo_id, path, parent_path, kind) VALUES ($1, 'pub-1', '', 'folder'), ($1, 'pub-2', '', 'folder')`, rid)

	run := &countingRunner{Runner: r.db}
	svc, err := files.New(files.Options{Runner: run, Blobs: r.blobs, Repos: r.reader, Authz: r.engine, Recorder: audit.Nop{}, Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	caller := files.Caller{Subject: authz.Subject{Kind: principal.KindUser, ID: reader.ID, Name: reader.Name}}
	waitFor(t, "the grant taking effect", 10*time.Second, func() bool {
		return r.get("reader", "/files/generic-wide/pub-1/").status == 200
	})

	t.Run("R-STOR-017 a page that skips 100,000 hidden entries costs a few dozen queries", func(t *testing.T) {
		run.n.Store(0)
		page, err := svc.Children(context.Background(), caller, "generic-wide", files.ChildrenQuery{Folders: true, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if n := run.n.Load(); n > 50 {
			t.Fatalf("one page took %d queries", n)
		}
		if len(page.Items) != 0 || page.Next == "" {
			t.Fatalf("%d items, next %q: the page ends at the scan limit with a cursor", len(page.Items), page.Next)
		}
		t.Run("R-AUTHZ-016 the cursor does not name the hidden entry it ended on", func(t *testing.T) {
			if strings.Contains(page.Next, "h-") {
				t.Fatalf("the cursor %q names a hidden entry", page.Next)
			}
		})
		next, err := svc.Children(context.Background(), caller, "generic-wide", files.ChildrenQuery{Folders: true, Limit: 1, After: page.Next})
		if err != nil {
			t.Fatal(err)
		}
		if len(next.Items) != 1 || next.Items[0].Path != "pub-1" || next.Next != "pub-1" {
			t.Fatalf("the traversal does not reach the visible entries: %+v next %q", next.Items, next.Next)
		}
	})
	t.Run("R-AUTHZ-016 a position that names no item of the repository is an invalid cursor", func(t *testing.T) {
		other := e.number("SELECT max(id) + 1000 FROM items")
		_, err := svc.Children(context.Background(), caller, "generic-wide", files.ChildrenQuery{Folders: true, Limit: 1, After: "\x00" + strconv.FormatInt(other, 10)})
		if err == nil || !strings.Contains(err.Error(), "cursor") {
			t.Fatalf("%v", err)
		}
	})
}
