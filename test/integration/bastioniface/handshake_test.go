//go:build integration

package bastioniface

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

func handshake(t *testing.T, s *site, bst string, extra ...string) (iface.Handshake, reply) {
	t.Helper()
	rp := s.iface(bst, http.MethodGet, iface.PathHandshake, nil, extra...)
	var h iface.Handshake
	if rp.Status == http.StatusOK {
		rp.decode(t, &h)
	}
	return h, rp
}

// TestR_BAS_010_TheHandshakeAnswersWhatTheBastionNeeds: the members of the answer, with the effective anonymous
// switch following the runtime setting and the download switch following audit.record_downloads.
func TestR_BAS_010_TheHandshakeAnswersWhatTheBastionNeeds(t *testing.T) {
	e := newEnv(t)
	s := e.newSite(siteOpts{endpointAnonymous: true, recordDownloads: true})
	bid := e.registration("partners")
	bst := s.registered("partners", bid)

	h, rp := handshake(t, s, bst)
	if rp.Status != http.StatusOK {
		t.Fatalf("%d %s", rp.Status, rp.Body)
	}
	if got := rp.Header.Get(iface.HeaderInterface); got != strconv.Itoa(iface.InterfaceVersion) {
		t.Errorf("%s = %q", iface.HeaderInterface, got)
	}
	if rp.Header.Get("Content-Type") != "application/json" || rp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", rp.Header)
	}
	tok, _ := s.bastion.Authenticate(context.Background(), bst)
	if h.InterfaceVersion != iface.InterfaceVersion || h.Bastion.Name != "partners" || h.Bastion.TokenID != tok.TokenID || !h.Bastion.TokenExpiresAt.Equal(tok.ExpiresAt) {
		t.Errorf("identity = %+v", h)
	}
	if h.Installation.BaseURL != baseURL || h.Installation.Release != "0.14.0" || h.ResponseBaseURL != baseURL {
		t.Errorf("installation = %+v, responseBaseUrl %q", h.Installation, h.ResponseBaseURL)
	}
	if !h.RecordDownloads || h.AnonymousAccess {
		t.Errorf("recordDownloads %v anonymousAccess %v (the runtime setting is off by default)", h.RecordDownloads, h.AnonymousAccess)
	}
	if d := time.Since(h.ServerTime); d < -5*time.Second || d > 5*time.Second {
		t.Errorf("serverTime = %v", h.ServerTime)
	}
	if _, err := iface.ParseCursor(h.Feed.Cursor); err != nil {
		t.Errorf("feed cursor %q: %v", h.Feed.Cursor, err)
	}

	on := true
	if _, err := s.setts.Update(context.Background(), admin, token.SettingsPatch{AnonymousAccess: &on, Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the runtime switch reaches the handshake", 10*time.Second, func() bool { h, _ := handshake(t, s, bst); return h.AnonymousAccess })
	// The endpoint's own rule narrows the switch: anonymous requests of an endpoint that admits none are refused
	// whatever the runtime setting says (R-AUTH-037, R-EP-010).
	s.rules.anonymous(false)
	if h, _ := handshake(t, s, bst); h.AnonymousAccess {
		t.Error("the handshake reports anonymous access for an endpoint that admits none")
	}
}

func TestR_BAS_010_TheDownloadSwitchIsReported(t *testing.T) {
	e := newEnv(t)
	s := e.newSite(siteOpts{recordDownloads: false})
	bst := s.registered("partners", e.registration("partners"))
	if h, _ := handshake(t, s, bst); h.RecordDownloads {
		t.Error("recordDownloads with audit.record_downloads off")
	}
}

// TestR_BAS_010_TheResponseBaseURLIsTheOneDerivedForTheRequest: the Bastion's forwarded host selects its base URL
// when it is an entry of server.additional_base_urls, with either value of server.forwarded_header; without the
// entry the answer is the main installation's own base URL, which is how the Bastion finds out (R-BAS-010).
func TestR_BAS_010_TheResponseBaseURLIsTheOneDerivedForTheRequest(t *testing.T) {
	forwarded := func(host string) []string {
		return []string{
			"X-Forwarded-Proto", "https", "X-Forwarded-Host", host, "X-Forwarded-Port", "443",
			"Forwarded", "proto=https;host=" + host,
		}
	}
	for _, family := range []httpserver.ForwardedHeader{httpserver.HeaderXForwardedFor, httpserver.HeaderForwarded} {
		t.Run(string(family), func(t *testing.T) {
			e := newEnv(t)
			with := e.newSite(siteOpts{forwarded: family, additional: []string{"https://packages.example.com"}})
			without := e.newSite(siteOpts{forwarded: family})
			bid := e.registration("partners")
			bstWith, bstWithout := with.registered("partners", bid), without.registered("partners", bid)

			if h, _ := handshake(t, with, bstWith, forwarded("packages.example.com")...); h.ResponseBaseURL != "https://packages.example.com" {
				t.Errorf("with the entry: responseBaseUrl = %q", h.ResponseBaseURL)
			}
			if h, _ := handshake(t, without, bstWithout, forwarded("packages.example.com")...); h.ResponseBaseURL != baseURL {
				t.Errorf("missing from additional_base_urls: responseBaseUrl = %q, want the main installation's %q", h.ResponseBaseURL, baseURL)
			}
			if h, _ := handshake(t, with, bstWith, forwarded("evil.example.net")...); h.ResponseBaseURL != baseURL {
				t.Errorf("a host that is no entry: responseBaseUrl = %q", h.ResponseBaseURL)
			}
		})
	}
}

type contactRow struct {
	replica, release, baseURL, addr string
	version                         int
	at                              time.Time
}

func contacts(t *testing.T, e *env, bid string) map[string]contactRow {
	t.Helper()
	rows, err := e.admin.Query(context.Background(),
		"SELECT replica, release, base_url, coalesce(host(client_address), ''), interface_version, contact_at FROM bastion_contact WHERE bastion_id = $1::uuid", bid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]contactRow{}
	for rows.Next() {
		var c contactRow
		if err := rows.Scan(&c.replica, &c.release, &c.baseURL, &c.addr, &c.version, &c.at); err != nil {
			t.Fatal(err)
		}
		out[c.replica] = c
	}
	return out
}

func contactEvents(s *site) int { return len(s.rec.named(audit.EventBastionContact)) }

// TestR_BAS_010_TheLastContactIsRecordedAtMostOncePerMinuteAndAnEventFollowsAChange: the contact row holds the
// time, the client address, the release from the User-Agent, the interface version, the base URL and the replica;
// bastion.contact is written when a reported value changes and not for a repeat.
func TestR_BAS_010_TheLastContactIsRecordedAtMostOncePerMinuteAndAnEventFollowsAChange(t *testing.T) {
	e := newEnv(t)
	s := e.newSite(siteOpts{additional: []string{"https://packages.example.com"}})
	bid := e.registration("partners")
	bst := s.registered("partners", bid)
	host := []string{"X-Forwarded-Proto", "https", "X-Forwarded-Host", "packages.example.com", "X-Forwarded-Port", "443"}

	handshake(t, s, bst, host...)
	got := contacts(t, e, bid)["trove-bastion-7d9c-x2k4q"]
	if got.release != "0.14.0" || got.version != iface.InterfaceVersion || got.baseURL != "https://packages.example.com" || got.addr != "127.0.0.1" {
		t.Fatalf("contact = %+v", got)
	}
	if time.Since(got.at) > 30*time.Second {
		t.Errorf("contact_at = %v", got.at)
	}
	waitFor(t, "bastion.contact for the first contact", 10*time.Second, func() bool { return contactEvents(s) == 1 })
	ev := s.rec.named(audit.EventBastionContact)[0]
	if ev.Actor.Kind != "bastion" || ev.Actor.Name != "partners" || ev.Actor.ID != bid || ev.Source.ClientAddr != "127.0.0.1" || ev.Subject.Name != "partners" {
		t.Errorf("event = %+v", ev)
	}
	waitFor(t, "the event reaches the table", 10*time.Second, func() bool {
		return e.number("SELECT count(*) FROM audit_event WHERE action = 'bastion.contact' AND details->>'release' = '0.14.0' AND details->>'replica' = 'trove-bastion-7d9c-x2k4q'") == 1
	})

	// The same values again: no write, no event.
	for range 3 {
		handshake(t, s, bst, host...)
	}
	if again := contacts(t, e, bid)["trove-bastion-7d9c-x2k4q"]; !again.at.Equal(got.at) {
		t.Errorf("a repeat within a minute rewrote the row: %v -> %v", got.at, again.at)
	}
	if n := contactEvents(s); n != 1 {
		t.Errorf("%d bastion.contact events after repeats", n)
	}

	// A changed value is written at once and recorded: the release.
	handshake(t, s, bst, append([]string{"User-Agent", "trove-bastion/0.15.0"}, host...)...)
	if c := contacts(t, e, bid)["trove-bastion-7d9c-x2k4q"]; c.release != "0.15.0" {
		t.Errorf("release = %q", c.release)
	}
	waitFor(t, "bastion.contact for the new release", 10*time.Second, func() bool { return contactEvents(s) == 2 })

	// Another replica is another row.
	handshake(t, s, bst, append([]string{iface.HeaderReplica, "trove-bastion-7d9c-zzzzz"}, host...)...)
	if rows := contacts(t, e, bid); len(rows) != 2 {
		t.Errorf("rows = %v", rows)
	}
	waitFor(t, "bastion.contact for the second replica", 10*time.Second, func() bool { return contactEvents(s) == 3 })
}

// TestR_BAS_010_AnotherReplicaOfTheMainInstallationWritesOnlyAfterAMinute: the rule is the statement's, so replicas
// of the main installation never write more often than once a minute per Bastion replica, and a row that is a minute old
// is refreshed without an event when nothing changed.
func TestR_BAS_010_AnotherReplicaOfTheMainInstallationWritesOnlyAfterAMinute(t *testing.T) {
	e := newEnv(t)
	s := e.newSite(siteOpts{})
	bid := e.registration("partners")
	rec := bastionmain.Recognition{Bastion: "partners", BastionID: bid, TokenID: "0192f1c4-0000-7000-8000-000000000001", Interface: iface.InterfaceVersion, Replica: "bastion-0"}
	rep := bastionmain.Report{ClientAddress: "198.51.100.20", UserAgent: "trove-bastion/0.14.0", ResponseBaseURL: "https://packages.example.com"}
	// Two recorders stand for two replicas of the main installation: they share the table and nothing else.
	one := bastionmain.NewContacts(bastionmain.ContactOptions{DB: s.db, Audit: s.rec})
	two := bastionmain.NewContacts(bastionmain.ContactOptions{DB: s.db, Audit: s.rec})
	ctx := context.Background()
	if err := one.Record(ctx, rec, rep); err != nil {
		t.Fatal(err)
	}
	first := contacts(t, e, bid)["bastion-0"]
	if err := two.Record(ctx, rec, rep); err != nil {
		t.Fatal(err)
	}
	if again := contacts(t, e, bid)["bastion-0"]; !again.at.Equal(first.at) {
		t.Errorf("the second replica wrote within a minute: %v -> %v", first.at, again.at)
	}
	e.exec("UPDATE bastion_contact SET contact_at = now() - interval '2 minutes' WHERE bastion_id = $1::uuid", bid)
	stale := contacts(t, e, bid)["bastion-0"]
	if err := two.Record(ctx, rec, rep); err != nil {
		t.Fatal(err)
	}
	fresh := contacts(t, e, bid)["bastion-0"]
	if !fresh.at.After(stale.at.Add(time.Minute)) {
		t.Errorf("a row of two minutes was not refreshed: %v -> %v", stale.at, fresh.at)
	}
	if n := contactEvents(s); n != 1 {
		t.Errorf("%d bastion.contact events: a refresh of unchanged values must write none", n)
	}
}

// TestR_BAS_010_TheHandshakeNeedsTheBastionToken: no token, another type, or a revoked one: 401 problem details.
func TestR_BAS_010_TheHandshakeNeedsTheBastionToken(t *testing.T) {
	e := newEnv(t)
	s := e.newSite(siteOpts{})
	for name, c := range map[string]call{
		"no credential":           {method: http.MethodGet, path: iface.PathHandshake, header: http.Header{iface.HeaderInterface: {"1"}}},
		"a revoked Bastion token": {method: http.MethodGet, path: iface.PathHandshake, header: http.Header{"Authorization": {"Bearer trv_bst_revokedrevokedrevokedrevokedrevokedrevok"}, iface.HeaderInterface: {"1"}}},
	} {
		rp := s.do(c)
		wantCode := errs.CodeBastionTokenInvalid
		if name == "no credential" {
			// The authentication stage refuses an anonymous request of an endpoint that admits none.
			wantCode = errs.CodeCredentialsRequired
		}
		if rp.Status != http.StatusUnauthorized || rp.code() != wantCode {
			t.Errorf("%s: %d %s", name, rp.Status, rp.Body)
		}
	}
}
