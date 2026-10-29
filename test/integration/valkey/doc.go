// Package valkey holds the integration tests of the Valkey client in
// internal/cluster (R-ARCH-030, R-ARCH-031, R-HA-056, R-HA-057) against the
// Valkey primary, replica and three Sentinels of the integration stack. They
// carry the integration build tag; see test/integration/testenv for the
// environment they need.
//
// The tests that force a failover return the stack to its initial layout
// before they end. The version tests start throwaway standalone containers of
// the images pinned in tools/versions.env, so they need the Docker CLI.
package valkey
