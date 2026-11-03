package ci

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/ci/reason"
)

// R-CI-016, Q-CI-47: a mapping's maxTtlSeconds bounds its own contribution, never the token beside another tier: the
// token's lifetime is its owner's (the system tier's contribution, else the project contribution that lives
// longest), and every other contribution ends at the earlier of the token's expiry and its own bound.
func TestR_CI_016_AMappingsBoundEndsOnlyItsOwnContribution(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	base := TTLInput{Default: 15 * time.Minute, Max: time.Hour, Min: 30 * time.Second, Now: now, SubjectExpiresAt: now.Add(time.Hour)}
	bound := func(m Mapping, s int64) Mapping { m.MaxTTLSeconds = &s; return m }
	sys := Contribution{Mapping: mappingOf(TierSystem, 10, "sys", onClaim("project_id", "1"), readGrant("npm-internal"))}
	web := Contribution{Mapping: bound(mappingOf(ProjectTier("web"), 10, "web-short", onClaim("project_id", "1"), readGrant("web-npm")), 1)}
	data := Contribution{Mapping: bound(mappingOf(ProjectTier("data"), 10, "data-mid", onClaim("project_id", "1"), readGrant("data-npm")), 300)}
	shop := Contribution{Mapping: mappingOf(ProjectTier("shop"), 10, "shop-open", onClaim("project_id", "1"), readGrant("shop-npm"))}
	cases := []struct {
		name    string
		cs      []Contribution
		ttl     time.Duration
		owner   int
		windows []time.Duration
		reason  reason.Reason
	}{
		{"a project's bound of 1 s does not bound the system tier's token", []Contribution{sys, web}, 15 * time.Minute, 0, []time.Duration{15 * time.Minute, time.Second}, ""},
		{"the system tier's own bound is the token's", []Contribution{{Mapping: bound(sys.Mapping, 120)}, data}, 2 * time.Minute, 0, []time.Duration{2 * time.Minute, 2 * time.Minute}, ""},
		{"without the system tier the longest contribution owns the token", []Contribution{web, data, shop}, 15 * time.Minute, 2, []time.Duration{time.Second, 5 * time.Minute, 15 * time.Minute}, ""},
		{"two bounded projects: the longer owns", []Contribution{web, data}, 5 * time.Minute, 1, []time.Duration{time.Second, 5 * time.Minute}, ""},
		{"one project alone keeps R-CI-016's floor", []Contribution{web}, 0, 0, nil, reason.TTLBelowMinimum},
		{"the system tier's bound below the floor fails as before", []Contribution{{Mapping: bound(sys.Mapping, 10)}, shop}, 0, 0, nil, reason.TTLBelowMinimum},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lt, r, _ := ComputeLifetime(base, c.cs)
			if r != c.reason || lt.TTL != c.ttl || (r == "" && (lt.Owner != c.owner || !slices.Equal(lt.Windows, c.windows))) {
				t.Fatalf("ComputeLifetime = %+v, %q; want ttl %s, owner %d, windows %v, %q", lt, r, c.ttl, c.owner, c.windows, c.reason)
			}
		})
	}
	t.Run("a requested ttl bounds every contribution", func(t *testing.T) {
		req := time.Minute
		in := base
		in.Requested = &req
		lt, r, _ := ComputeLifetime(in, []Contribution{sys, data})
		if r != "" || lt.TTL != time.Minute || !slices.Equal(lt.Windows, []time.Duration{time.Minute, time.Minute}) {
			t.Fatalf("%+v %q", lt, r)
		}
	})
}

func TestR_CI_016_ComputeTTL(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	d := func(s int64) *time.Duration { v := time.Duration(s) * time.Second; return &v }
	base := TTLInput{Default: 15 * time.Minute, Max: time.Hour, Min: 30 * time.Second, Now: now, SubjectExpiresAt: now.Add(time.Hour)}
	with := func(f func(*TTLInput)) TTLInput { in := base; f(&in); return in }
	cases := []struct {
		name   string
		in     TTLInput
		want   time.Duration
		reason reason.Reason
		detail string
	}{
		{"the default", base, 15 * time.Minute, "", ""},
		{"a requested ttl shortens", with(func(in *TTLInput) { in.Requested = d(60) }), time.Minute, "", ""},
		{"a requested ttl never lengthens past ci.token.max_ttl", with(func(in *TTLInput) { in.Requested = d(86400) }), time.Hour, "", ""},
		{"the mapping's bound", with(func(in *TTLInput) { in.MappingMax = d(600) }), 10 * time.Minute, "", ""},
		{"the issuer's bound", with(func(in *TTLInput) { in.IssuerMax = d(300) }), 5 * time.Minute, "", ""},
		{"ten minutes of the subject token left", with(func(in *TTLInput) { in.SubjectExpiresAt = now.Add(10 * time.Minute) }), 10 * time.Minute, "", ""},
		{"whole seconds", with(func(in *TTLInput) { in.SubjectExpiresAt = now.Add(100*time.Second + 900*time.Millisecond) }), 100 * time.Second, "", ""},
		{"no subject term without an exp", with(func(in *TTLInput) { in.SubjectExpiresAt = time.Time{} }), 15 * time.Minute, "", ""},
		{"20 s of the subject token left", with(func(in *TTLInput) { in.SubjectExpiresAt = now.Add(20 * time.Second) }), 0, reason.SubjectTokenExpiresTooSoon, "subject token"},
		{"an expired subject token within the skew", with(func(in *TTLInput) { in.SubjectExpiresAt = now.Add(-10 * time.Second) }), 0, reason.SubjectTokenExpiresTooSoon, "subject token"},
		{"Q-CI-41 a requested ttl below the floor is the caller's", with(func(in *TTLInput) { in.Requested = d(10) }), 0, reason.TTLBelowMinimum, "requested ttl"},
		{"Q-CI-41 the request is named before the subject", with(func(in *TTLInput) {
			in.Requested, in.SubjectExpiresAt = d(10), now.Add(5*time.Second)
		}), 0, reason.TTLBelowMinimum, "requested ttl"},
		{"Q-CI-41 a mapping's bound below the floor", with(func(in *TTLInput) { in.MappingMax = d(10) }), 0, reason.TTLBelowMinimum, "mapping"},
		{"Q-CI-41 the issuer's bound below the floor", with(func(in *TTLInput) { in.IssuerMax = d(10) }), 0, reason.TTLBelowMinimum, "issuer"},
		{"exactly the floor", with(func(in *TTLInput) { in.Requested = d(30) }), 30 * time.Second, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, r, detail := ComputeTTL(c.in)
			if got != c.want || r != c.reason || !strings.Contains(detail, c.detail) {
				t.Errorf("ComputeTTL = %s, %q, %q; want %s, %q, a detail naming %q", got, r, detail, c.want, c.reason, c.detail)
			}
			if r != "" && !strings.Contains(detail, "ci.token.min_ttl (30 s)") {
				t.Errorf("the detail %q does not name the floor", detail)
			}
		})
	}
}
