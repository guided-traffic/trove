package ci

import (
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/ci/reason"
)

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
