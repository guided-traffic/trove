package config

import "time"

// OCI is the oci section (R-OCI-005, R-OCI-019): the lifetime of the registry tokens the token service issues and the grace
// period of the sweep of unreferenced blobs. The per-repository settings of the format (oci.mirror) are in the repository
// document, not here. The upload sessions the registry uses are storage.uploads (R-STOR-011).
type OCI struct {
	TokenLifetime         time.Duration `yaml:"token_lifetime" default:"300s" validate:"min=60s,max=3600s" doc:"Lifetime of a registry token (R-OCI-005); never beyond the expiry of the trove token it was issued for. A longer lifetime keeps a revoked permission alive longer (the bound of R-AUTH-023 applies to the trove token, not to this one)."`
	UnreferencedBlobGrace time.Duration `yaml:"unreferenced_blob_grace" default:"24h" validate:"min=1m" doc:"Age a blob of an image name must have before the sweep removes it for being referenced by no manifest of the name (R-OCI-019); must exceed storage.uploads.session_max_age, so a blob pushed before its manifest is never taken."`
}

// validateOCICoupling checks the OCI keys against the storage keys they depend on: the sweep must not take a blob that a
// session still open could be about to reference (R-OCI-019).
func (c *Config) validateOCICoupling(r *Report) {
	if c.OCI.UnreferencedBlobGrace <= c.Storage.Uploads.SessionMaxAge {
		r.Add("oci.unreferenced_blob_grace", "(%s) must exceed storage.uploads.session_max_age (%s) (R-OCI-019)",
			c.OCI.UnreferencedBlobGrace, c.Storage.Uploads.SessionMaxAge)
	}
}
