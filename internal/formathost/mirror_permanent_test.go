package formathost

import (
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/mirror"
)

// R-REPO-041, R-REPO-053: an artifact that came back with the wrong digest, or whose document announces nothing it could be
// compared with, fails its package at once; other upstream failures stay transient.
func TestR_REPO_053_AnIntegrityFailureOfAFillIsPermanentForThePass(t *testing.T) {
	for _, code := range []string{errs.CodeUpstreamIntegrityMismatch, errs.CodeUpstreamDigestMissing} {
		err := errs.E(errs.UpstreamFailed, code, "x")
		var perm *mirror.PermanentError
		if !errors.As(mapFill(err), &perm) {
			t.Errorf("mapFill(%s) is not permanent", code)
		}
		if code == errs.CodeUpstreamDigestMissing && !errors.As(mapGate(err), &perm) {
			t.Errorf("mapGate(%s) is not permanent", code)
		}
	}
	if err := errs.E(errs.UpstreamFailed, errs.CodeUpstreamUnreachable, "x"); errors.As(mapFill(err), new(*mirror.PermanentError)) {
		t.Error("an unreachable upstream is permanent")
	}
}
