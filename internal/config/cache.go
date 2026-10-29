package config

import "time"

// Cache sizes the in-process caches (R-ARCH-018).
type Cache struct {
	RenditionBytes    ByteSize      `yaml:"rendition_bytes" default:"256MiB" validate:"min=0" doc:"Per replica: rendered package documents."`
	SnapshotSafetyTTL time.Duration `yaml:"snapshot_safety_ttl" default:"5m" validate:"min=10s" doc:"Full reload of the repository snapshot."`
	UnknownRepoKeys   int           `yaml:"unknown_repo_keys" default:"10000" validate:"min=0" doc:"Per replica: negative cache of unknown repository keys."`
}
