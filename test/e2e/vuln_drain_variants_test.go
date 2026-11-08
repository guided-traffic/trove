//go:build e2e

package e2e

// E5.13: a malicious version stays blocked on every replica throughout drain variants B, C and E of R-HA-059 (variant A:
// TestVulnLong_R_VULN_015_BlockHoldsDuringFailures, variant D: TestVulnPooler_R_VULN_015_BlockHoldsThroughDrainVariantD in the set
// vuln-pooler). The node pick, the drain and the variant's own condition are the harness of drain_variants_test.go; the probes are
// those of the other blocks (blockedPathsLabeled).

import (
	"testing"
	"time"
)

// TestVulnLong_R_VULN_015_BlockHoldsThroughDrainVariants is E5.13's "a malicious version stays blocked on every replica throughout
// drain variants A to E" for B, C and E. The four versions of TestVulnLong_R_VULN_015_BlockHoldsDuringFailures (npm and NuGet, blocked
// in triage and malicious) are asked for through the edge by four loops as GET, HEAD and Range GET, 20 s before the drain and 40 s
// after it; every answer must be the refusal of R-VULN-016 from a replica, the replacement of an evicted pod included from its first
// request. Each variant's own condition is judged by the harness: the Valkey primary change reaches every replica that ran through and
// none turns unready (B), CloudNativePG switches over before the eviction (C), the backup Job completes with backoffLimit 0 (E).
func TestVulnLong_R_VULN_015_BlockHoldsThroughDrainVariants(t *testing.T) {
	e := vulnFx(t)
	probes := e.blockedPathsLabeled(t, "variants")

	for _, v := range []struct {
		variant drainVariant
		name    string
	}{
		{variantB, "R-HA-059 R-VULN-015 R-VULN-027 E5.13 drain variant B (the Valkey primary and a Sentinel on the node): every replica refuses, the replacement from its first request, the primary change reaches every replica that ran through, none turns unready"},
		{variantC, "R-HA-059 R-VULN-015 R-VULN-027 E5.13 drain variant C (the PostgreSQL primary on the node): every replica refuses, the replacement from its first request, CloudNativePG switches over before the eviction"},
		{variantE, "R-HA-059 R-VULN-015 R-VULN-027 E5.13 drain variant E (a running backup pod on the node): every replica refuses, the replacement from its first request, the backup Job completes with backoffLimit 0"},
	} {
		t.Run(v.name, func(t *testing.T) {
			troveSettled(t, 3, 3, 4)
			d := prepareVariantDrain(t, v.variant)
			// the preparation moves primaries and starts the backup Job; the probes start on a settled release, so a failure is the drain's
			troveSettled(t, 3, 3, 4)
			tally := probeDuring(t, probes, 20*time.Second, 40*time.Second, func() { d.Run(t) })
			// the three packages replicas and the replacement of the one on the node
			tally.report(t, 4)
			d.Verify(t)
			d.Settle(t)
		})
	}
}
