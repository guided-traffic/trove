package errs

import (
	"errors"
	"testing"
)

func TestR_API_012_StaleIsOneErrorWithTheCurrentTag(t *testing.T) {
	err := Stale("The thing", 7)
	e, ok := As(err)
	if !ok || e.Status() != 412 || e.Code != CodePreconditionFailed {
		t.Fatalf("err = %#v", err)
	}
	if e.Extensions["currentEtag"] != `"7"` {
		t.Errorf("currentEtag = %v", e.Extensions["currentEtag"])
	}
	if cur, ok := CurrentRevision(err); !ok || cur != 7 {
		t.Errorf("CurrentRevision = %d, %v", cur, ok)
	}
	var st *StaleRevision
	if !errors.As(err, &st) {
		t.Error("the cause is not a *StaleRevision")
	}
	if _, ok := CurrentRevision(errors.New("other")); ok {
		t.Error("CurrentRevision accepted another error")
	}
}

func TestR_API_012_NoRevisionIsBelowOne(t *testing.T) {
	if AnyRevision >= 0 {
		t.Fatalf("AnyRevision = %d collides with a stored revision", AnyRevision)
	}
}
