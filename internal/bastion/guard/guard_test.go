package guard

import (
	"net/netip"
	"testing"
)

func TestR_BAS_019_AnIPv6AddressCountsAsItsSlash64(t *testing.T) {
	cases := []struct{ in, want string }{
		{"203.0.113.7", "203.0.113.7"},
		{"2001:db8:1:2:aaaa:bbbb:cccc:dddd", "2001:db8:1:2::/64"},
		{"2001:db8:1:2::1", "2001:db8:1:2::/64"},
		{"::ffff:203.0.113.7", "203.0.113.7"},
		{"::1", "::/64"},
	}
	for _, c := range cases {
		got, err := ParseAddressKey(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseAddressKey(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if k1, k2 := AddressKey(netip.MustParseAddr("2001:db8:1:2::1")), AddressKey(netip.MustParseAddr("2001:db8:1:3::1")); k1 == k2 {
		t.Errorf("two /64 networks share the key %s", k1)
	}
	if _, err := ParseAddressKey("not-an-address"); err == nil {
		t.Error("an unparsable address was accepted")
	}
}

func TestR_BAS_035_TheStubIsRecognizable(t *testing.T) {
	if !IsStub(Stub{}) {
		t.Fatal("IsStub(Stub{}) is false")
	}
	if !Allow().Allowed {
		t.Fatal("Allow does not admit")
	}
}
