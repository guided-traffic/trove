package auth

import "testing"

func TestR_AUTH_038_AddressBucket(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"203.0.113.7", "203.0.113.7"},
		{"::ffff:203.0.113.7", "203.0.113.7"},
		{"2001:db8:1:2::1", "2001:db8:1:2::/64"},
		{"2001:db8:1:2:ffff:ffff:ffff:ffff", "2001:db8:1:2::/64"},
		{"fe80::1%eth0", "fe80::/64"},
		{"", ""},
		{"not-an-address", "not-an-address"},
	} {
		t.Run("R-AUTH-038 Q-AUTH-90 "+c.in, func(t *testing.T) {
			if got := AddressBucket(c.in); got != c.want {
				t.Fatalf("AddressBucket(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
	t.Run("R-AUTH-038 two /64 networks are two buckets", func(t *testing.T) {
		if AddressBucket("2001:db8:1:2::1") == AddressBucket("2001:db8:1:3::1") {
			t.Fatal("neighbouring /64 networks share a bucket")
		}
	})
}
