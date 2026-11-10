//go:build e2e

package e2e

// R-REPO-058 on kind (set `nuget-ha`): the status of a NuGet mirror from every replica that serves it. The npm mirror's is in
// npm_ha_worker_pool_test.go.

import "testing"

// TestNuGetHA_R_REPO_058_MirrorStatusFromEveryReplica: the status operation of a NuGet mirror answers the same on every replica that
// serves it after a complete pass.
func TestNuGetHA_R_REPO_058_MirrorStatusFromEveryReplica(t *testing.T) {
	e := mirrorFx(t)
	key := e.needNuGetMirror(t)
	assertStatusFromEveryReplica(t, e, key)
	e.jobs.check(t)
}
