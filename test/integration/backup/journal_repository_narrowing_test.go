//go:build integration

package backup

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/journal/replay"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// R-REPO-061, R-HA-054, R-BAK-024: the narrowings of a repository and its claims, changed after t through the service the API
// uses, shipped with the journal's shipper and replayed into the copy made at t. Each test changes the restored copy where the
// case needs a restored state that differs from the one at t (the journal replay meets whatever a restore brought back), and
// shows the second replay applying nothing again.

var repoCaller = repository.Caller{Actor: audit.Actor{Kind: "admin-cli", Name: "test"}}

// narrowWorld is a twin with the project web and a repository service on the source.
type narrowWorld struct {
	t   *testing.T
	tw  *twin
	src *twinSide
	svc *repository.Service
}

func newNarrowWorld(t *testing.T) *narrowWorld {
	t.Helper()
	tw := newTwin(t)
	w := &narrowWorld{t: t, tw: tw}
	w.src = tw.open(tw.src)
	w.src.exec("INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'web', 'web')")
	w.svc = repositories(t, w.src)
	return w
}

// create makes a repository of the project web through the service.
func (w *narrowWorld) create(class, key, format, extra string) {
	w.t.Helper()
	switch class {
	case "remote":
		remote := `"remote":{"url":"https://upstream.example.test/"}`
		if extra == "" {
			extra = remote
		} else {
			extra = remote + "," + extra
		}
	}
	if extra != "" {
		extra = "," + extra
	}
	body := fmt.Sprintf(`{"key":%q,"class":%q,"format":%q%s}`, key, class, format, extra)
	if _, err := w.svc.Create(context.Background(), repoCaller, "web", []byte(body)); err != nil {
		w.t.Fatalf("create %s: %v", key, err)
	}
}

func (w *narrowWorld) claims(key string, in repository.ClaimInput) {
	w.t.Helper()
	allow := func(context.Context) error { return nil }
	if _, err := w.svc.PutClaims(context.Background(), repoCaller, qname.Repo{Project: "web", Key: key}, repository.Precondition{Any: true}, in, allow); err != nil {
		w.t.Fatalf("claims of %s: %v", key, err)
	}
}

// snapshot closes the source and takes t.
func (w *narrowWorld) snapshot() {
	// The journal rows of the set-up (claims written before t) are not under test.
	w.src.exec("DELETE FROM revocation_outbox")
	w.src.close()
	w.tw.snapshot()
	w.src = w.tw.open(w.tw.src)
	w.svc = repositories(w.t, w.src)
}

func (w *narrowWorld) patch(key, member string, value any) {
	w.t.Helper()
	if _, err := w.svc.Patch(context.Background(), repoCaller, qname.Repo{Project: "web", Key: key}, repository.Precondition{Any: true}, setMember(member, value)); err != nil {
		w.t.Fatalf("patch %s %s: %v", key, member, err)
	}
}

func virtualDoc(members []string, rules ...map[string]any) map[string]any {
	pins := []map[string]any{}
	pins = append(pins, rules...)
	return map[string]any{"members": members, "deployTarget": nil, "mergedMetadataTtlSeconds": 7200, "pinning": map[string]any{"rules": pins}}
}

func pinRule(members []string, patterns ...string) map[string]any {
	return map[string]any{"packages": patterns, "members": members}
}

// ship returns the store with the source's journal and closes the source.
func (w *narrowWorld) ship(wantObjects int) journal.Store {
	w.t.Helper()
	store := newMemStore()
	if n := w.src.ship(store); n != wantObjects {
		w.t.Fatalf("%d journal objects shipped, want %d", n, wantObjects)
	}
	return store
}

func (s *twinSide) repoID(key string) int64 {
	s.t.Helper()
	return s.number("SELECT id FROM repositories WHERE key = $1", key)
}

// doc reads one top-level member of the stored configuration as text ("" when absent).
func (s *twinSide) member(key, member string) string {
	s.t.Helper()
	return s.text("SELECT COALESCE(config->$2, 'null'::jsonb)::text FROM repositories WHERE key = $1", key, member)
}

func (s *twinSide) disabledRepo(key string) bool {
	s.t.Helper()
	return s.text("SELECT COALESCE(config->>'disabled', 'false') FROM repositories WHERE key = $1", key) == "true"
}

func (s *twinSide) members(virtual string) string {
	s.t.Helper()
	return s.text(`SELECT COALESCE(string_agg(r.key, ',' ORDER BY m.position), '') FROM virtual_members m
		JOIN repositories r ON r.id = m.member_id WHERE m.virtual_id = (SELECT id FROM repositories WHERE key = $1)`, virtual)
}

// pin lists the members of the rule that holds the pattern; "-" when no rule holds it.
func (s *twinSide) pin(virtual, pattern string) string {
	s.t.Helper()
	return s.text(`SELECT CASE WHEN count(DISTINCT pr.position) = 0 THEN '-' ELSE COALESCE(string_agg(r.key, ',' ORDER BY r.key), '') END
		FROM virtual_pin_rules pr
		LEFT JOIN virtual_pin_members pm ON pm.virtual_id = pr.virtual_id AND pm.position = pr.position
		LEFT JOIN repositories r ON r.id = pm.member_id
		WHERE pr.virtual_id = (SELECT id FROM repositories WHERE key = $1) AND $2 = ANY(pr.package_patterns)`, virtual, pattern)
}

func (s *twinSide) claimList(key string) string {
	s.t.Helper()
	return s.text(`SELECT COALESCE(string_agg(pattern || ':' || scope || ':' || source, ',' ORDER BY pattern COLLATE "C"), '')
		FROM namespace_claims WHERE repo_id = (SELECT id FROM repositories WHERE key = $1)`, key)
}

func (s *twinSide) configVersion(key string) int64 {
	return s.number("SELECT config_version FROM repositories WHERE key = $1", key)
}

// secondPass is the R-BAK-024 subtest: replaying the same journal again applies nothing and suspends nothing new.
func secondPass(t *testing.T, rst *twinSide, store journal.Store, at time.Time, want int) {
	t.Helper()
	t.Run("R-BAK-024 a second replay applies nothing again", func(t *testing.T) {
		before := rst.number("SELECT COALESCE(sum(config_version), 0) FROM repositories")
		again, _ := rst.replay(store, at, rst.changes)
		if again.Counts[journal.Applied] != 0 || again.Counts[journal.Suspended] != 0 || again.Counts[journal.AlreadyInEffect] != want {
			t.Errorf("second replay: %v, want %d already in effect", again.Counts, want)
		}
		if after := rst.number("SELECT COALESCE(sum(config_version), 0) FROM repositories"); after != before {
			t.Errorf("the second replay moved config_version from %d to %d", before, after)
		}
	})
}

func forged(t *testing.T, store journal.Store, event string, entry map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(entry)
	return handWritten(t, store, event, raw)
}

// TestR_REPO_061_AnExcludeAddedAfterTheRestorePointIsReplayed: the pattern added to npmjs after t is in effect after the
// restore; the entries that cannot be replayed are reported without writing anything.
func TestR_REPO_061_AnExcludeAddedAfterTheRestorePointIsReplayed(t *testing.T) {
	w := newNarrowWorld(t)
	w.create("remote", "npmjs", "npm", `"excludePatterns":["@old/*"]`)
	w.create("remote", "npm-gone", "npm", "")
	w.create("remote", "npm-deleting", "npm", "")
	w.snapshot()
	w.patch("npmjs", "excludePatterns", []string{"@old/*", "@evil/*"})
	w.patch("npmjs", "description", "the public registry")
	if n := w.src.number("SELECT count(*) FROM revocation_outbox WHERE event = 'repository.narrowed'"); n != 1 {
		t.Fatalf("%d repository.narrowed rows, want 1: the description update narrows nothing", n)
	}
	store := w.ship(1)
	goneID, delID := w.src.repoID("npm-gone"), w.src.repoID("npm-deleting")
	forgedGone := forged(t, store, "repository.narrowed", map[string]any{"repositoryId": "999999999", "key": "web/ghost", "excludeAdded": []string{"@x/*"}})
	forgedDel := forged(t, store, "repository.narrowed", map[string]any{"repositoryId": fmt.Sprint(delID), "key": "web/npm-deleting", "excludeAdded": []string{"@x/*"}})
	forgedBad := forged(t, store, "repository.narrowed", map[string]any{"repositoryId": fmt.Sprint(goneID), "key": "web/npm-gone", "excludeAdded": []string{""}})
	forgedNoID := forged(t, store, "repository.narrowed", map[string]any{"repositoryId": "web/npm-gone", "excludeAdded": []string{"@x/*"}})
	w.src.close()

	rst := w.tw.open(w.tw.restored)
	rst.exec("UPDATE repositories SET lifecycle = 'deleting' WHERE key = 'npm-deleting'")
	before := rst.configVersion("npmjs")
	sum, _ := rst.replay(store, w.tw.at, rst.changes)
	if o := outcomes(sum)["repository.narrowed web/npmjs"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
		t.Errorf("repository.narrowed web/npmjs: %v, want applied", o)
	}
	if got := rst.member("npmjs", "excludePatterns"); got != `["@old/*", "@evil/*"]` {
		t.Errorf("excludePatterns = %s", got)
	}
	if rst.configVersion("npmjs") != before+1 || rst.disabledRepo("npmjs") {
		t.Errorf("config_version %d -> %d, disabled %v", before, rst.configVersion("npmjs"), rst.disabledRepo("npmjs"))
	}
	if d := rst.text("SELECT COALESCE(config->>'description', '') FROM repositories WHERE key = 'npmjs'"); d != "" {
		t.Errorf("the description %q was replayed", d)
	}
	if rst.number("SELECT count(*) FROM audit_event WHERE action = 'repository.narrowed'") != 0 {
		t.Error("the replay wrote an audit event")
	}
	t.Run("R-HA-054 an entry that cannot be replayed writes nothing", func(t *testing.T) {
		for id, want := range map[string]journal.Outcome{
			forgedGone: journal.SubjectUnknown, forgedDel: journal.SubjectUnknown, forgedBad: journal.Skipped, forgedNoID: journal.Skipped,
		} {
			if got := outcomeOf(t, sum, id); got != want {
				t.Errorf("entry %s: %s, want %s", id, got, want)
			}
		}
		if rst.member("npm-gone", "excludePatterns") != "[]" || rst.member("npm-deleting", "excludePatterns") != "[]" {
			t.Error("a refused entry changed a repository")
		}
	})
	secondPass(t, rst, store, w.tw.at, 1)
}

// TestR_REPO_061_AnIncludeListIsIntersectedAndAnEmptyIntersectionDisables: an include list narrowed after t is the
// intersection with the restored list, an empty restored list takes the journaled one, and no pattern left disables the
// repository and reports it suspended (Q-REPO-247).
func TestR_REPO_061_AnIncludeListIsIntersectedAndAnEmptyIntersectionDisables(t *testing.T) {
	w := newNarrowWorld(t)
	w.create("remote", "shrunk", "npm", `"includePatterns":["@a/*","@b/*","@x/*"]`)
	w.create("remote", "firstlist", "npm", "")
	w.create("remote", "foreign", "npm", `"includePatterns":["@z/*"]`)
	w.snapshot()
	w.patch("shrunk", "includePatterns", []string{"@a/*", "@b/*"})
	w.patch("firstlist", "includePatterns", []string{"@acme/*"})
	store := w.ship(2)
	foreignID := w.src.repoID("foreign")
	disjoint := forged(t, store, "repository.narrowed", map[string]any{"repositoryId": fmt.Sprint(foreignID), "key": "web/foreign", "include": []string{"@a/*"}})
	w.src.close()

	rst := w.tw.open(w.tw.restored)
	sum, _ := rst.replay(store, w.tw.at, rst.changes)
	got := outcomes(sum)
	for _, k := range []string{"shrunk", "firstlist"} {
		if o := got["repository.narrowed web/"+k]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
			t.Errorf("%s: %v, want applied", k, o)
		}
	}
	if rst.member("shrunk", "includePatterns") != `["@a/*", "@b/*"]` || rst.member("firstlist", "includePatterns") != `["@acme/*"]` {
		t.Errorf("include lists: %s, %s", rst.member("shrunk", "includePatterns"), rst.member("firstlist", "includePatterns"))
	}
	if o := got["repository.narrowed web/foreign"]; !slices.Equal(o, []journal.Outcome{journal.Suspended}) {
		t.Errorf("foreign: %v, want suspended", o)
	}
	if !rst.disabledRepo("foreign") || rst.member("foreign", "includePatterns") != `["@z/*"]` {
		t.Errorf("foreign: disabled %v, include %s: disabled, list unchanged", rst.disabledRepo("foreign"), rst.member("foreign", "includePatterns"))
	}
	for _, r := range sum.Results {
		if r.ID == disjoint {
			var d struct {
				Repository  string
				Parts       []string
				SuspendedBy string
				Entry       map[string]any
			}
			if err := json.Unmarshal(r.Detail, &d); err != nil || d.Repository != "web/foreign" || d.SuspendedBy != "include" || d.Entry["include"] == nil {
				t.Errorf("the suspension detail = %s (%v)", r.Detail, err)
			}
		}
	}
	secondPass(t, rst, store, w.tw.at, 3)
}

// TestR_REPO_061_AMemberRemovedIsRemovedAgain: a member taken out of a virtual repository after t is taken out of the restored
// one, with its pin-rule entries.
func TestR_REPO_061_AMemberRemovedIsRemovedAgain(t *testing.T) {
	w := newNarrowWorld(t)
	w.create("local", "plain", "npm", `"local":{"autoClaim":false}`)
	w.create("local", "claimless", "npm", `"local":{"autoClaim":false}`)
	w.create("remote", "npmjs", "npm", "")
	w.create("virtual", "all", "npm", `"virtual":{"members":["plain","claimless","npmjs"],"pinning":{"rules":[{"packages":["@acme/*"],"members":["plain","claimless"]}]}}`)
	w.snapshot()
	w.patch("all", "virtual", virtualDoc([]string{"claimless", "npmjs"}, pinRule([]string{"claimless"}, "@acme/*")))
	store := w.ship(1)
	w.src.close()

	rst := w.tw.open(w.tw.restored)
	sum, _ := rst.replay(store, w.tw.at, rst.changes)
	if o := outcomes(sum)["repository.narrowed web/all"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
		t.Fatalf("repository.narrowed web/all: %v, want applied", o)
	}
	if got := rst.members("all"); got != "claimless,npmjs" {
		t.Errorf("members = %s", got)
	}
	if got := rst.pin("all", "@acme/*"); got != "claimless" {
		t.Errorf("the rule's members = %s: the removed member leaves the rule too", got)
	}
	if rst.disabledRepo("all") {
		t.Error("a member without claims was removed and the repository was disabled")
	}
	secondPass(t, rst, store, w.tw.at, 1)
}

// TestR_REPO_061_AMemberWhoseClaimsWouldLeaveEffectDisablesTheVirtualRepository: the removal of a local member that holds a
// claim of scope members from a virtual repository that keeps another member, remote or local, disables the repository and keeps
// the member (Q-REPO-248); a member without such a claim is removed.
func TestR_REPO_061_AMemberWhoseClaimsWouldLeaveEffectDisablesTheVirtualRepository(t *testing.T) {
	w := newNarrowWorld(t)
	w.create("local", "internal", "npm", `"local":{"autoClaim":false}`)
	w.create("remote", "npmjs", "npm", "")
	w.create("virtual", "with-remote", "npm", `"virtual":{"members":["internal","npmjs"]}`)
	w.create("local", "internal-a", "npm", `"local":{"autoClaim":false}`)
	w.create("local", "internal-b", "npm", `"local":{"autoClaim":false}`)
	w.create("virtual", "locals-only", "npm", `"virtual":{"members":["internal-a","internal-b"]}`)
	w.create("local", "plain-a", "npm", `"local":{"autoClaim":false}`)
	w.create("local", "plain-b", "npm", `"local":{"autoClaim":false}`)
	w.create("virtual", "no-claims", "npm", `"virtual":{"members":["plain-a","plain-b"]}`)
	w.claims("internal", repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@acme/*"}}})
	w.claims("internal-a", repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@team/*"}}})
	w.snapshot()
	w.patch("with-remote", "virtual", virtualDoc([]string{"npmjs"}))
	w.patch("locals-only", "virtual", virtualDoc([]string{"internal-b"}))
	w.patch("no-claims", "virtual", virtualDoc([]string{"plain-b"}))
	store := w.ship(3)
	w.src.close()

	rst := w.tw.open(w.tw.restored)
	sum, _ := rst.replay(store, w.tw.at, rst.changes)
	got := outcomes(sum)
	if o := got["repository.narrowed web/with-remote"]; !slices.Equal(o, []journal.Outcome{journal.Suspended}) {
		t.Errorf("with-remote: %v, want suspended", o)
	}
	if !rst.disabledRepo("with-remote") || rst.members("with-remote") != "internal,npmjs" {
		t.Errorf("with-remote: disabled %v members %s: disabled, member kept", rst.disabledRepo("with-remote"), rst.members("with-remote"))
	}
	if o := got["repository.narrowed web/locals-only"]; !slices.Equal(o, []journal.Outcome{journal.Suspended}) {
		t.Errorf("locals-only: %v, want suspended: the claim's names would reach the local member that stays", o)
	}
	if !rst.disabledRepo("locals-only") || rst.members("locals-only") != "internal-a,internal-b" {
		t.Errorf("locals-only: disabled %v members %s: disabled, member kept", rst.disabledRepo("locals-only"), rst.members("locals-only"))
	}
	if o := got["repository.narrowed web/no-claims"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
		t.Errorf("no-claims: %v, want applied", o)
	}
	if rst.disabledRepo("no-claims") || rst.members("no-claims") != "plain-b" {
		t.Errorf("no-claims: disabled %v members %s", rst.disabledRepo("no-claims"), rst.members("no-claims"))
	}
	for _, r := range sum.Results {
		if r.Outcome == journal.Suspended {
			var d struct {
				SuspendedBy string
				Entry       struct{ MembersRemoved []struct{ Key string } }
			}
			if err := json.Unmarshal(r.Detail, &d); err != nil || d.SuspendedBy != "membersRemoved" || len(d.Entry.MembersRemoved) != 1 || !strings.HasPrefix(d.Entry.MembersRemoved[0].Key, "web/internal") {
				t.Errorf("the suspension detail = %s (%v)", r.Detail, err)
			}
		}
	}
	secondPass(t, rst, store, w.tw.at, 3)
}

// TestR_REPO_061_APinRuleIsAddedOnlyWithoutAnOverlap: a pin rule added after t is added to the restored repository when no
// claim in effect and no other rule overlaps its pattern, with the members the restored repository has; otherwise the
// repository is disabled (Q-REPO-249). A rule the restored repository holds is reduced to the journaled members.
func TestR_REPO_061_APinRuleIsAddedOnlyWithoutAnOverlap(t *testing.T) {
	w := newNarrowWorld(t)
	for _, v := range []string{"free", "claimed", "ruled", "held"} {
		w.create("local", "l-"+v, "npm", `"local":{"autoClaim":false}`)
		w.create("remote", "r-"+v, "npm", "")
	}
	w.create("virtual", "free", "npm", `"virtual":{"members":["l-free","r-free"]}`)
	w.create("virtual", "claimed", "npm", `"virtual":{"members":["l-claimed","r-claimed"]}`)
	w.create("virtual", "ruled", "npm", `"virtual":{"members":["l-ruled","r-ruled"],"pinning":{"rules":[{"packages":["@acme/*"],"members":["l-ruled","r-ruled"]}]}}`)
	w.create("virtual", "held", "npm", `"virtual":{"members":["l-held","r-held"],"pinning":{"rules":[{"packages":["@held/*"],"members":["l-held","r-held"]}]}}`)
	w.claims("l-claimed", repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@acme/lib"}}})
	w.snapshot()
	w.patch("free", "virtual", virtualDoc([]string{"l-free", "r-free"}, pinRule([]string{"l-free"}, "@acme/*")))
	w.patch("claimed", "virtual", virtualDoc([]string{"l-claimed", "r-claimed"}, pinRule([]string{"l-claimed"}, "@acme/*")))
	w.patch("ruled", "virtual", virtualDoc([]string{"l-ruled", "r-ruled"}, pinRule([]string{"l-ruled", "r-ruled"}, "@acme/*"), pinRule([]string{"l-ruled"}, "@acme/lib")))
	store := w.ship(3)
	heldID, lHeld := w.src.repoID("held"), w.src.repoID("l-held")
	reduce := forged(t, store, "repository.narrowed", map[string]any{
		"repositoryId": fmt.Sprint(heldID), "key": "web/held",
		"pinsAdded": []map[string]any{{"pattern": "@held/*", "members": []map[string]string{{"repositoryId": fmt.Sprint(lHeld), "key": "web/l-held"}}}},
	})
	w.src.close()

	rst := w.tw.open(w.tw.restored)
	sum, _ := rst.replay(store, w.tw.at, rst.changes)
	got := outcomes(sum)
	if o := got["repository.narrowed web/free"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) || rst.pin("free", "@acme/*") != "l-free" || rst.disabledRepo("free") {
		t.Errorf("free: %v, rule %s, disabled %v: the rule is added with the journaled member", o, rst.pin("free", "@acme/*"), rst.disabledRepo("free"))
	}
	if o := got["repository.narrowed web/claimed"]; !slices.Equal(o, []journal.Outcome{journal.Suspended}) || rst.pin("claimed", "@acme/*") != "-" || !rst.disabledRepo("claimed") {
		t.Errorf("claimed: %v, rule %s, disabled %v: a claim in effect overlaps, so the repository is disabled", o, rst.pin("claimed", "@acme/*"), rst.disabledRepo("claimed"))
	}
	if o := got["repository.narrowed web/ruled"]; !slices.Equal(o, []journal.Outcome{journal.Suspended}) || rst.pin("ruled", "@acme/lib") != "-" || !rst.disabledRepo("ruled") {
		t.Errorf("ruled: %v, rule %s, disabled %v: the pattern overlaps the restored rule @acme/*, so the repository is disabled", o, rst.pin("ruled", "@acme/lib"), rst.disabledRepo("ruled"))
	}
	if outcomeOf(t, sum, reduce) != journal.Applied || rst.pin("held", "@held/*") != "l-held" {
		t.Errorf("held: %s, rule %s: a rule the restored repository holds is reduced to the entry's members", outcomeOf(t, sum, reduce), rst.pin("held", "@held/*"))
	}
	secondPass(t, rst, store, w.tw.at, 4)
}

// TestR_REPO_061_APinMemberRemovedIsRemovedAgain: a member taken out of a pin rule after t is taken out of the restored rule;
// a rule that the restored repository lacks disables it.
func TestR_REPO_061_APinMemberRemovedIsRemovedAgain(t *testing.T) {
	w := newNarrowWorld(t)
	w.create("local", "internal", "npm", `"local":{"autoClaim":false}`)
	w.create("remote", "npmjs", "npm", "")
	w.create("virtual", "all", "npm", `"virtual":{"members":["internal","npmjs"],"pinning":{"rules":[{"packages":["@acme/*"],"members":["internal","npmjs"]}]}}`)
	w.create("virtual", "norule", "npm", `"virtual":{"members":["internal","npmjs"]}`)
	w.snapshot()
	w.patch("all", "virtual", virtualDoc([]string{"internal", "npmjs"}, pinRule([]string{"internal"}, "@acme/*")))
	store := w.ship(1)
	noRuleID, npmjsID := w.src.repoID("norule"), w.src.repoID("npmjs")
	missing := forged(t, store, "repository.narrowed", map[string]any{
		"repositoryId": fmt.Sprint(noRuleID), "key": "web/norule",
		"pinMembersRemoved": []map[string]any{{"pattern": "@acme/*", "members": []map[string]string{{"repositoryId": fmt.Sprint(npmjsID), "key": "web/npmjs"}}}},
	})
	w.src.close()

	rst := w.tw.open(w.tw.restored)
	sum, _ := rst.replay(store, w.tw.at, rst.changes)
	if o := outcomes(sum)["repository.narrowed web/all"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) || rst.pin("all", "@acme/*") != "internal" {
		t.Errorf("all: %v, rule %s, want applied with internal alone", o, rst.pin("all", "@acme/*"))
	}
	if outcomeOf(t, sum, missing) != journal.Suspended || !rst.disabledRepo("norule") || rst.pin("norule", "@acme/*") != "-" {
		t.Errorf("norule: %s, disabled %v, rule %s: no rule holds the pattern, so the repository is disabled", outcomeOf(t, sum, missing), rst.disabledRepo("norule"), rst.pin("norule", "@acme/*"))
	}
	secondPass(t, rst, store, w.tw.at, 2)
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// rescanWorker starts a worker on the side's database that takes only the rescan kind, without the wake-up path (no signals),
// and returns the channel of the repository IDs it ran. This is how the workers of a restored installation meet the row a
// replay handler wrote (Q-HA-180): by polling.
func rescanWorker(t *testing.T, s *twinSide) <-chan int64 {
	t.Helper()
	ran := make(chan int64, 4)
	reg := jobs.NewRegistry()
	spec := vuln.RescanKindSpec()
	spec.Handler = func(_ context.Context, j *jobs.Run) error {
		var a vuln.RescanArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			return err
		}
		ran <- a.RepositoryID
		return nil
	}
	reg.RegisterKind(vuln.KindRescan, spec)
	reg.Freeze()
	w, err := jobs.NewWorker(jobs.WorkerOptions{
		Role: "worker", Instance: "it-restored", UID: newUUID(), Main: s.db.Main(), Control: s.db.ControlPool(),
		Registry: reg, WatchdogTimeout: 30 * time.Second, Log: quietLog, Metrics: prometheus.NewRegistry(),
		Config: config.Jobs{
			Workers:       config.JobsWorkers{Default: 1, Maintenance: 1, Index: 1, Delivery: 1},
			PollInterval:  200 * time.Millisecond,
			LeaseDuration: 6 * time.Second, HeartbeatInterval: 500 * time.Millisecond,
			ScheduleTick: time.Second, Retention: 720 * time.Hour,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		w.Drain(ctx)
		w.Quiesce(ctx)
		_ = w.Stop(ctx)
	})
	return ran
}

// TestR_REPO_061_AStrongerVulnerabilitiesMemberIsReplayedAndARescanIsQueued: a stronger vulnerabilities member set after t is
// the restored member's more blocking value, a rescan is enqueued in the replay's transaction under the repository's dedup
// key, and a worker that finds it by polling runs it (Q-HA-180).
func TestR_REPO_061_AStrongerVulnerabilitiesMemberIsReplayedAndARescanIsQueued(t *testing.T) {
	w := newNarrowWorld(t)
	w.create("remote", "npmjs", "npm", `"vulnerabilities":{"enabled":false,"blockMalicious":false}`)
	w.create("remote", "mirror", "npm", `"vulnerabilities":{"enabled":true,"matchPublicAdvisories":"always","blockMalicious":false}`)
	w.snapshot()
	w.patch("npmjs", "vulnerabilities", map[string]any{"enabled": true, "matchPublicAdvisories": "always", "blockMalicious": true, "onMissingData": "block"})
	// The mirror's member is made weaker in one field and stronger in another.
	w.patch("mirror", "vulnerabilities", map[string]any{"enabled": true, "matchPublicAdvisories": "auto", "blockMalicious": true})
	store := w.ship(2)
	npmjsID, mirrorID := w.src.repoID("npmjs"), w.src.repoID("mirror")
	w.src.close()

	rst := w.tw.open(w.tw.restored)
	if rst.number("SELECT count(*) FROM job WHERE kind = 'vuln-rescan'") != 0 {
		t.Fatal("setup: the restored database holds a rescan")
	}
	sum, _ := rst.replay(store, w.tw.at, rst.changes)
	got := outcomes(sum)
	for _, k := range []string{"npmjs", "mirror"} {
		if o := got["repository.narrowed web/"+k]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
			t.Errorf("%s: %v, want applied", k, o)
		}
	}
	var v map[string]any
	_ = json.Unmarshal([]byte(rst.member("npmjs", "vulnerabilities")), &v)
	if v["enabled"] != true || v["matchPublicAdvisories"] != "always" || v["blockMalicious"] != true || v["onMissingData"] != "block" {
		t.Errorf("npmjs vulnerabilities = %v", v)
	}
	_ = json.Unmarshal([]byte(rst.member("mirror", "vulnerabilities")), &v)
	if v["matchPublicAdvisories"] != "always" || v["blockMalicious"] != true {
		t.Errorf("mirror vulnerabilities = %v: the weaker advisory matching of the journal does not lower the restored value", v)
	}
	t.Run("R-HA-054 a rescan row exists in the restored database, written without a wake-up", func(t *testing.T) {
		key := vuln.RescanKey(npmjsID)
		if n := rst.number("SELECT count(*) FROM job WHERE kind = 'vuln-rescan' AND dedup_key = $1 AND state = 'queued' AND queue = 'default' AND dedup_mode = 'exclusive'", key); n != 1 {
			t.Fatalf("%d queued rescan rows for %s", n, key)
		}
		if n := rst.number("SELECT count(*) FROM job WHERE kind = 'vuln-rescan' AND dedup_key = $1", vuln.RescanKey(mirrorID)); n != 0 {
			t.Errorf("%d rescan rows for the mirror: only blockMalicious moved there", n)
		}
		if n := rst.number("SELECT max_attempts FROM job WHERE dedup_key = $1", key); n != 10 {
			t.Errorf("max_attempts = %d, want the kind's default", n)
		}
	})
	secondPass(t, rst, store, w.tw.at, 2)
	t.Run("R-HA-054 a worker that starts after the restore finds the row by polling", func(t *testing.T) {
		ran := rescanWorker(t, rst)
		select {
		case id := <-ran:
			if id != npmjsID {
				t.Errorf("the worker ran the rescan of %d, want %d", id, npmjsID)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("no worker took the rescan within 30 seconds")
		}
	})
	if n := rst.number("SELECT count(*) FROM job WHERE kind = 'vuln-rescan' AND dedup_key = $1", vuln.RescanKey(npmjsID)); n != 1 {
		t.Errorf("%d rescan rows after the second replay", n)
	}
}

// routingOf is the claim routing of a side, as a replica builds it, for the auto claim of a first publish.
func routingOf(t *testing.T, s *twinSide) (*repository.Routing, *repository.SnapshotReader) {
	t.Helper()
	reg := prometheus.NewRegistry()
	caches, err := cache.NewRegistry(s.changes, reg)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := repository.NewReader(repository.ReaderOptions{
		Runner: s.db, Catalog: catalog.Default(), Caches: caches, Poller: s.changes,
		SafetyTTL: 5 * time.Minute, UnknownKeys: 100, LookupWait: 5 * time.Second, Log: quietLog,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := repository.NewRouting(repository.RoutingOptions{
		Runner: s.db, Reader: reader, Catalog: catalog.Default(), Caches: caches, Notifier: s.changes, Recorder: s.rec,
		SafetyTTL: 5 * time.Minute, Log: quietLog,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt, reader
}

func firstPublish(t *testing.T, s *twinSide, rt *repository.Routing, reader *repository.SnapshotReader, repo, name string) repository.AutoClaimResult {
	t.Helper()
	ctx := context.Background()
	r, err := reader.ByName(ctx, qname.Repo{Project: "web", Key: repo})
	if err != nil {
		t.Fatal(err)
	}
	var res repository.AutoClaimResult
	if err := s.db.InTx(ctx, func(tx meta.Tx) (err error) {
		res, err = rt.AutoClaim(ctx, tx, r, name, repoCaller)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return res
}

// TestR_REPO_061_AnAutoClaimIsCreatedAgain: the auto claim of a first publish after t is created in the restored database
// unless a claim in effect together with it covers the name; a claim widened to global after t is widened again, and a manual
// claim created after t is created (Q-REPO-250).
func TestR_REPO_061_AnAutoClaimIsCreatedAgain(t *testing.T) {
	w := newNarrowWorld(t)
	w.create("local", "internal", "npm", `"local":{"autoClaim":true}`)
	w.create("local", "other", "npm", `"local":{"autoClaim":false}`)
	w.create("remote", "npmjs", "npm", "")
	w.create("virtual", "all", "npm", `"virtual":{"members":["internal","other","npmjs"]}`)
	w.claims("internal", repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@acme/*"}}})
	w.claims("other", repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "taken-*"}}})
	w.snapshot()
	rt, reader := routingOf(t, w.src)
	if res := firstPublish(t, w.src, rt, reader, "internal", "fresh-lib"); !res.Created {
		t.Fatalf("the first publish created no claim: %+v", res)
	}
	w.claims("internal", repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@acme/*", Scope: "global"}, {Pattern: "solo-*"}}})
	store := w.ship(2)
	internalID := w.src.repoID("internal")
	covered := forged(t, store, "repository.claims_changed", map[string]any{
		"repositoryId": fmt.Sprint(internalID), "key": "web/internal", "source": "auto",
		"added": []map[string]string{{"pattern": "taken-lib", "scope": "members"}},
	})
	w.src.close()

	rst := w.tw.open(w.tw.restored)
	version := rst.number("SELECT claims_version FROM repositories WHERE key = 'internal'")
	sum, _ := rst.replay(store, w.tw.at, rst.changes)
	got := outcomes(sum)["repository.claims_changed web/internal"]
	if !slices.Equal(got, []journal.Outcome{journal.Applied, journal.Applied, journal.AlreadyInEffect}) {
		t.Errorf("repository.claims_changed web/internal: %v, want the auto claim and the manual change applied, the covered one in effect", got)
	}
	if list := rst.claimList("internal"); list != "@acme/*:global:manual,fresh-lib:members:auto,solo-*:members:manual" {
		t.Errorf("claims = %s", list)
	}
	if outcomeOf(t, sum, covered) != journal.AlreadyInEffect {
		t.Errorf("an auto claim that another repository's claim covers: %s, want already in effect", outcomeOf(t, sum, covered))
	}
	if rst.text("SELECT COALESCE(created_by::text, 'none') FROM namespace_claims WHERE pattern = 'fresh-lib'") != "none" {
		t.Error("a replayed claim has a creator")
	}
	if rst.number("SELECT claims_version FROM repositories WHERE key = 'internal'") <= version {
		t.Error("claims_version did not rise")
	}
	if rst.disabledRepo("internal") {
		t.Error("the repository was disabled")
	}
	secondPass(t, rst, store, w.tw.at, 3)
}

// TestR_REPO_061_AnOverlappingManualClaimDisablesItsRepository: a manual claim created after t is created in the restored
// database; when it overlaps a claim of another repository that is in effect together with it, its repository is disabled
// (Q-REPO-250).
func TestR_REPO_061_AnOverlappingManualClaimDisablesItsRepository(t *testing.T) {
	w := newNarrowWorld(t)
	for _, k := range []string{"team-a", "team-b", "solo", "elsewhere"} {
		w.create("local", k, "npm", `"local":{"autoClaim":false}`)
	}
	w.create("virtual", "all", "npm", `"virtual":{"members":["team-a","team-b"]}`)
	w.claims("team-b", repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@acme/*"}}})
	w.claims("elsewhere", repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@wide/*", Scope: "global"}}})
	w.snapshot()
	w.claims("team-a", repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@acme/lib"}}})                                        // shares a virtual repository
	w.claims("solo", repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@wide/lib"}}})                                          // a global claim of another repository overlaps
	w.claims("elsewhere", repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@wide/*", Scope: "global"}, {Pattern: "@own/*"}}}) // no overlap
	store := w.ship(3)
	w.src.close()

	rst := w.tw.open(w.tw.restored)
	sum, _ := rst.replay(store, w.tw.at, rst.changes)
	got := outcomes(sum)
	for key, want := range map[string]journal.Outcome{"team-a": journal.Suspended, "solo": journal.Suspended, "elsewhere": journal.Applied} {
		if o := got["repository.claims_changed web/"+key]; !slices.Equal(o, []journal.Outcome{want}) {
			t.Errorf("%s: %v, want %s", key, o, want)
		}
	}
	for key, want := range map[string]bool{"team-a": true, "solo": true, "elsewhere": false} {
		if rst.disabledRepo(key) != want {
			t.Errorf("%s: disabled = %v, want %v", key, rst.disabledRepo(key), want)
		}
	}
	if rst.claimList("team-a") != "@acme/lib:members:manual" || rst.claimList("elsewhere") != "@own/*:members:manual,@wide/*:global:manual" {
		t.Errorf("claims: team-a %s, elsewhere %s: the claim of a suspended repository is created", rst.claimList("team-a"), rst.claimList("elsewhere"))
	}
	t.Run("R-HA-054 an entry for a repository that cannot hold claims", func(t *testing.T) {
		store := newMemStore()
		gone := forged(t, store, "repository.claims_changed", map[string]any{
			"repositoryId": "999999999", "key": "web/ghost", "source": "manual",
			"added": []map[string]string{{"pattern": "@x/*", "scope": "members"}},
		})
		remote := forged(t, store, "repository.claims_changed", map[string]any{
			"repositoryId": fmt.Sprint(rst.repoID("all")), "key": "web/all", "source": "manual",
			"added": []map[string]string{{"pattern": "@x/*", "scope": "members"}},
		})
		bad := forged(t, store, "repository.claims_changed", map[string]any{
			"repositoryId": fmt.Sprint(rst.repoID("solo")), "key": "web/solo", "source": "manual",
			"added": []map[string]string{{"pattern": "a/b/*/c", "scope": "members"}},
		})
		s2, _ := rst.replay(store, w.tw.at, rst.changes)
		for id, want := range map[string]journal.Outcome{gone: journal.SubjectUnknown, remote: journal.SubjectUnknown, bad: journal.Skipped} {
			if got := outcomeOf(t, s2, id); got != want {
				t.Errorf("entry %s: %s, want %s", id, got, want)
			}
		}
	})
	secondPass(t, rst, store, w.tw.at, 3)
}

// failingHook fails the update after the audit events were recorded in its transaction.
type failingHook struct{}

func (failingHook) OnUpdated(context.Context, meta.Tx, repository.Caller, repository.Repository, repository.Repository) error {
	return errors.New("a later step of the transaction failed")
}

func (failingHook) OnDelete(context.Context, meta.Tx, repository.Caller, repository.Repository) error {
	return nil
}

// TestR_REPO_061_ANarrowingUpdateWritesItsEntryInTheSameTransaction: against the real audit writer, the journal row of a
// narrowing is written with the update and rolls back with it. A hook that fails after the events were recorded leaves no
// row of revocation_outbox, no audit event and no change; the same update without the failure leaves exactly one row.
func TestR_REPO_061_ANarrowingUpdateWritesItsEntryInTheSameTransaction(t *testing.T) {
	w := newNarrowWorld(t)
	w.create("remote", "npmjs", "npm", "")
	ctx := context.Background()
	patch := func(svc *repository.Service) error {
		_, err := svc.Patch(ctx, repoCaller, qname.Repo{Project: "web", Key: "npmjs"}, repository.Precondition{Any: true}, setMember("excludePatterns", []string{"@evil/*"}))
		return err
	}
	rows := func() int64 {
		return w.src.number("SELECT count(*) FROM revocation_outbox WHERE event = 'repository.narrowed'")
	}
	t.Run("R-REPO-061 a rollback after the events were recorded leaves no entry", func(t *testing.T) {
		reg := jobs.NewRegistry()
		reg.RegisterKind(repository.DeleteJobKind, repository.DeleteJobSpec(func(context.Context, *jobs.Run) error { return nil }))
		failing, err := repository.NewService(repository.Options{
			Runner: w.src.db, Catalog: catalog.Default(), Recorder: w.src.rec, Notifier: w.src.changes, Hooks: failingHook{},
			Jobs: jobs.NewQueue(jobs.QueueOptions{DB: w.src.db.Main(), Registry: reg, Log: quietLog}), Log: quietLog,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := patch(failing); err == nil {
			t.Fatal("the update with a failing hook succeeded")
		}
		if n := rows(); n != 0 {
			t.Errorf("%d repository.narrowed rows after a rolled-back update", n)
		}
		if n := w.src.number("SELECT count(*) FROM audit_event WHERE action IN ('repository.narrowed', 'repository.updated')"); n != 0 {
			t.Errorf("%d audit events after a rolled-back update", n)
		}
		if got := w.src.member("npmjs", "excludePatterns"); got != "[]" {
			t.Errorf("excludePatterns = %s after a rolled-back update", got)
		}
	})
	t.Run("R-REPO-061 the update without the failure leaves its entry in the same transaction", func(t *testing.T) {
		if err := patch(w.svc); err != nil {
			t.Fatal(err)
		}
		if n := rows(); n != 1 {
			t.Fatalf("%d repository.narrowed rows, want 1", n)
		}
		if a, b := w.src.text("SELECT id FROM revocation_outbox WHERE event = 'repository.narrowed'"),
			w.src.text("SELECT id::text FROM audit_event WHERE action = 'repository.narrowed'"); a != b {
			t.Errorf("the outbox row %s and the audit event %s differ", a, b)
		}
	})
}

// nulHandler is a replay handler that passes a text of its entry to SQL unchecked, as a handler that missed a check would.
func init() {
	journal.Register(journal.Handler{
		Domain: "refusetest", Events: []string{"refusetest.exec"},
		Apply: func(ctx context.Context, tx meta.Tx, _ journal.Env, o journal.Object) (journal.Result, error) {
			var e struct{ Text, SQL string }
			if !o.Into(&e) {
				return o.Malformed("no text"), nil
			}
			ptx, ok := postgres.Unwrap(tx)
			if !ok {
				return journal.Result{}, errors.New("not a PostgreSQL transaction")
			}
			stmt, args := "SELECT $1::text::jsonb", []any{e.Text}
			if e.SQL != "" {
				stmt, args = e.SQL, nil
			}
			if _, err := ptx.Exec(ctx, stmt, args...); err != nil {
				return journal.Result{}, err
			}
			return o.Result(journal.Applied, ""), nil
		},
	})
}

// TestR_HA_054_AnEntryTheDatabaseRefusesDoesNotStopTheReplay: an entry with a value the database refuses (a NUL byte that a
// handler passes to SQL) is rolled back, reported skipped with its subject quoted, and the entries after it are still replayed.
// A second replay reports the same.
func TestR_HA_054_AnEntryTheDatabaseRefusesDoesNotStopTheReplay(t *testing.T) {
	w := newNarrowWorld(t)
	w.create("remote", "npmjs", "npm", "")
	w.snapshot()
	w.patch("npmjs", "excludePatterns", []string{"@evil/*"})
	store := w.ship(1)
	bad := forged(t, store, "refusetest.exec", map[string]any{"Text": "{\"a\": \"b\u0000\"}", "key": "web/ali\nce \x00"})
	w.src.close()

	rst := w.tw.open(w.tw.restored)
	sum, printed := rst.replay(store, w.tw.at, rst.changes)
	if got := outcomeOf(t, sum, bad); got != journal.Skipped {
		t.Errorf("the refused entry: %s, want skipped", got)
	}
	if o := outcomes(sum)["repository.narrowed web/npmjs"]; !slices.Equal(o, []journal.Outcome{journal.Applied}) {
		t.Errorf("the entry after it: %v, want applied", o)
	}
	if !strings.Contains(printed, `"refusetest.exec" "web/ali\nce \x00" `) {
		t.Errorf("the refused entry's subject is not printed quoted:\n%s", printed)
	}
	again, _ := rst.replay(store, w.tw.at, rst.changes)
	if outcomeOf(t, again, bad) != journal.Skipped || again.Counts[journal.Applied] != 0 {
		t.Errorf("second replay: %v", again.Counts)
	}

	t.Run("R-HA-054 only the errors a forged value can cause are skipped", func(t *testing.T) {
		notNull := forged(t, store, "refusetest.exec", map[string]any{"SQL": "INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), NULL, 'x')"})
		divide := forged(t, store, "refusetest.exec", map[string]any{"SQL": "SELECT 1/0"})
		again, _ := rst.replay(store, w.tw.at, rst.changes)
		for name, id := range map[string]string{"not_null_violation": notNull, "division_by_zero": divide} {
			if got := outcomeOf(t, again, id); got != journal.Skipped {
				t.Errorf("%s: %s, want skipped", name, got)
			}
		}
		// A unique violation is a handler that disagrees with the schema: the replay stops with the error.
		dup := newMemStore()
		forged(t, dup, "refusetest.exec", map[string]any{"SQL": "INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'web', 'x')"})
		r := replay.New(replay.Options{DB: rst.db, Reader: replay.NewReader(dup, twinPrefix), Notifier: rst.changes, Log: testenv.Quiet()})
		if _, err := r.Replay(context.Background(), journal.ReplayOptions{RestoredTo: w.tw.at, Margin: 5 * time.Minute, Out: io.Discard}); err == nil {
			t.Error("a unique violation was skipped: it must stop the replay")
		}
	})
}

// TestR_REPO_061_AForgedAutoClaimIsAnExactNameOfScopeMembers: an auto claim that is a wildcard pattern, a claim of scope global,
// and a claim list that misses its added member are refused. The limit of 1000 claims does not stop a replayed claim, manual or
// auto: a dropped claim would leave a name unclaimed, and the replay warns about a repository above the limit.
func TestR_REPO_061_AForgedAutoClaimIsAnExactNameOfScopeMembers(t *testing.T) {
	w := newNarrowWorld(t)
	w.create("local", "internal", "npm", `"local":{"autoClaim":false}`)
	w.create("local", "full", "npm", `"local":{"autoClaim":false}`)
	var claims []repository.ClaimEntry
	for i := 0; i < repository.MaxClaims; i++ {
		claims = append(claims, repository.ClaimEntry{Pattern: fmt.Sprintf("pkg-%04d", i)})
	}
	w.claims("full", repository.ClaimInput{Claims: claims})
	w.snapshot()
	internalID, fullID := w.src.repoID("internal"), w.src.repoID("full")
	store := newMemStore()
	entry := func(id int64, key, source string, added any) map[string]any {
		m := map[string]any{"repositoryId": fmt.Sprint(id), "key": key, "source": source}
		if added != nil {
			m["added"] = added
		}
		return m
	}
	one := func(p, scope string) []map[string]string { return []map[string]string{{"pattern": p, "scope": scope}} }
	wild := forged(t, store, "repository.claims_changed", entry(internalID, "web/internal", "auto", one("@evil/*", "members")))
	global := forged(t, store, "repository.claims_changed", entry(internalID, "web/internal", "auto", one("evil-lib", "global")))
	noAdded := forged(t, store, "repository.claims_changed", entry(internalID, "web/internal", "manual", nil))
	over := forged(t, store, "repository.claims_changed", entry(fullID, "web/full", "manual", one("one-more", "members")))
	autoOver := forged(t, store, "repository.claims_changed", entry(fullID, "web/full", "auto", one("auto-more", "members")))
	w.src.close()

	rst := w.tw.open(w.tw.restored)
	sum, printed := rst.replay(store, w.tw.at, rst.changes)
	for name, tc := range map[string]struct {
		id   string
		want journal.Outcome
	}{
		"a wildcard auto claim": {wild, journal.Skipped}, "a global auto claim": {global, journal.Skipped}, "no added member": {noAdded, journal.Skipped},
		"a manual claim past the limit": {over, journal.Applied}, "an auto claim past the limit": {autoOver, journal.Applied},
	} {
		if got := outcomeOf(t, sum, tc.id); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
	if rst.claimList("internal") != "" || rst.disabledRepo("full") || rst.number("SELECT count(*) FROM namespace_claims WHERE repo_id = $1", fullID) != int64(repository.MaxClaims)+2 {
		t.Errorf("claims of internal %q, full disabled %v: both claims are created past the limit", rst.claimList("internal"), rst.disabledRepo("full"))
	}
	if !strings.Contains(printed, "above the limit of") || len(sum.Warnings) == 0 {
		t.Errorf("no warning for a repository above the limit:\n%s", printed)
	}
}
