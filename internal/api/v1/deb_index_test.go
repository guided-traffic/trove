package v1

import "testing"

// R-DEB-014: waitFor is a list of <suite>:<changeSeq>.
func TestR_DEB_014_ParseWaitFor(t *testing.T) {
	got, err := parseWaitFor("stable:42, testing:0,")
	if err != nil || len(got) != 2 || got[0] != (waitTarget{"stable", 42}) || got[1] != (waitTarget{"testing", 0}) {
		t.Fatalf("= %v %v", got, err)
	}
	for _, bad := range []string{"stable", "stable:", ":3", "stable:x", "stable:-1", "a:1:2x"} {
		if _, err := parseWaitFor(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	long := ""
	for i := 0; i <= maxWaitFor; i++ {
		long += "s:1,"
	}
	if _, err := parseWaitFor(long); err == nil {
		t.Error("more suites than the bound were accepted")
	}
}
