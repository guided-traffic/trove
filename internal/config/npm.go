package config

import "time"

// NPM is the npm section (R-NPM-007, R-NPM-011, R-NPM-013): the limits of a publish
// and the cache headers of package documents. The per-repository settings of the
// format (latestGuard, serveRemovedVersions, the virtual strategies) are in the
// repository document, not here. The keys of the trusted publishing exchange
// (R-NPM-022) and of the audit forwarding (R-NPM-038) arrive with their
// requirements.
type NPM struct {
	Publish NPMPublish `yaml:"publish"`

	AbbreviatedTime bool          `yaml:"abbreviated_time" default:"true" doc:"Add the time map to abbreviated package documents, which pnpm and Yarn need for release-age policies (R-NPM-011)."`
	DocumentMaxAge  time.Duration `yaml:"document_max_age" default:"0s" validate:"min=0s,max=24h" doc:"max-age of the Cache-Control header of package documents; 0 makes clients revalidate every time, which a 304 makes cheap (R-NPM-013)."`
}

// NPMPublish bounds one npm publish (R-NPM-007).
type NPMPublish struct {
	MaxTarballSize      ByteSize      `yaml:"max_tarball_size" default:"256MiB" validate:"min=1KiB" doc:"Largest tarball of a publish; at most storage.upload.max_size."`
	MaxUnpackedSize     ByteSize      `yaml:"max_unpacked_size" default:"2GiB" validate:"min=1KiB" doc:"Largest sum of the unpacked entry sizes of a tarball."`
	MaxTarEntries       int           `yaml:"max_tar_entries" default:"100000" validate:"min=1,max=10000000" doc:"Most entries in one tarball."`
	ManifestConsistency string        `yaml:"manifest_consistency" default:"reject" validate:"oneof=reject|warn" doc:"A request manifest that differs from the tarball's package.json: reject answers 400 npm_manifest_mismatch, warn stores the tarball's values."`
	RetryWindow         time.Duration `yaml:"retry_window" default:"10m" validate:"min=0s,max=24h" doc:"How long a repeated publish of identical bytes is acknowledged as the first one (Q-NPM-11); 0s turns the acknowledgement off."`
}

// validateNPMCoupling checks the npm limits against the storage limits they sit
// inside: a tarball larger than an upload may be could never be spooled.
func (c *Config) validateNPMCoupling(r *Report) {
	if c.NPM.Publish.MaxTarballSize > c.Storage.Upload.MaxSize {
		r.Add("npm.publish.max_tarball_size", "(%s) must not exceed storage.upload.max_size (%s): the storage limit refuses the upload first",
			c.NPM.Publish.MaxTarballSize, c.Storage.Upload.MaxSize)
	}
}
