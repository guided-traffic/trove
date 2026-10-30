package npm

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats/npm/publish"
)

// R-NPM-007 step 2: a publish whose parts are each within their limit (the tarball, the metadata
// budget and a provenance bundle of the largest size) is within the cap of the body.
func TestR_NPM_007_BodyLimitHoldsEveryPartAtItsLimit(t *testing.T) {
	const maxTarball = 256 << 20
	parts := base64Len(maxTarball) + publish.DefaultMaxMetadataBytes + base64Len(maxSigstoreBytes)
	if got := bodyLimit(maxTarball); got != parts {
		t.Fatalf("the cap %d is not the sum %d of the parts at their limits", got, parts)
	}
	if want := int64(768<<10 + 349528); bodySlack != want { // 349,528 is the base64 form of 256 KiB
		t.Fatalf("the slack is %d, want 768 KiB of metadata and the %d of a bundle: %d", bodySlack, 349528, want)
	}
}
