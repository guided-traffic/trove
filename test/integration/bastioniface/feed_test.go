//go:build integration

package bastioniface

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/errs"
)

// bastionClient is the Bastion's side of the feed: it applies every ID once and keeps the cursor.
type bastionClient struct {
	t       *testing.T
	s       *site
	token   string
	cursor  string
	applied map[string]iface.FeedEntry
	order   []string
}

func (s *site) bastionClient(token string) *bastionClient {
	return &bastionClient{t: s.t, s: s, token: token, applied: map[string]iface.FeedEntry{}}
}

func (b *bastionClient) read(after string, limit int) (iface.FeedAnswer, reply) {
	b.t.Helper()
	q := url.Values{"after": {after}}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	rp := b.s.iface(b.token, http.MethodGet, iface.PathFeed+"?"+q.Encode(), nil)
	var ans iface.FeedAnswer
	if rp.Status == http.StatusOK {
		rp.decode(b.t, &ans)
	}
	return ans, rp
}

// poll reads from the client's cursor until the feed has nothing more, applies the entries it has not seen and
// returns every entry of the answers, duplicates included.
func (b *bastionClient) poll(limit int) (all []iface.FeedEntry, resets int) {
	b.t.Helper()
	for range 50 {
		ans, rp := b.read(b.cursor, limit)
		if rp.Status != http.StatusOK {
			b.t.Fatalf("feed: %d %s", rp.Status, rp.Body)
		}
		all = append(all, ans.Entries...)
		if ans.Reset {
			resets++
			b.applied, b.order = map[string]iface.FeedEntry{}, nil
		}
		for _, e := range ans.Entries {
			if _, seen := b.applied[e.ID]; !seen {
				b.applied[e.ID] = e
				b.order = append(b.order, e.ID)
			}
		}
		b.cursor = ans.Cursor
		if !ans.More {
			return all, resets
		}
	}
	b.t.Fatal("the feed does not end")
	return nil, 0
}

func ids(es []iface.FeedEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

// feedSite is a site with one registration and its token.
func feedSite(t *testing.T, o siteOpts) (*site, *env, string) {
	e := newEnv(t)
	s := e.newSite(o)
	bid := e.registration("partners")
	return s, e, s.registered("partners", bid)
}

// TestR_BAS_015_RevokingATokenAddsOneEntryThatIsRepeatedOnlyWithinTheLookback is the first acceptance criterion
// of R-BAS-015 with the real token service: the revocation of $FULL is a token entry within the commit, a read
// with the earlier cursor returns it once, and a read with the returned cursor returns it again, with the same
// ID, for as long as it lies within the lookback.
func TestR_BAS_015_RevokingATokenAddsOneEntryThatIsRepeatedOnlyWithinTheLookback(t *testing.T) {
	s, _, bst := feedSite(t, siteOpts{})
	b := s.bastionClient(bst)
	hs := s.iface(bst, http.MethodGet, iface.PathHandshake, nil)
	var h iface.Handshake
	hs.decode(t, &h)
	b.cursor = h.Feed.Cursor

	sa := s.serviceAccount("acme-full")
	full := s.sat(sa)
	if all, _ := b.poll(0); len(all) != 0 {
		t.Fatalf("the feed holds %v before anything was revoked", ids(all))
	}

	if _, err := s.tokens.Revoke(context.Background(), admin, full.Record.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	ans, rp := b.read(b.cursor, 0)
	if rp.Status != http.StatusOK || len(ans.Entries) != 1 {
		t.Fatalf("%d %s", rp.Status, rp.Body)
	}
	e := ans.Entries[0]
	if e.Kind != iface.FeedToken || !slices.Equal(e.Tokens, []string{full.Record.ID}) || e.Principals != nil || e.Repositories != nil || e.At.IsZero() {
		t.Errorf("entry = %+v", e)
	}
	if ans.Reset || ans.More || ans.Cursor == b.cursor {
		t.Errorf("answer = %+v", ans)
	}
	again, _ := b.read(ans.Cursor, 0)
	if len(again.Entries) != 1 || again.Entries[0].ID != e.ID {
		t.Fatalf("a read with the returned cursor within the lookback returned %v, want %s again", ids(again.Entries), e.ID)
	}
	if again.Cursor != ans.Cursor {
		t.Errorf("the cursor moved from %s to %s without a newer entry", ans.Cursor, again.Cursor)
	}
}

// TestR_BAS_015_TheLookbackBoundsWhatIsRepeated: an entry that lies further behind the cursor than the lookback is
// not sent again.
func TestR_BAS_015_TheLookbackBoundsWhatIsRepeated(t *testing.T) {
	s, e, bst := feedSite(t, siteOpts{lookback: time.Minute})
	b := s.bastionClient(bst)
	uid := "0192f1c4-0000-7000-8000-0000000000c1"
	old := e.journaled(audit.EventUserDisabled, 10*time.Minute, uid, "", `{}`)
	recent := e.journaled(audit.EventUserDisabled, 30*time.Second, uid, "", `{}`)
	newest := e.journaled(audit.EventUserDisabled, 5*time.Second, uid, "", `{}`)

	ans, _ := b.read("t:"+time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), 0)
	if !slices.Equal(ids(ans.Entries), []string{old, recent, newest}) {
		t.Fatalf("history = %v", ids(ans.Entries))
	}
	for _, e := range ans.Entries {
		if e.Kind != iface.FeedPrincipal || !slices.Equal(e.Principals, []string{uid}) {
			t.Errorf("entry = %+v", e)
		}
	}
	// A cursor at the newest entry re-sends what lies within a minute behind it: the recent one and itself,
	// not the old one.
	again, _ := b.read(ans.Cursor, 0)
	if !slices.Equal(ids(again.Entries), []string{recent, newest}) || again.Reset {
		t.Errorf("behind the cursor: %v (reset %v)", ids(again.Entries), again.Reset)
	}
}

// TestR_BAS_015_AnUnpublishNamesTheRepositoryAndEveryVirtualRepositoryIncludingIt: the third acceptance criterion;
// the repositories are read when the feed is read.
func TestR_BAS_015_AnUnpublishNamesTheRepositoryAndEveryVirtualRepositoryIncludingIt(t *testing.T) {
	s, e, bst := feedSite(t, siteOpts{})
	b := s.bastionClient(bst)
	e.repository("web/npm-local", "local")
	e.repository("web/npm-remote", "remote")
	e.repository("web/npm-all", "virtual", "web/npm-local", "web/npm-remote")
	e.repository("web/npm-everything", "virtual", "web/npm-all")
	e.repository("web/npm-other", "virtual", "web/npm-remote")
	// The same key in another project is another repository: its virtual repositories do not join the closure.
	e.repository("data/npm-local", "local")
	e.repository("data/npm-all", "virtual", "data/npm-local")
	cursor := "t:" + time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	id := e.journaled(audit.EventNPMUnpublish, time.Second, "", "web/npm-local", `{"package":"@acme/sdk","version":"1.0.0","integrity":"sha512-x"}`)
	del := e.journaled(audit.EventPackageDeleted, time.Second, "", "web/npm-gone", `{"package":"acme.sdk","version":"1.0.0"}`)
	ans, _ := b.read(cursor, 0)
	if len(ans.Entries) != 2 || ans.Entries[0].ID != id || ans.Entries[1].ID != del {
		t.Fatalf("entries = %+v", ans.Entries)
	}
	if got := ans.Entries[0]; got.Kind != iface.FeedContent || !slices.Equal(got.Repositories, []string{"web/npm-local", "web/npm-all", "web/npm-everything"}) {
		t.Errorf("content entry = %+v", got)
	}
	if got := ans.Entries[1]; !slices.Equal(got.Repositories, []string{"web/npm-gone"}) {
		t.Errorf("an unknown repository is reported alone: %+v", got)
	}
	// The closure is read at the time the feed is read: a repository that includes web/npm-local from now on is in
	// the next answer for the same event.
	e.repository("web/npm-late", "virtual", "web/npm-local")
	ans, _ = b.read(cursor, 0)
	if !slices.Equal(ans.Entries[0].Repositories, []string{"web/npm-local", "web/npm-all", "web/npm-everything", "web/npm-late"}) {
		t.Errorf("closure at read time = %v", ans.Entries[0].Repositories)
	}
}

// TestR_BAS_015_TheRealEventsOfTheIdentityAndSettingsAreMapped: events written by the real services, not by the
// test: a group member removed (the member is named in the event, the entry carries the ID), a user disabled, and a
// switch of anonymous access off, which yields a settings and a grants entry with the IDs of one event.
func TestR_BAS_015_TheRealEventsOfTheIdentityAndSettingsAreMapped(t *testing.T) {
	s, _, bst := feedSite(t, siteOpts{})
	b := s.bastionClient(bst)
	ctx := context.Background()
	hs := s.iface(bst, http.MethodGet, iface.PathHandshake, nil)
	var h iface.Handshake
	hs.decode(t, &h)
	b.cursor = h.Feed.Cursor

	alice, err := s.manager.CreateUser(ctx, admin, principal.NewUser{Name: "alice", Email: "alice@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	bot := s.serviceAccount("ci-bot")
	if _, err := s.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "devs", Members: []string{"alice", "sa:ci-bot"}}); err != nil {
		t.Fatal(err)
	}
	if all, _ := b.poll(0); len(all) != 0 {
		t.Fatalf("widenings reached the feed: %v", all)
	}
	if _, err := s.manager.PatchMembers(ctx, admin, "devs", "manual", nil, []string{"alice", "sa:ci-bot"}); err != nil {
		t.Fatal(err)
	}
	if err := s.manager.DisableUser(ctx, admin, "alice", principal.DisableOptions{Reason: "admin"}); err != nil {
		t.Fatal(err)
	}
	// Switching anonymous access on only widens: a settings entry, so that the Bastion handshakes again.
	// Switching it off narrows: the same event also yields a grants entry.
	on, off := true, false
	for _, v := range []*bool{&on, &off} {
		if _, err := s.setts.Update(ctx, admin, token.SettingsPatch{AnonymousAccess: v, Expected: errs.AnyRevision}); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := b.poll(0)
	var kinds []iface.FeedKind
	for _, e := range all {
		kinds = append(kinds, e.Kind)
	}
	want := []iface.FeedKind{iface.FeedPrincipal, iface.FeedPrincipal, iface.FeedSettings, iface.FeedSettings, iface.FeedGrants}
	if !slices.Equal(kinds, want) {
		t.Fatalf("kinds = %v, want %v: %+v", kinds, want, all)
	}
	members := all[0].Principals
	slices.Sort(members)
	if wantIDs := []string{alice.ID, bot.ID}; !slices.Equal(members, sortedCopy(wantIDs)) {
		t.Errorf("removed members = %v, want %v", members, wantIDs)
	}
	if !slices.Equal(all[1].Principals, []string{alice.ID}) {
		t.Errorf("the disabled user = %v", all[1].Principals)
	}
	if all[4].ID != all[3].ID+":1" {
		t.Errorf("the second entry of one event has the ID %q, want %q", all[4].ID, all[3].ID+":1")
	}
	for _, e := range all {
		if e.Tokens != nil && e.Kind != iface.FeedToken {
			t.Errorf("entry = %+v", e)
		}
	}
}

func sortedCopy(s []string) []string { c := slices.Clone(s); slices.Sort(c); return c }

// TestR_BAS_015_APageIsCutAtTheLimitAndAlwaysAdvances: the entries after the cursor are cut at the limit, more says
// so, and following the cursor returns every entry exactly once in order, whatever the lookback re-sends.
func TestR_BAS_015_APageIsCutAtTheLimitAndAlwaysAdvances(t *testing.T) {
	s, e, bst := feedSite(t, siteOpts{maxPage: 3, lookback: 5 * time.Minute})
	b := s.bastionClient(bst)
	var want []string
	for i := range 10 {
		want = append(want, e.journaled(audit.EventUserDisabled, time.Duration(20-i)*time.Second, "0192f1c4-0000-7000-8000-0000000000"+strconv.Itoa(10+i), "", `{}`))
	}
	b.cursor = "t:" + time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)

	first, _ := b.read(b.cursor, 0)
	if len(first.Entries) != 3 || !first.More || first.Reset {
		t.Fatalf("first page: %d entries, more %v", len(first.Entries), first.More)
	}
	if over, _ := b.read(b.cursor, 50); len(over.Entries) != 3 {
		t.Errorf("a limit above max_page returned %d entries", len(over.Entries))
	}
	b.poll(0)
	if !slices.Equal(b.order, want) {
		t.Fatalf("applied %v\nwant    %v", b.order, want)
	}
	if rp := s.iface(bst, http.MethodGet, iface.PathFeed+"?after="+url.QueryEscape(b.cursor)+"&limit=0", nil); rp.Status != http.StatusUnprocessableEntity {
		t.Errorf("limit 0: %d %s", rp.Status, rp.Body)
	}
}

// TestR_BAS_015_AnEventWithTwoEntriesIsNeverSplit: the page is cut at event boundaries, so the cursor never names an
// event whose second entry was left out.
func TestR_BAS_015_AnEventWithTwoEntriesIsNeverSplit(t *testing.T) {
	s, e, bst := feedSite(t, siteOpts{maxPage: 10})
	b := s.bastionClient(bst)
	b.cursor = "t:" + time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	off := `{"before":{"anonymousAccess":true},"after":{"anonymousAccess":false}}`
	first := e.journaled(audit.EventSettingsAuthChanged, 3*time.Second, "", "", off)
	second := e.journaled(audit.EventSettingsAuthChanged, 2*time.Second, "", "", off)
	ans, _ := b.read(b.cursor, 1)
	// A limit of 1 takes the first event whole: both its entries, then more.
	if !slices.Equal(ids(ans.Entries), []string{first, first + ":1"}) || !ans.More {
		t.Fatalf("limit 1: %v more %v", ids(ans.Entries), ans.More)
	}
	next, _ := b.read(ans.Cursor, 1)
	got := ids(next.Entries)
	if !slices.Contains(got, second) || !slices.Contains(got, second+":1") || next.More {
		t.Errorf("next page: %v more %v", got, next.More)
	}
}

// TestR_BAS_015_AVanishedCursorEntryWithinRetentionResets: a point-in-time restore is simulated by deleting the row
// of the entry the cursor names; the answer is reset, empty, and its cursor is a valid position.
func TestR_BAS_015_AVanishedCursorEntryWithinRetentionResets(t *testing.T) {
	s, e, bst := feedSite(t, siteOpts{})
	b := s.bastionClient(bst)
	id := e.journaled(audit.EventUserDisabled, 20*time.Second, "0192f1c4-0000-7000-8000-0000000000c1", "", `{}`)
	b.cursor = "t:" + time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	b.poll(0)
	if len(b.order) != 1 || b.order[0] != id {
		t.Fatalf("applied %v", b.order)
	}
	e.exec("DELETE FROM audit_event WHERE id = $1::uuid", id)

	ans, rp := b.read(b.cursor, 0)
	if rp.Status != http.StatusOK || !ans.Reset || len(ans.Entries) != 0 || ans.More || ans.Cursor == "" || ans.Cursor == b.cursor {
		t.Fatalf("answer = %+v", ans)
	}
	// The cursor of the reset is usable: the next read is not a reset.
	next, _ := b.read(ans.Cursor, 0)
	if next.Reset {
		t.Errorf("the cursor of a reset resets again: %+v", next)
	}
}

// TestR_BAS_015_ACursorInTheFutureBeyondTheLookbackOrBeforeTheRetentionResets covers the other two conditions of a
// reset, for both forms of cursor.
func TestR_BAS_015_ACursorInTheFutureBeyondTheLookbackOrBeforeTheRetentionResets(t *testing.T) {
	s, e, bst := feedSite(t, siteOpts{lookback: time.Minute})
	b := s.bastionClient(bst)
	id := e.journaled(audit.EventUserDisabled, 5*time.Second, "0192f1c4-0000-7000-8000-0000000000c1", "", `{}`)
	_ = id
	now := time.Now().UTC()
	for name, after := range map[string]string{
		"a synthetic cursor two minutes ahead": "t:" + now.Add(2*time.Minute).Format(time.RFC3339Nano),
		"an entry cursor two minutes ahead":    iface.Cursor{At: now.Add(2 * time.Minute), ID: "0192f1c4-0000-7000-8000-0000000000ff"}.String(),
		"a synthetic cursor before retention":  "t:" + now.AddDate(-2, 0, 0).Format(time.RFC3339Nano),
		"an entry cursor before retention":     iface.Cursor{At: now.AddDate(-2, 0, 0), ID: "0192f1c4-0000-7000-8000-0000000000ff"}.String(),
	} {
		ans, rp := b.read(after, 0)
		if rp.Status != http.StatusOK || !ans.Reset || len(ans.Entries) != 0 {
			t.Errorf("%s: %d %+v", name, rp.Status, ans)
		}
	}
	// A cursor slightly ahead, within the lookback, is the clock skew of a replica and no reset.
	if ans, _ := b.read("t:"+now.Add(20*time.Second).Format(time.RFC3339Nano), 0); ans.Reset {
		t.Errorf("a cursor 20 s ahead reset: %+v", ans)
	}
}

// TestR_BAS_015_MoreEntriesBehindTheCursorThanFourPagesResets: the entries behind the cursor exceed four times
// max_page, so the answer is reset and never a truncated list (Q-BAS-49).
func TestR_BAS_015_MoreEntriesBehindTheCursorThanFourPagesResets(t *testing.T) {
	s, e, bst := feedSite(t, siteOpts{maxPage: 2, lookback: 5 * time.Minute})
	b := s.bastionClient(bst)
	var last string
	for i := range 9 {
		last = e.journaled(audit.EventUserDisabled, time.Duration(30-i)*time.Second, "0192f1c4-0000-7000-8000-0000000000"+strconv.Itoa(10+i), "", `{}`)
	}
	at := e.text("SELECT occurred_at::text FROM audit_event WHERE id = $1::uuid", last)
	ts, err := time.Parse("2006-01-02 15:04:05.999999-07", at)
	if err != nil {
		t.Fatal(err)
	}
	ans, _ := b.read(iface.Cursor{At: ts.UTC(), ID: last}.String(), 0)
	if !ans.Reset || len(ans.Entries) != 0 {
		t.Fatalf("nine entries behind a cursor with max_page 2: %+v", ans)
	}
	// Eight are within the bound of 4 x 2.
	e.exec("DELETE FROM audit_event WHERE id = (SELECT id FROM audit_event WHERE action = $1 ORDER BY occurred_at LIMIT 1)", audit.EventUserDisabled)
	if ans, _ := b.read(iface.Cursor{At: ts.UTC(), ID: last}.String(), 0); ans.Reset || len(ans.Entries) != 8 {
		t.Errorf("eight entries behind the cursor: reset %v, %d entries", ans.Reset, len(ans.Entries))
	}
}

// TestR_BAS_015_TheFeedNamesIDsOnlyAndAMalformedCursorIsRefused: the entries carry no names or secrets, and a cursor
// that does not parse is a 400 invalid_cursor.
func TestR_BAS_015_TheFeedNamesIDsOnlyAndAMalformedCursorIsRefused(t *testing.T) {
	s, _, bst := feedSite(t, siteOpts{})
	for _, after := range []string{"", "not-a-cursor", "t:yesterday", "bm90LWEtcGlwZQ"} {
		rp := s.iface(bst, http.MethodGet, iface.PathFeed+"?after="+url.QueryEscape(after), nil)
		if rp.Status != http.StatusBadRequest {
			t.Errorf("after %q: %d %s", after, rp.Status, rp.Body)
		}
	}
	if rp := s.iface(bst, http.MethodGet, iface.PathFeed+"?after=t:2026-01-01T00:00:00Z&x=1", nil); rp.Status != http.StatusOK {
		t.Logf("an unknown query parameter is ignored: %d", rp.Status)
	}
	rp := s.iface(bst, http.MethodGet, iface.PathFeed, nil)
	if rp.Status != http.StatusBadRequest {
		t.Errorf("without after: %d %s", rp.Status, rp.Body)
	}
}
