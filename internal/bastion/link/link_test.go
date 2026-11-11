package link_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/answers"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/link"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/maintest"
)

// TestR_BAS_027_AReplicaStartsUnreachableAndBecomesReachableWithAHandshakeAndAPoll: a replica is unreachable
// from its start, with the metrics that say so; a handshake and a feed poll that both succeed end it.
func TestR_BAS_027_AReplicaStartsUnreachableAndBecomesReachableWithAHandshakeAndAPoll(t *testing.T) {
	r := newRig(t, rigOpts{})
	start := r.clock.Now()
	st := r.l.State()
	if st.Reachable || !st.UnreachableSince.Equal(start) || st.Handshaken {
		t.Fatalf("state at start: %+v", st)
	}
	if r.gauge("reachable") != 0 || r.gauge("since") != float64(start.Unix()) {
		t.Errorf("metrics at start: reachable %v since %v", r.gauge("reachable"), r.gauge("since"))
	}
	r.main.SetAnonymous(true)
	r.tickAfter(time.Second)
	st = r.l.State()
	if !st.Reachable || !st.UnreachableSince.IsZero() || !st.Handshaken || !st.AnonymousReads || !st.RecordDownloads {
		t.Fatalf("state after the first tick: %+v", st)
	}
	if st.Registration != "partners" || st.Installation.Release != maintest.Release {
		t.Errorf("the handshake's identity: %+v", st)
	}
	if r.gauge("reachable") != 1 || r.gauge("since") != 0 || r.gauge("lag") != 0 {
		t.Errorf("metrics: reachable %v since %v lag %v", r.gauge("reachable"), r.gauge("since"), r.gauge("lag"))
	}
	r.smu.Lock()
	defer r.smu.Unlock()
	if len(r.states) == 0 || !r.states[len(r.states)-1].Reachable {
		t.Errorf("the watcher was not told: %+v", r.states)
	}
}

// TestR_BAS_027_AFailedHandshakeBeginsUnreachableForEveryCause: a connect error, a timeout, a 5xx, a refused
// token, an interface mismatch and a base URL the main installation does not honour each begin the state, with
// the metrics and since; the cause gone, the next handshake and poll end it.
func TestR_BAS_027_AFailedHandshakeBeginsUnreachableForEveryCause(t *testing.T) {
	causes := []struct {
		name  string
		cause func(r *rig)
		heal  func(r *rig)
	}{
		{"connect error", func(r *rig) { r.faulty.SetDown(true) }, func(r *rig) { r.faulty.SetDown(false) }},
		{"timeout", func(r *rig) { r.main.SetDelay(time.Second) }, func(r *rig) { r.main.SetDelay(0) }},
		{"5xx", func(r *rig) { r.main.Force("handshake", http.StatusServiceUnavailable) }, func(r *rig) { r.main.Force("handshake", 0) }},
		{"token refused", func(r *rig) { r.main.Revoke(r.token) }, func(r *rig) { r.newToken() }},
		{"interface mismatch", func(r *rig) { r.main.SetVersion(iface.InterfaceVersion + 1) }, func(r *rig) { r.main.SetVersion(iface.InterfaceVersion) }},
		{"base URL not honoured", func(r *rig) { r.main.SetBaseURLHonoured(false) }, func(r *rig) { r.main.SetBaseURLHonoured(true) }},
	}
	for _, tc := range causes {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, rigOpts{timeout: 200 * time.Millisecond})
			r.connect()
			tc.cause(r)
			r.clock.Advance(time.Second)
			r.l.RequestHandshake()
			r.tick()
			st := r.l.State()
			if st.Reachable || !st.UnreachableSince.Equal(r.clock.Now()) {
				t.Fatalf("still reachable or no since: %+v", st)
			}
			if r.gauge("reachable") != 0 || r.gauge("since") != float64(r.clock.Now().Unix()) {
				t.Errorf("metrics: reachable %v since %v", r.gauge("reachable"), r.gauge("since"))
			}
			tc.heal(r)
			r.tickAfter(2 * time.Second)
			if st := r.l.State(); !st.Reachable || !st.UnreachableSince.IsZero() {
				t.Fatalf("not reachable after the cause is gone: %+v\n%s", st, r.logs.String())
			}
			if r.gauge("reachable") != 1 || r.gauge("since") != 0 {
				t.Errorf("metrics after: reachable %v since %v", r.gauge("reachable"), r.gauge("since"))
			}
		})
	}
}

// TestR_BAS_027_NoSuccessfulPollForUnreachableAfterBeginsUnreachable: a poll that is merely late does not; one
// that is missing for unreachable_after does, and a handshake and a poll end it.
func TestR_BAS_027_NoSuccessfulPollForUnreachableAfterBeginsUnreachable(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.connect()
	r.faulty.SetDown(true)
	r.tickAfter(5 * time.Second)
	if !r.l.State().Reachable {
		t.Fatal("a feed poll 5 s late (unreachable_after 10 s) made the replica unreachable")
	}
	if lag := r.gauge("lag"); lag < 4.9 || lag > 5.1 {
		t.Errorf("trove_bastion_feed_lag_seconds = %v, want 5", lag)
	}
	r.tickAfter(5 * time.Second)
	st := r.l.State()
	if st.Reachable || !st.UnreachableSince.Equal(r.clock.Now()) {
		t.Fatalf("not unreachable after unreachable_after: %+v", st)
	}
	r.faulty.SetDown(false)
	r.tickAfter(2 * time.Second)
	if !r.l.State().Reachable {
		t.Fatalf("not reachable again after one handshake and one poll:\n%s", r.logs.String())
	}
}

// TestR_BAS_027_AForwardErrorAloneDoesNotBeginUnreachableButARefusedTokenDoes: errors of the request flow that
// say nothing about the link are not the link's business; a 401 bastion_token_invalid on a call of the interface's
// own or a mismatch in any answer is, at once, and the former sets trove_bastion_main_token_refused.
func TestR_BAS_027_AForwardErrorAloneDoesNotBeginUnreachableButARefusedTokenDoes(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.connect()
	guarded := r.l.Guard(r.c)
	do := func() error {
		u, _ := url.Parse("/api/npm/web/npm-all/x")
		resp, err := guarded.Do(context.Background(), mainclient.Forward{
			Request: &http.Request{Method: http.MethodGet, URL: u, Header: http.Header{}}, Class: mainclient.Authenticated, Kind: mainclient.KindForward,
		})
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		return err
	}
	r.main.Force("forward", http.StatusBadGateway)
	if err := do(); !errors.Is(err, mainclient.ErrUnreachable) {
		t.Fatalf("forward: %v", err)
	}
	r.faulty.FailNext(3)
	for i := 0; i < 3; i++ {
		if err := do(); !errors.Is(err, mainclient.ErrUnreachable) {
			t.Fatalf("forward on a failing transport: %v", err)
		}
	}
	if !r.l.State().Reachable {
		t.Fatal("forward errors alone made the replica unreachable")
	}
	r.main.Force("forward", 0)
	r.main.Revoke(r.token)
	// On a forward the refusal is no statement about the Bastion's token (the path is the caller's, Q-BAS-121);
	// on a call of the interface's own it is, at once.
	if err := do(); errors.Is(err, mainclient.ErrTokenRefused) || err == nil {
		t.Fatalf("forward with a revoked token: %v", err)
	}
	if !r.l.State().Reachable || r.gauge("refused") != 0 {
		t.Fatalf("a forward's 401 bastion_token_invalid moved the link: reachable %v refused %v", r.l.State().Reachable, r.gauge("refused"))
	}
	if _, err := guarded.Check(context.Background(), mainclient.Authenticated, iface.CheckRequest{Repositories: []string{"web/npm-all"}}); !errors.Is(err, mainclient.ErrTokenRefused) {
		t.Fatalf("check with a revoked token: %v", err)
	}
	if r.l.State().Reachable || r.gauge("reachable") != 0 || r.gauge("refused") != 1 {
		t.Errorf("after a refused token: reachable %v gauge %v refused %v", r.l.State().Reachable, r.gauge("reachable"), r.gauge("refused"))
	}
	if r.logs.count("the main installation refused the Bastion token") == 0 {
		t.Error("the refusal is not logged")
	}
	if strings.Contains(r.logs.String(), r.token) {
		t.Error("the log shows the token")
	}
	// A new token ends it with the next handshake and poll.
	r.newToken()
	r.tickAfter(2 * time.Second)
	if !r.l.State().Reachable || r.gauge("refused") != 0 {
		t.Errorf("after a new token: reachable %v refused %v", r.l.State().Reachable, r.gauge("refused"))
	}
}

// TestR_BAS_027_WhileUnreachableOnlyHandshakeAndFeedAreSent: every tick of an unreachable replica is a handshake
// and a feed probe, nothing else.
func TestR_BAS_027_WhileUnreachableOnlyHandshakeAndFeedAreSent(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.connect()
	r.main.Force("handshake", http.StatusServiceUnavailable)
	r.clock.Advance(time.Second)
	r.l.RequestHandshake()
	r.tick()
	if r.l.State().Reachable {
		t.Fatal("still reachable")
	}
	hs, feed := r.main.Calls("handshake"), r.main.Calls("feed")
	for i := 0; i < 4; i++ {
		r.tickAfter(2 * time.Second)
	}
	if got := r.main.Calls("handshake") - hs; got != 4 {
		t.Errorf("handshakes while unreachable = %d, want one per poll interval (4)", got)
	}
	if got := r.main.Calls("feed") - feed; got != 4 {
		t.Errorf("feed probes while unreachable = %d, want 4", got)
	}
	for _, op := range []string{"check", "forward", "downloads"} {
		if n := r.main.Calls(op); n != 0 {
			t.Errorf("%s: %d requests while unreachable", op, n)
		}
	}
}

// TestR_BAS_027_TheGracePeriodExtendsAnswersWhileUnreachableAndNeverTheTokensExpiry: the link is cut by a
// fault-injecting transport. An answer is usable for the period while reachable; while unreachable for period
// plus grace period, at the latest that long after unreachability began; the token's expiry is never extended;
// when the link returns, the grace period ends with it.
func TestR_BAS_027_TheGracePeriodExtendsAnswersWhileUnreachableAndNeverTheTokensExpiry(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.connect()
	t0 := r.clock.Now()
	plain := r.answer(t0, "caller-a")
	expiring := r.answer(t0, "caller-b")
	exp := t0.Add(30 * time.Second)
	expiring.TokenExpiresAt = &exp
	if r.check(plain) != answers.Usable || r.check(expiring) != answers.Usable {
		t.Fatal("fresh answers are not usable")
	}

	r.clock.Advance(2 * time.Minute)
	r.faulty.SetDown(true)
	r.tick()
	st := r.l.State()
	if st.Reachable {
		t.Fatalf("not unreachable after the cut: %+v", st)
	}
	cut := st.UnreachableSince
	atCut := r.answer(cut, "caller-c") // obtained at the moment the cut was noticed

	// Within the period everything but the expired token is usable.
	if got := r.check(plain); got != answers.Usable {
		t.Errorf("plain at 2m: %v", got)
	}
	if got := r.check(expiring); got != answers.TokenExpired {
		t.Errorf("expiring at 2m: %v, want token_expired", got)
	}
	// Past the period, while unreachable: usable, which the same answer is not while reachable.
	r.clock.Advance(3*time.Minute + time.Second) // 5m1s after t0
	st = r.l.State()
	live := r.l.AnswerState(r.clock.Now())
	if got := live.Check(plain); got != answers.Usable {
		t.Errorf("plain past the period while unreachable: %v", got)
	}
	reachableView := live
	reachableView.Unreachable = false
	if got := reachableView.Check(plain); got != answers.PeriodOver {
		t.Errorf("the same answer judged as reachable: %v", got)
	}
	if got := live.Check(expiring); got != answers.TokenExpired {
		t.Errorf("the token's expiry was extended by the grace period: %v", got)
	}
	// At period + grace after t0 it is over; one obtained at the cut lives to cut + period + grace.
	r.clock.Advance(t0.Add(20 * time.Minute).Sub(r.clock.Now())) // t0 + 20m, cut + 18m
	if got := r.check(plain); got != answers.PeriodOver {
		t.Errorf("plain at period+grace: %v", got)
	}
	if got := r.check(atCut); got != answers.Usable {
		t.Errorf("an answer from the cut, 18 minutes later: %v", got)
	}
	r.clock.Advance(2 * time.Minute) // cut + 20m
	if got := r.check(atCut); got != answers.PeriodOver {
		t.Errorf("an answer from the cut, period+grace after unreachability began: %v", got)
	}
	// The link returns: the grace period is over with the outage.
	r.faulty.SetDown(false)
	r.tick()
	if !r.l.State().Reachable {
		t.Fatalf("not back:\n%s", r.logs.String())
	}
	fresh := r.answer(r.clock.Now(), "caller-d")
	if got := r.check(fresh); got != answers.Usable {
		t.Errorf("a fresh answer after the return: %v", got)
	}
	r.clock.Advance(5*time.Minute + time.Second)
	if got := r.check(fresh); got != answers.PeriodOver {
		t.Errorf("an answer past the period after the return: %v", got)
	}
}

// TestR_BAS_027_ARevocationMadeDuringTheOutageIsAppliedWhenTheLinkIsBack: H-383 in the small. During the cut the
// entry cannot be seen and the answer stays usable for the grace period; the first poll after the return
// applies the entry, and the answer is refused from then on.
func TestR_BAS_027_ARevocationMadeDuringTheOutageIsAppliedWhenTheLinkIsBack(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.connect()
	a := r.answer(r.clock.Now(), "caller-x")
	r.clock.Advance(time.Minute)
	r.faulty.SetDown(true)
	r.tickAfter(11 * time.Second)
	r.main.AddEntry(iface.FeedEntry{Kind: iface.FeedToken, Tokens: []string{"caller-x"}})
	r.tickAfter(30 * time.Second)
	if got := r.check(a); got != answers.Usable {
		t.Fatalf("during the outage: %v", got)
	}
	r.faulty.SetDown(false)
	r.tickAfter(2 * time.Second)
	if got := r.check(a); got != answers.Revoked {
		t.Errorf("after the return: %v", got)
	}
	if r.applied("token") != 1 {
		t.Errorf("applied token entries = %v", r.applied("token"))
	}
}

// TestR_BAS_028_AMismatchMakesTheReplicaUnreachableAndClearsWithAnEqualHandshake: the metrics name both
// versions, the log names both and the main installation's release, and the state ends with a handshake of equal
// versions after either side was upgraded.
func TestR_BAS_028_AMismatchMakesTheReplicaUnreachableAndClearsWithAnEqualHandshake(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.connect()
	if maintest.Value(r.m.InterfaceVersion.WithLabelValues("main")) != iface.InterfaceVersion || maintest.Value(r.m.InterfaceVersion.WithLabelValues("bastion")) != iface.InterfaceVersion {
		t.Fatal("the versions are not exported after the first handshake")
	}
	a := r.answer(r.clock.Now(), "caller")
	r.main.SetVersion(iface.InterfaceVersion + 1)
	r.clock.Advance(time.Second)
	r.l.RequestHandshake()
	r.tick()
	st := r.l.State()
	if st.Reachable || !st.InterfaceMismatch {
		t.Fatalf("state: %+v", st)
	}
	if r.gauge("mismatch") != 1 || maintest.Value(r.m.InterfaceVersion.WithLabelValues("main")) != iface.InterfaceVersion+1 || maintest.Value(r.m.InterfaceVersion.WithLabelValues("bastion")) != iface.InterfaceVersion {
		t.Errorf("metrics: mismatch %v main %v bastion %v", r.gauge("mismatch"), maintest.Value(r.m.InterfaceVersion.WithLabelValues("main")), maintest.Value(r.m.InterfaceVersion.WithLabelValues("bastion")))
	}
	out := r.logs.String()
	if !strings.Contains(out, "bastion interface version differs") || !strings.Contains(out, fmt.Sprintf(`"bastion":%d`, iface.InterfaceVersion)) || !strings.Contains(out, fmt.Sprintf(`"main":%d`, iface.InterfaceVersion+1)) || !strings.Contains(out, maintest.Release) {
		t.Errorf("the log does not name both versions and the release:\n%s", out)
	}
	// The grace period holds for the answers the replica has; a cold request is the engine's 503.
	r.clock.Advance(6 * time.Minute)
	if got := r.check(a); got != answers.Usable {
		t.Errorf("a held answer during a mismatch, past the period: %v", got)
	}
	// Either side is upgraded: the main installation speaks 1 again.
	r.main.SetVersion(iface.InterfaceVersion)
	r.tickAfter(2 * time.Second)
	st = r.l.State()
	if !st.Reachable || st.InterfaceMismatch || r.gauge("mismatch") != 0 {
		t.Errorf("after the upgrade: %+v mismatch %v", st, r.gauge("mismatch"))
	}
}

// TestR_BAS_028_AMismatchInAnyAnswerIsNotedAtOnce: a forward whose answer states another version makes the replica
// unreachable without waiting for the next handshake.
func TestR_BAS_028_AMismatchInAnyAnswerIsNotedAtOnce(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.connect()
	guarded := r.l.Guard(r.c)
	r.main.SetVersion(iface.InterfaceVersion + 1)
	_, err := guarded.Check(context.Background(), mainclient.Authenticated, iface.CheckRequest{Repositories: []string{"a"}})
	if !errors.Is(err, mainclient.ErrInterfaceMismatch) {
		t.Fatal(err)
	}
	st := r.l.State()
	if st.Reachable || !st.InterfaceMismatch || r.gauge("mismatch") != 1 {
		t.Errorf("state %+v mismatch %v", st, r.gauge("mismatch"))
	}
}

// TestR_BAS_010_TheHandshakesBaseURLMustEqualTheBastions: the main installation derives the response base URL
// from the forwarded host the client sent (R-BAS-017); a Bastion whose host is not among the main
// installation's additional base URLs gets the main URL back, which is unreachable, with
// trove_bastion_base_url_mismatch 1 and a log line. A port that is not the default is part of the comparison.
func TestR_BAS_010_TheHandshakesBaseURLMustEqualTheBastions(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.main.SetBaseURLHonoured(false)
	r.tickAfter(time.Second)
	st := r.l.State()
	if st.Reachable || !st.BaseURLMismatch || r.gauge("baseurl") != 1 {
		t.Fatalf("state %+v gauge %v", st, r.gauge("baseurl"))
	}
	if !strings.Contains(r.logs.String(), "bastion base URL not honoured by the main installation") {
		t.Errorf("no log line:\n%s", r.logs.String())
	}
	r.main.SetBaseURLHonoured(true)
	r.tickAfter(2 * time.Second)
	if st := r.l.State(); !st.Reachable || st.BaseURLMismatch || r.gauge("baseurl") != 0 {
		t.Errorf("after the main installation lists the host: %+v", st)
	}

	// The comparison is of origins: scheme, host (case-insensitive) and port, a default port being none.
	for _, tc := range []struct {
		name, sent, configured string
		mismatch               bool
	}{
		{"a port that is not the default", "https://packages.example.com:8443", "https://packages.example.com:8443", false},
		{"the port is missing at the Bastion", "https://packages.example.com:8443", "https://packages.example.com", true},
		{"case and a trailing slash", "https://Packages.Example.com", "https://packages.example.com/", false},
		{"another scheme", "http://packages.example.com", "https://packages.example.com", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, rigOpts{clientBase: tc.sent, mutate: func(c *link.Config) { c.BaseURL = tc.configured }})
			r.tickAfter(time.Second)
			if got := r.l.State().BaseURLMismatch; got != tc.mismatch {
				t.Errorf("mismatch = %v, want %v", got, tc.mismatch)
			}
			if got := r.l.State().Reachable; got == tc.mismatch {
				t.Errorf("reachable = %v with mismatch %v", got, tc.mismatch)
			}
		})
	}
}

// TestR_BAS_010_ASettingsEntryMakesTheNextTickHandshake: anonymousReads and recordDownloads follow the main
// installation within one tick of the settings entry, and the watcher hears of it.
func TestR_BAS_010_ASettingsEntryMakesTheNextTickHandshake(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.connect()
	if r.l.State().AnonymousReads || !r.l.State().RecordDownloads {
		t.Fatalf("flags: %+v", r.l.State())
	}
	hs := r.main.Calls("handshake")
	r.main.SetAnonymous(true)
	r.main.SetRecordDownloads(false)
	r.main.AddEntry(iface.FeedEntry{Kind: iface.FeedSettings})
	r.tickAfter(2 * time.Second) // the poll reads the entry
	if r.applied("settings") != 1 {
		t.Fatalf("settings entries applied = %v", r.applied("settings"))
	}
	r.tickAfter(2 * time.Second) // the handshake it asked for
	if got := r.main.Calls("handshake") - hs; got != 1 {
		t.Errorf("handshakes after the entry = %d, want 1", got)
	}
	st := r.l.State()
	if !st.AnonymousReads || st.RecordDownloads {
		t.Errorf("flags after the handshake: %+v", st)
	}
	r.smu.Lock()
	last := r.states[len(r.states)-1]
	r.smu.Unlock()
	if !last.AnonymousReads || last.RecordDownloads {
		t.Errorf("the watcher's last state: %+v", last)
	}
	// Nothing changes between handshakes: the interval handshake finds the same flags and does not notify.
	r.smu.Lock()
	n := len(r.states)
	r.smu.Unlock()
	r.tickAfter(time.Minute)
	r.smu.Lock()
	defer r.smu.Unlock()
	if len(r.states) != n {
		t.Errorf("the watcher was told of an unchanged state")
	}
}

// TestR_BAS_010_TheHandshakeRepeatsEveryHandshakeInterval: one handshake per interval while reachable.
func TestR_BAS_010_TheHandshakeRepeatsEveryHandshakeInterval(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.connect()
	hs := r.main.Calls("handshake")
	for i := 0; i < 5; i++ {
		r.tickAfter(10 * time.Second)
	}
	if got := r.main.Calls("handshake") - hs; got != 0 {
		t.Errorf("handshakes within the interval = %d", got)
	}
	r.tickAfter(11 * time.Second) // 61 s since the first
	if got := r.main.Calls("handshake") - hs; got != 1 {
		t.Errorf("handshakes after the interval = %d", got)
	}
}

// TestR_BAS_026_TheFeedStartsAtTheSyntheticCursorAndTheHistoryStartsThere: before the first read succeeded no
// answer is usable; the first read is t:<start - period - grace - 15 s>, which is also the start of the history;
// an empty read moves the time forward to the request.
func TestR_BAS_026_TheFeedStartsAtTheSyntheticCursorAndTheHistoryStartsThere(t *testing.T) {
	r := newRig(t, rigOpts{})
	start := r.clock.Now()
	a := r.answer(start, "caller")
	if got := r.check(a); got != answers.BeforeStart {
		t.Fatalf("before the first read: %v", got)
	}
	r.tickAfter(time.Second)
	wantStart := start.Add(-(5*time.Minute + 15*time.Minute + 15*time.Second)).UTC()
	reads := r.main.FeedReads()
	if len(reads) != 1 || reads[0] != "t:"+wantStart.Format(time.RFC3339Nano) {
		t.Fatalf("first read %q, want t:%s", reads, wantStart.Format(time.RFC3339Nano))
	}
	if got := r.l.State().HistoryStart; !got.Equal(wantStart) {
		t.Errorf("history start %v, want %v", got, wantStart)
	}
	if got := r.check(a); got != answers.Usable {
		t.Errorf("after the first read: %v", got)
	}
	r.tickAfter(2 * time.Second)
	reads = r.main.FeedReads()
	if want := "t:" + start.Add(time.Second).UTC().Format(time.RFC3339Nano); reads[1] != want {
		t.Errorf("second read %q, want %q (the empty read moves the time)", reads[1], want)
	}
	// With an entry, the cursor becomes the entry's.
	e := r.main.AddEntry(iface.FeedEntry{Kind: iface.FeedGrants})
	r.tickAfter(2 * time.Second)
	r.tickAfter(2 * time.Second)
	reads = r.main.FeedReads()
	last := reads[len(reads)-1]
	cur, err := iface.ParseCursor(last)
	if err != nil || cur.ID != e.ID {
		t.Errorf("the read after the entry used %q (%v)", last, err)
	}
}

// TestR_BAS_026_AnEntryIsAppliedOnceByItsID: the lookback re-sends an entry for as long as it lies within it; the
// log records it once, with its receipt time, counts it once and asks the request flow to delete the answers it
// matches once.
func TestR_BAS_026_AnEntryIsAppliedOnceByItsID(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.connect()
	a := r.answer(r.clock.Now(), "caller-1")
	other := r.answer(r.clock.Now(), "caller-2")
	r.clock.Advance(time.Second)
	e := r.main.AddEntry(iface.FeedEntry{Kind: iface.FeedToken, Tokens: []string{"caller-1"}})
	r.tickAfter(2 * time.Second)
	if r.check(a) != answers.Revoked || r.check(other) != answers.Usable {
		t.Fatalf("matching: a %v, other %v", r.check(a), r.check(other))
	}
	// The main installation sends it again for as long as it lies within the lookback.
	again, err := r.c.Feed(context.Background(), iface.Cursor{At: e.At, ID: e.ID}, 10)
	if err != nil || len(again.Entries) != 1 || again.Entries[0].ID != e.ID {
		t.Fatalf("the lookback did not re-send the entry: %+v %v", again, err)
	}
	for i := 0; i < 3; i++ {
		r.tickAfter(2 * time.Second)
	}
	if r.applied("token") != 1 {
		t.Errorf("applied = %v, want 1", r.applied("token"))
	}
	if got := r.sink.entries(); len(got) != 1 || got[0].ID != e.ID {
		t.Errorf("the sink was asked to delete %+v", got)
	}
}

// TestR_BAS_026_EveryKindIsAppliedAndMatchesWhatTheRulesSay: a token, a principal, a grants and a content entry
// each make exactly the answers unusable that R-BAS-020 rule 3 names; each is counted by kind.
func TestR_BAS_026_EveryKindIsAppliedAndMatchesWhatTheRulesSay(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.connect()
	o := r.clock.Now()
	tokenAns := answers.Answer{Kind: answers.Credential, ObtainedAt: o, TokenID: "t1", PrincipalID: "p1"}
	principalAns := answers.Answer{Kind: answers.Credential, ObtainedAt: o, TokenID: "t2", PrincipalID: "p2"}
	shape := answers.Answer{Kind: answers.Shape, ObtainedAt: o, TokenID: "t3", PrincipalID: "p3", Repository: "web/npm-all"}
	url1 := answers.Answer{Kind: answers.URL, ObtainedAt: o, TokenID: "t4", PrincipalID: "p4", Repository: "web/npm-local"}
	url2 := answers.Answer{Kind: answers.URL, ObtainedAt: o, TokenID: "t5", PrincipalID: "p5", Repository: "npm-other"}
	all := []answers.Answer{tokenAns, principalAns, shape, url1, url2}
	for _, a := range all {
		if r.check(a) != answers.Usable {
			t.Fatalf("%+v not usable at first", a)
		}
	}
	r.clock.Advance(time.Second)
	r.main.AddEntry(iface.FeedEntry{Kind: iface.FeedToken, Tokens: []string{"t1"}})
	r.tickAfter(time.Second)
	if r.check(tokenAns) != answers.Revoked || r.check(principalAns) != answers.Usable {
		t.Error("the token entry")
	}
	r.main.AddEntry(iface.FeedEntry{Kind: iface.FeedPrincipal, Principals: []string{"p2"}})
	r.tickAfter(time.Second)
	if r.check(principalAns) != answers.Revoked || r.check(shape) != answers.Usable {
		t.Error("the principal entry")
	}
	r.main.AddEntry(iface.FeedEntry{Kind: iface.FeedContent, Repositories: []string{"web/npm-local", "web/npm-all"}})
	r.tickAfter(time.Second)
	if r.check(url1) != answers.Revoked || r.check(url2) != answers.Usable || r.check(shape) != answers.Usable {
		t.Errorf("the content entry: url1 %v url2 %v shape %v", r.check(url1), r.check(url2), r.check(shape))
	}
	r.main.AddEntry(iface.FeedEntry{Kind: iface.FeedGrants})
	r.tickAfter(time.Second)
	if r.check(shape) != answers.Revoked || r.check(url2) != answers.Revoked {
		t.Error("the grants entry")
	}
	for _, k := range []string{"token", "principal", "content", "grants"} {
		if r.applied(k) != 1 {
			t.Errorf("applied %s = %v", k, r.applied(k))
		}
	}
	if len(r.sink.entries()) != 4 {
		t.Errorf("the sink was asked %d times", len(r.sink.entries()))
	}
}

// TestR_BAS_026_APageThatIsCutIsReadToTheEnd: a limit of 100 and 250 entries are three pages in one tick.
func TestR_BAS_026_APageThatIsCutIsReadToTheEnd(t *testing.T) {
	r := newRig(t, rigOpts{mutate: func(c *link.Config) { c.FeedLimit = 100 }})
	r.connect()
	for i := 0; i < 250; i++ {
		r.main.AddEntry(iface.FeedEntry{Kind: iface.FeedToken, Tokens: []string{fmt.Sprintf("t%d", i)}})
	}
	before := r.main.Calls("feed")
	r.tickAfter(2 * time.Second)
	if r.applied("token") != 250 {
		t.Errorf("applied = %v, want 250", r.applied("token"))
	}
	if got := r.main.Calls("feed") - before; got < 3 {
		t.Errorf("feed reads = %d, want at least 3 pages", got)
	}
	if r.check(r.answer(r.clock.Now(), "t7")) != answers.Revoked {
		t.Error("an answer of the 8th token is usable")
	}
}

// TestR_BAS_026_AResetDropsEveryAnswerAndMovesTheHistoryStart: on reset: true the log is emptied, the request
// flow is told to drop every answer, the history starts at the reset, and an answer from before it is not used.
func TestR_BAS_026_AResetDropsEveryAnswerAndMovesTheHistoryStart(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.connect()
	old := r.answer(r.clock.Now(), "caller")
	r.main.AddEntry(iface.FeedEntry{Kind: iface.FeedToken, Tokens: []string{"other"}})
	r.tickAfter(2 * time.Second)
	r.main.Reset()
	r.tickAfter(2 * time.Second)
	reset := r.clock.Now()
	if r.gauge("resets") != 1 || r.sink.all != 1 {
		t.Fatalf("resets %v, DropAll %d", r.gauge("resets"), r.sink.all)
	}
	if got := r.l.State().HistoryStart; !got.Equal(reset) {
		t.Errorf("history start %v, want the reset time %v", got, reset)
	}
	if got := r.check(old); got != answers.BeforeStart {
		t.Errorf("an answer from before the reset: %v", got)
	}
	r.clock.Advance(16 * time.Second)
	if got := r.check(r.answer(r.clock.Now(), "caller")); got != answers.Usable {
		t.Errorf("an answer from after the reset: %v", got)
	}
	if !r.l.State().Reachable {
		t.Error("a reset made the replica unreachable")
	}
}

// TestR_BAS_026_AReplicaStartedAfterARevocationDoesNotAcceptTheRevokedAnswer: the entry is older than the
// replica; the start reads the history of an answer's lifetime, so an answer stored by another replica before
// the revocation is refused by the new one.
func TestR_BAS_026_AReplicaStartedAfterARevocationDoesNotAcceptTheRevokedAnswer(t *testing.T) {
	clk := newClock()
	main := maintest.New(t)
	main.SetClock(clk.Now)
	stored := clk.Now() // the answer another replica stored, o
	clk.Advance(time.Minute)
	main.AddEntry(iface.FeedEntry{Kind: iface.FeedToken, Tokens: []string{"caller"}})
	clk.Advance(2 * time.Minute)
	r := newRig(t, rigOpts{main: main, clock: clk})
	r.tick()
	if got := r.check(r.answer(stored, "caller")); got != answers.Revoked {
		t.Errorf("the revoked answer from before the start: %v", got)
	}
	if got := r.check(r.answer(stored, "someone-else")); got != answers.Usable {
		t.Errorf("an unrelated answer from before the start: %v", got)
	}
}

// revokedWithin measures how long a token entry takes to make the answer unusable on every replica.
func revokedWithin(t *testing.T, replicas []*rig, id string, bound time.Duration) time.Duration {
	t.Helper()
	answersByReplica := make([]answers.Answer, len(replicas))
	for i, r := range replicas {
		answersByReplica[i] = answers.Answer{Kind: answers.Credential, ObtainedAt: time.Now(), TokenID: id}
		if got := r.l.AnswerState(time.Now()).Check(answersByReplica[i]); got != answers.Usable {
			t.Fatalf("replica %d: the answer is %v before the revocation", i, got)
		}
	}
	replicas[0].main.AddEntry(iface.FeedEntry{Kind: iface.FeedToken, Tokens: []string{id}})
	start := time.Now()
	deadline := start.Add(bound)
	for {
		done := true
		for i, r := range replicas {
			if r.l.AnswerState(time.Now()).Check(answersByReplica[i]) != answers.Revoked {
				done = false
			}
		}
		if done {
			return time.Since(start)
		}
		if time.Now().After(deadline) {
			t.Fatalf("token %s still accepted by a replica after %v", id, bound)
		}
		time.Sleep(time.Millisecond)
	}
}

// runLinks starts the polling loops of the replicas on real time.
func runLinks(t *testing.T, replicas []*rig) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, r := range replicas {
		wg.Add(1)
		go func() { defer wg.Done(); r.l.Run(ctx) }()
	}
	t.Cleanup(func() { cancel(); wg.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for _, r := range replicas {
		for {
			if st := r.l.State(); st.Reachable && !st.HistoryStart.IsZero() {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("a replica did not connect:\n%s", r.logs.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func realTimeReplicas(t *testing.T, n int, poll time.Duration) []*rig {
	t.Helper()
	main := maintest.New(t)
	out := make([]*rig, n)
	for i := range out {
		out[i] = newRig(t, rigOpts{main: main, realTime: true, mutate: func(c *link.Config) {
			c.PollInterval, c.UnreachableAfter, c.HandshakeInterval = poll, 3*poll+time.Second, time.Hour
		}})
	}
	runLinks(t, out)
	return out
}

// TestR_BAS_026_ARevocationReachesEveryReplicaWithin5SecondsOver100Trials: two replicas follow the feed; after a
// token entry is committed at the main installation, both refuse an answer obtained before it within 5 s, in each
// of 100 trials. The poll interval is shortened so that the trials take seconds; the default interval's bound is
// the next test.
func TestR_BAS_026_ARevocationReachesEveryReplicaWithin5SecondsOver100Trials(t *testing.T) {
	replicas := realTimeReplicas(t, 2, 25*time.Millisecond)
	var worst time.Duration
	for i := 0; i < 100; i++ {
		if d := revokedWithin(t, replicas, fmt.Sprintf("caller-%03d", i), 5*time.Second); d > worst {
			worst = d
		}
	}
	t.Logf("slowest of 100 trials: %v", worst)
	for i, r := range replicas {
		// Record makes an entry effective before the metric counts it (applyEntries), so the last trial can pass while
		// its count is still on the way: wait for the count, bounded, instead of reading it once.
		deadline := time.Now().Add(5 * time.Second)
		for r.applied("token") != 100 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if n := r.applied("token"); n != 100 {
			t.Errorf("replica %d applied %v token entries", i, n)
		}
		if !r.l.State().Reachable {
			t.Errorf("replica %d is not reachable", i)
		}
	}
}

// TestR_BAS_026_TheDefaultPollIntervalHoldsTheBound: with feed.poll_interval at its default of 2 s the entry is in
// force at both replicas within 5 s.
func TestR_BAS_026_TheDefaultPollIntervalHoldsTheBound(t *testing.T) {
	replicas := realTimeReplicas(t, 2, 2*time.Second)
	d := revokedWithin(t, replicas, "caller-default", 5*time.Second)
	t.Logf("with the default poll interval: %v", d)
}

// TestR_BAS_006_TheTokenLifetimeIsExportedAndWarnedOnceADay: a token that expires in 20 days is exported as
// remaining seconds below 1,728,001 and logged once at start (bastion token expires soon) and then once a day;
// one that expires in 40 days is not warned about.
func TestR_BAS_006_TheTokenLifetimeIsExportedAndWarnedOnceADay(t *testing.T) {
	r := newRig(t, rigOpts{expires: 20 * 24 * time.Hour})
	r.connect()
	rem := r.gauge("remaining")
	if rem >= 1_728_001 || rem < 1_728_000-5 {
		t.Errorf("remaining = %v", rem)
	}
	if n := r.logs.count("bastion token expires soon"); n != 1 {
		t.Fatalf("warnings at start = %d", n)
	}
	out := r.logs.String()
	if !strings.Contains(out, `"registration":"partners"`) || !strings.Contains(out, r.token[:12]) || !strings.Contains(out, `"token_id":"tok-`) || !strings.Contains(out, "expiresAt") || strings.Contains(out, r.token) {
		t.Errorf("the warning: %s", out)
	}
	for i := 0; i < 5; i++ {
		r.tickAfter(2 * time.Second)
	}
	if n := r.logs.count("bastion token expires soon"); n != 1 {
		t.Errorf("warnings within the day = %d", n)
	}
	if got := r.gauge("remaining"); got >= rem {
		t.Errorf("remaining does not follow the clock: %v then %v", rem, got)
	}
	r.tickAfter(24*time.Hour + time.Second)
	if n := r.logs.count("bastion token expires soon"); n != 2 {
		t.Errorf("warnings after a day = %d", n)
	}
	// A reload of the token restarts the clock of the warning and asks for a handshake.
	r.newToken()
	r.l.TokenChanged()
	r.tickAfter(time.Second)
	if n := r.logs.count("bastion token expires soon"); n != 2 {
		t.Errorf("a new token with a year left was warned about: %d", n)
	}
	if got := r.gauge("remaining"); got < 300*24*3600 {
		t.Errorf("remaining after the new token = %v", got)
	}
	long := newRig(t, rigOpts{expires: 40 * 24 * time.Hour})
	long.connect()
	if n := long.logs.count("bastion token expires soon"); n != 0 {
		t.Errorf("a token with 40 days left was warned about")
	}
}

// TestR_BAS_006_ARevokedTokenSetsTheRefusedGaugeWithinThePollIntervalPlus5Seconds: revoking the token in use
// makes the replica report trove_bastion_main_token_refused 1 and unreachable.
func TestR_BAS_006_ARevokedTokenSetsTheRefusedGaugeWithinThePollIntervalPlus5Seconds(t *testing.T) {
	replicas := realTimeReplicas(t, 1, 25*time.Millisecond)
	r := replicas[0]
	r.main.Revoke(r.token)
	deadline := time.Now().Add(5*time.Second + 25*time.Millisecond)
	for r.gauge("refused") != 1 || r.l.State().Reachable {
		if time.Now().After(deadline) {
			t.Fatalf("refused %v reachable %v", r.gauge("refused"), r.l.State().Reachable)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
