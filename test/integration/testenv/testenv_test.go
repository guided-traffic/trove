package testenv

import (
	"strings"
	"testing"
)

// recorder captures Fatalf of the accessor under test.
type recorder struct {
	testing.TB
	msg string
}

func (r *recorder) Helper() {}
func (r *recorder) Fatalf(format string, args ...any) {
	r.msg = strings.TrimSpace(format)
	panic(r)
}

func TestGetFailsWithAClearMessage(t *testing.T) {
	t.Run("a missing variable fails and names the variable and the fix", func(t *testing.T) {
		t.Setenv("TROVE_TEST_UNSET_FOR_THIS_TEST", "")
		r := &recorder{TB: t}
		func() {
			defer func() { _ = recover() }()
			Get(r, "TROVE_TEST_UNSET_FOR_THIS_TEST")
		}()
		if !strings.Contains(r.msg, "is not set") || !strings.Contains(r.msg, "integration") {
			t.Fatalf("message %q is not clear", r.msg)
		}
	})
	t.Run("a set variable is returned", func(t *testing.T) {
		t.Setenv("TROVE_TEST_SET_FOR_THIS_TEST", "x")
		if got := Get(t, "TROVE_TEST_SET_FOR_THIS_TEST"); got != "x" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("lists are split and trimmed", func(t *testing.T) {
		got := split(" a:1, b:2 ,,c:3")
		if len(got) != 3 || got[1] != "b:2" {
			t.Fatalf("got %v", got)
		}
	})
}
