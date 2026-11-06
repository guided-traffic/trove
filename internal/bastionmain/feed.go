package bastionmain

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Reader is what the feed needs of the database: reads with the failover retry. *postgres.DB implements it.
type Reader interface {
	Read(ctx context.Context, fn func(q.DBTX) error) error
}

// nilUUID is the cursor ID of a synthetic cursor: it sorts before every event of that instant.
const nilUUID = "00000000-0000-0000-0000-000000000000"

// behindFactor bounds the entries behind the cursor at this multiple of the page size; more means the Bastion
// has to start again (Q-BAS-49 item 3).
const behindFactor = 4

// The audit actions of the project model that the feed reads. They are names, not constants of the audit package:
// the registry of the audit package stays the one list, and TestR_BAS_015_EveryJournaledEventIsClassified holds
// these to it.
const (
	eventRepositoryMoved      = "repository.moved"
	eventProjectMemberRemoved = "authz.project.member_removed"
	eventProjectMemberChanged = "authz.project.member_changed"
)

// feedActions are the audit actions that can yield an entry (R-BAS-015). The registry of the audit package
// stays the one list of journaled actions; this is the subset the mapping reads, passed to the query as a text
// array. An action in the journal that is not here is in noFeedEntry with the reason it yields no entry.
func feedActions() []string {
	return []string{
		audit.EventTokenRevoked, audit.EventCITokenRevoked, audit.EventTokenRotated,
		audit.EventUserDisabled, audit.EventUserDeleted, audit.EventSADisabled, audit.EventSADeleted,
		audit.EventGroupMembersChanged, audit.EventUserGroupsSynced, "authz.role.unbound",
		audit.EventGroupDeleted, "authz.policy.deleted", "authz.policy.updated", "authz.settings.changed",
		audit.EventCIIssuerUpdated, audit.EventCIIssuerDisabled, audit.EventCIIssuerDeleted,
		audit.EventCIMappingUpdated, audit.EventCIMappingDisabled, audit.EventCIMappingDeleted,
		"endpoint.exposure.updated", "endpoint.exposure.deleted",
		audit.EventSettingsAuthChanged, eventRepositoryMoved, eventProjectMemberRemoved, eventProjectMemberChanged,
		audit.EventNPMUnpublish, audit.EventPackageDeleted,
		audit.EventVulnFindingStatusChanged, audit.EventVulnFindingReopened,
	}
}

// FeedActions returns the audit actions the feed reads, for the tests and the documentation.
func FeedActions() []string { return feedActions() }

// noFeedEntry are the events the revocation journal copies that yield no entry, each with the reason it narrows
// nothing a Bastion keeps (R-BAS-015, Q-BAS-120). Every journaled event of the audit registry is either read by
// the feed or listed here; TestR_BAS_015_EveryJournaledEventIsClassified holds the two lists to the registry.
func noFeedEntry() map[string]string {
	return map[string]string{
		"authz.ownership.removed":   "bounds who may publish a package name (R-AUTHZ-018); a read is never decided by ownership",
		"authz.ownership.released":  "bounds who may publish a package name (R-AUTHZ-018); a read is never decided by ownership",
		audit.EventPackagePublished: "reserves the version numbers a publish took (R-HA-054); a Bastion never publishes",
		audit.EventProjectUpdated:   "a project's name and description decide no read",
		audit.EventProjectDeleted:   "a project is deleted only when it holds no repository and no service account, and its policies are deleted before it, each journaled as authz.policy.deleted (grants)",
	}
}

// NoFeedEntry returns the journaled events that yield no entry, with their reasons, for the tests and the
// documentation.
func NoFeedEntry() map[string]string { return noFeedEntry() }

// FeedOptions configure [NewFeed].
type FeedOptions struct {
	DB Reader
	// Lookback and MaxPage are bastions.feed.lookback and bastions.feed.max_page.
	Lookback time.Duration
	MaxPage  int
	// Endpoint is the name of the default endpoint, the one endpoint whose exposure change reaches a Bastion
	// ("default" when empty).
	Endpoint string
	Log      *slog.Logger
}

// Feed is the revocation feed of the main installation (R-BAS-015): it maps the journaled audit events to the
// entries of the interface and pages through them. It holds no state.
type Feed struct {
	o FeedOptions
}

// NewFeed returns the feed.
func NewFeed(o FeedOptions) *Feed {
	if o.Endpoint == "" {
		o.Endpoint = "default"
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Feed{o: o}
}

// FeedEvent is a stored audit event as the mapping reads it.
type FeedEvent struct {
	ID           string
	At           time.Time
	Action       string
	ResourceType string
	ResourceID   string
	Repository   string
	Details      []byte
}

// rawEntry is an entry before the lookups: the repositories of a content entry are still the event's own.
type rawEntry struct {
	kind       iface.FeedKind
	tokens     []string
	principals []string
	repository string
	// names, with kind principal, are member names that still need their IDs.
	names []string
}

// detailsDoc is the part of an event's details the mapping reads; a document that does not read leaves the
// members empty, and the fallbacks of [mapEvent] apply.
type detailsDoc struct {
	Predecessor string          `json:"predecessor"`
	TokenIDs    []string        `json:"tokenIds"`
	Principal   string          `json:"principal"`
	PrincipalID string          `json:"principalId"`
	Endpoint    string          `json:"endpoint"`
	Removed     json.RawMessage `json:"removed"`
	// Block is "set" in the details of a finding event that set a download block (R-VULN-013).
	Block  string `json:"block"`
	Before *struct {
		AnonymousAccess *bool `json:"anonymousAccess"`
	} `json:"before"`
	After *struct {
		AnonymousAccess *bool `json:"anonymousAccess"`
	} `json:"after"`
}

// mapEvent maps one audit event to the entries it yields (the table of R-BAS-015): none for an event that
// bounds writes or only widens access, one for most, two for settings.auth.changed that switched anonymous
// access off. An event whose target cannot be read yields a grants entry, which drops every answer: the
// conservative reading never keeps an answer the event may have narrowed. The one exception is a withdrawal
// without a repository, which yields none (the caller logs it).
func mapEvent(ev FeedEvent, defaultEndpoint string) []rawEntry {
	var d detailsDoc
	if len(ev.Details) > 0 {
		_ = json.Unmarshal(ev.Details, &d)
	}
	grants := []rawEntry{{kind: iface.FeedGrants}}
	switch ev.Action {
	case audit.EventTokenRevoked:
		return tokenEntry(grants, ev.ResourceID)
	case audit.EventTokenRotated:
		return tokenEntry(grants, d.Predecessor)
	case audit.EventCITokenRevoked:
		return tokenEntry(grants, d.TokenIDs...)
	case audit.EventUserDisabled, audit.EventUserDeleted, audit.EventSADisabled, audit.EventSADeleted:
		if ev.ResourceID == "" {
			return grants
		}
		return []rawEntry{{kind: iface.FeedPrincipal, principals: []string{ev.ResourceID}}}
	case audit.EventUserGroupsSynced:
		// The subject is the user whose groups were synchronized; additions only widen.
		if !hasEntries(d.Removed) {
			return nil
		}
		if ev.ResourceID == "" {
			return grants
		}
		return []rawEntry{{kind: iface.FeedPrincipal, principals: []string{ev.ResourceID}}}
	case audit.EventGroupMembersChanged:
		members := removedMembers(d.Removed)
		if len(members) == 0 {
			return nil
		}
		return []rawEntry{{kind: iface.FeedPrincipal, names: members}}
	case "authz.role.unbound":
		// A user or service account is named by ID; a group, a CI identity and the built-in principals
		// reach callers the entry cannot list.
		if (strings.HasPrefix(d.Principal, "user:") || strings.HasPrefix(d.Principal, "sa:")) && d.PrincipalID != "" {
			return []rawEntry{{kind: iface.FeedPrincipal, principals: []string{d.PrincipalID}}}
		}
		return grants
	case audit.EventGroupDeleted, "authz.policy.deleted", "authz.policy.updated", "authz.settings.changed",
		audit.EventCIIssuerUpdated, audit.EventCIIssuerDisabled, audit.EventCIIssuerDeleted,
		audit.EventCIMappingUpdated, audit.EventCIMappingDisabled, audit.EventCIMappingDeleted:
		return grants
	case eventRepositoryMoved:
		// The repository leaves its project's roles and policies behind and changes its address (R-REPO-060): any
		// caller's read of it, and of every virtual repository over it, may have narrowed. A content entry drops
		// only the URL answers of a repository, not its shape answers, so only grants is exact enough (Q-BAS-120).
		return grants
	case eventProjectMemberRemoved, eventProjectMemberChanged:
		// A member's role grants reads on every repository of the project (R-AUTHZ-026): a removal or a lower role
		// narrows every answer of that member, and the event names the project, not the repositories.
		return grants
	case "endpoint.exposure.updated", "endpoint.exposure.deleted":
		// Only the exposure of the default endpoint reaches a Bastion, which connects to it (Q-BAS-4).
		if d.Endpoint != "" && d.Endpoint != defaultEndpoint {
			return nil
		}
		return grants
	case audit.EventSettingsAuthChanged:
		out := []rawEntry{{kind: iface.FeedSettings}}
		if d.After != nil && d.After.AnonymousAccess != nil && !*d.After.AnonymousAccess &&
			(d.Before == nil || d.Before.AnonymousAccess == nil || *d.Before.AnonymousAccess) {
			out = append(out, rawEntry{kind: iface.FeedGrants})
		}
		return out
	case audit.EventNPMUnpublish, audit.EventPackageDeleted:
		if ev.Repository == "" {
			return nil
		}
		return []rawEntry{{kind: iface.FeedContent, repository: ev.Repository}}
	case audit.EventVulnFindingStatusChanged, audit.EventVulnFindingReopened:
		// A finding event that set a block withdraws a version a Bastion may hold (R-VULN-015); one that lifted a block or
		// touched none narrows nothing. The entry is the content of the finding's repository and of the virtual ones above it.
		if d.Block != "set" || ev.Repository == "" {
			return nil
		}
		return []rawEntry{{kind: iface.FeedContent, repository: ev.Repository}}
	}
	return nil
}

func tokenEntry(fallback []rawEntry, ids ...string) []rawEntry {
	ids = slices.DeleteFunc(slices.Clone(ids), func(s string) bool { return s == "" })
	if len(ids) == 0 {
		return fallback
	}
	return []rawEntry{{kind: iface.FeedToken, tokens: ids}}
}

func hasEntries(raw json.RawMessage) bool {
	var a []json.RawMessage
	return json.Unmarshal(raw, &a) == nil && len(a) > 0
}

// removedMembers reads the member names of the removed list of group.members.changed.
func removedMembers(raw json.RawMessage) []string {
	var refs []struct {
		Member string `json:"member"`
	}
	if json.Unmarshal(raw, &refs) != nil {
		return nil
	}
	var out []string
	for _, r := range refs {
		if r.Member != "" && !slices.Contains(out, r.Member) {
			out = append(out, r.Member)
		}
	}
	return out
}

// Position returns the feed position of the handshake (R-BAS-010): the newest journaled event within the
// lookback, or the database clock as a synthetic cursor when there is none. A Bastion that starts from it
// misses nothing: the lookback re-sends the entries of a transaction that commits late.
func (f *Feed) Position(ctx context.Context) (string, error) {
	var out string
	err := f.o.DB.Read(ctx, func(db q.DBTX) error {
		qs := q.New(db)
		row, err := qs.BastionFeedNow(ctx, q.BastionFeedNowParams{WindowSecs: f.o.Lookback.Seconds(), Actions: feedActions()})
		if err == nil {
			out = iface.Cursor{At: row.OccurredAt.UTC(), ID: row.ID}.String()
			return nil
		}
		if !postgres.IsNoRows(err) {
			return err
		}
		st, err := qs.BastionFeedCursorState(ctx, q.BastionFeedCursorStateParams{CursorAt: time.Unix(0, 0).UTC().Format(time.RFC3339Nano), CursorID: nilUUID})
		if err != nil {
			return err
		}
		out = iface.Cursor{At: time.UnixMicro(st.DbNowUs).UTC()}.String()
		return nil
	})
	return out, err
}

// Read answers one feed request (R-BAS-015): after is the cursor of an earlier answer or a synthetic "t:…", limit
// the page size (0 or above bastions.feed.max_page means the maximum). A cursor that does not parse is a
// 400 invalid_cursor.
func (f *Feed) Read(ctx context.Context, after string, limit int) (iface.FeedAnswer, error) {
	cur, err := iface.ParseCursor(after)
	if err != nil {
		return iface.FeedAnswer{}, errs.E(errs.Invalid, errs.CodeInvalidCursor, "The cursor is not valid. Start again with the cursor of the handshake.", errs.WithCause(err))
	}
	if limit <= 0 || limit > f.o.MaxPage {
		limit = f.o.MaxPage
	}
	var ans iface.FeedAnswer
	err = f.o.DB.Read(ctx, func(db q.DBTX) error {
		ans = iface.FeedAnswer{Entries: []iface.FeedEntry{}, Cursor: after}
		return f.read(ctx, q.New(db), cur, limit, &ans)
	})
	if err != nil {
		return iface.FeedAnswer{}, err
	}
	return ans, nil
}

func (f *Feed) read(ctx context.Context, qs *q.Queries, cur iface.Cursor, limit int, ans *iface.FeedAnswer) error {
	cursorID := cur.ID
	if cur.Synthetic() {
		cursorID = nilUUID
	}
	cursorAt := cur.At.UTC().Format(time.RFC3339Nano)
	st, err := qs.BastionFeedCursorState(ctx, q.BastionFeedCursorStateParams{CursorAt: cursorAt, CursorID: cursorID})
	if err != nil {
		return err
	}
	now := time.UnixMicro(st.DbNowUs).UTC()
	if f.mustReset(cur, st, now) {
		return f.reset(ctx, qs, ans)
	}
	behindMax := behindFactor * f.o.MaxPage
	behind, err := qs.BastionFeedBehind(ctx, q.BastionFeedBehindParams{
		CursorAt: cursorAt, LookbackSecs: f.o.Lookback.Seconds(), CursorID: cursorID, Actions: feedActions(), MaxRows: int32(behindMax + 1), //nolint:gosec // bounded by the configuration range
	})
	if err != nil {
		return err
	}
	if len(behind) > behindMax {
		return f.reset(ctx, qs, ans)
	}
	ahead, err := qs.BastionFeedAhead(ctx, q.BastionFeedAheadParams{
		CursorAt: cursorAt, CursorID: cursorID, Actions: feedActions(), MaxRows: int32(limit + 1), //nolint:gosec // bounded by the configuration range
	})
	if err != nil {
		return err
	}
	events := make([]FeedEvent, 0, len(behind)+len(ahead))
	for _, r := range behind {
		events = append(events, FeedEvent{ID: r.ID, At: r.OccurredAt.UTC(), Action: r.Action, ResourceType: r.ResourceType, ResourceID: r.ResourceID, Repository: r.Repository, Details: r.Details})
	}
	nBehind := len(events)
	for _, r := range ahead {
		events = append(events, FeedEvent{ID: r.ID, At: r.OccurredAt.UTC(), Action: r.Action, ResourceType: r.ResourceType, ResourceID: r.ResourceID, Repository: r.Repository, Details: r.Details})
	}
	mapped := make([][]rawEntry, len(events))
	for i, ev := range events {
		mapped[i] = mapEvent(ev, f.o.Endpoint)
		if i >= nBehind && len(mapped[i]) == 0 && (ev.Action == audit.EventNPMUnpublish || ev.Action == audit.EventPackageDeleted) {
			f.o.Log.Warn("a withdrawal event names no repository; it yields no feed entry", "event_id", ev.ID, "action", ev.Action)
		}
	}
	look, err := f.lookups(ctx, qs, mapped)
	if err != nil {
		return err
	}
	// Entries behind the cursor are all sent; the page after it is cut at event boundaries, so the cursor
	// never names an event whose second entry was left out. An event that yields no entry still advances
	// the cursor.
	last, page := -1, 0
	for i := range events {
		es := mapped[i]
		if i >= nBehind {
			if i-nBehind >= limit || (page > 0 && page+len(es) > limit) {
				ans.More = true
				break
			}
			page += len(es)
			last = i
		}
		ans.Entries = append(ans.Entries, look.entries(events[i], es)...)
	}
	if last >= 0 {
		ans.Cursor = iface.Cursor{At: events[last].At, ID: events[last].ID}.String()
	}
	return nil
}

// mustReset applies the three conditions of a reset (R-BAS-015): the event the cursor names no longer exists
// although its time lies within the audit retention (a point-in-time restore, a failover that lost commits), the
// cursor lies in the future beyond the lookback, or it lies before the retention.
func (f *Feed) mustReset(cur iface.Cursor, st q.BastionFeedCursorStateRow, now time.Time) bool {
	if cur.At.After(now.Add(f.o.Lookback)) {
		return true
	}
	if st.RetentionDays > 0 && cur.At.Before(now.Add(-time.Duration(st.RetentionDays)*24*time.Hour)) {
		return true
	}
	return !cur.Synthetic() && !st.CursorExists
}

// reset answers a feed whose cursor is no longer valid: no entries, the position of now, reset true.
func (f *Feed) reset(ctx context.Context, qs *q.Queries, ans *iface.FeedAnswer) error {
	row, err := qs.BastionFeedNow(ctx, q.BastionFeedNowParams{WindowSecs: f.o.Lookback.Seconds(), Actions: feedActions()})
	switch {
	case err == nil:
		ans.Cursor = iface.Cursor{At: row.OccurredAt.UTC(), ID: row.ID}.String()
	case postgres.IsNoRows(err):
		st, serr := qs.BastionFeedCursorState(ctx, q.BastionFeedCursorStateParams{CursorAt: time.Unix(0, 0).UTC().Format(time.RFC3339Nano), CursorID: nilUUID})
		if serr != nil {
			return serr
		}
		ans.Cursor = iface.Cursor{At: time.UnixMicro(st.DbNowUs).UTC()}.String()
	default:
		return err
	}
	ans.Entries, ans.More, ans.Reset = []iface.FeedEntry{}, false, true
	return nil
}

// lookups holds the results of the queries the mapped entries need: the virtual closure of every repository
// named by a content entry and the IDs of the members named by a membership event.
type lookups struct {
	closure map[string][]string
	ids     map[string][]string
}

func (f *Feed) lookups(ctx context.Context, qs *q.Queries, mapped [][]rawEntry) (lookups, error) {
	l := lookups{closure: map[string][]string{}, ids: map[string][]string{}}
	var repos, names []string
	for _, es := range mapped {
		for _, e := range es {
			if e.repository != "" && !slices.Contains(repos, e.repository) {
				repos = append(repos, e.repository)
			}
			for _, n := range e.names {
				if n = memberName(n); !slices.Contains(names, n) {
					names = append(names, n)
				}
			}
		}
	}
	for _, name := range repos {
		members, err := qs.BastionVirtualClosure(ctx, []string{name})
		if err != nil {
			return l, err
		}
		// The repository first, then every virtual repository that includes it, all by their qualified names; an
		// unknown name (the repository was deleted) is reported alone.
		out := []string{name}
		for _, m := range members {
			if m != name {
				out = append(out, m)
			}
		}
		l.closure[name] = out
	}
	if len(names) > 0 {
		rows, err := qs.BastionPrincipalIDsByName(ctx, names)
		if err != nil {
			return l, err
		}
		for _, r := range rows {
			l.ids[r.Name] = append(l.ids[r.Name], r.ID)
		}
	}
	return l, nil
}

// memberName is the name a member reference of a membership event has in the principal table: a service account
// is referenced as "sa:<name>", a user by its name, which holds no colon.
func memberName(ref string) string { return strings.TrimPrefix(ref, "sa:") }

// entries builds the wire entries of one event: the first has the event's ID, the others "<ID>:1", "<ID>:2".
func (l lookups) entries(ev FeedEvent, es []rawEntry) []iface.FeedEntry {
	var out []iface.FeedEntry
	for _, e := range es {
		fe := iface.FeedEntry{ID: ev.ID, At: ev.At, Kind: e.kind}
		if n := len(out); n > 0 {
			fe.ID = ev.ID + ":" + strconv.Itoa(n)
		}
		switch e.kind {
		case iface.FeedToken:
			fe.Tokens = e.tokens
		case iface.FeedPrincipal:
			fe.Principals = e.principals
			if len(e.names) > 0 {
				for _, n := range e.names {
					for _, id := range l.ids[memberName(n)] {
						if !slices.Contains(fe.Principals, id) {
							fe.Principals = append(fe.Principals, id)
						}
					}
				}
				if len(fe.Principals) == 0 {
					// Members that cannot be named by ID any more: every answer is dropped instead.
					fe.Kind = iface.FeedGrants
				}
			}
		case iface.FeedContent:
			fe.Repositories = l.closure[e.repository]
		}
		out = append(out, fe)
	}
	return out
}
