package app

import (
	"log/slog"

	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// checkBinaryVersion logs an ERROR when the version this binary was built with
// cannot be compared with the schema's compatibility floor. The schema check
// would fail on every retry with the same error, and the replica would wait for
// a schema that is not the problem. It reports whether the version is usable.
func checkBinaryVersion(log *slog.Logger, version string) bool {
	err := postgres.CheckBinaryVersion(version)
	if err == nil {
		return true
	}
	log.Error("this binary's version is not major.minor.patch or dev, so it can never pass the schema check and will not become ready; rebuild it with a version that is (make image sets VERSION=dev outside a v0.* tag)",
		"version", version, "error", err)
	return false
}
