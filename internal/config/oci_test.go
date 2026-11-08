package config

import (
	"testing"
	"time"
)

// The oci section and the storage.uploads keys it depends on. No requirement ID names the tests: the keys are read by the work
// packages of the token service, the sweep and the sessions, which name R-OCI-005, R-OCI-019 and R-STOR-011 when they are built.

func TestOCIDefaults(t *testing.T) {
	c := mustLoad(t, Options{Environ: servableEnv(), Command: CommandServe}).Config
	if c.OCI.TokenLifetime != 300*time.Second || c.OCI.UnreferencedBlobGrace != 24*time.Hour {
		t.Errorf("oci = %+v", c.OCI)
	}
	u := c.Storage.Uploads
	if u.PartSize != 16*MiB || u.WriterLease != 60*time.Second || u.SessionTTL != 4*time.Hour || u.SessionMaxAge != 20*time.Hour {
		t.Errorf("storage.uploads = %+v", u)
	}
}

func TestOCIRanges(t *testing.T) {
	cases := []struct {
		name, yaml, key string
		bad             bool
	}{
		{"a token lifetime below 60s", "oci:\n  token_lifetime: 59s\n", "oci.token_lifetime", true},
		{"a token lifetime of 60s", "oci:\n  token_lifetime: 60s\n", "oci.token_lifetime", false},
		{"a token lifetime of one hour", "oci:\n  token_lifetime: 3600s\n", "oci.token_lifetime", false},
		{"a token lifetime above one hour", "oci:\n  token_lifetime: 3601s\n", "oci.token_lifetime", true},
		{"a grace of 0", "oci:\n  unreferenced_blob_grace: 0s\n", "oci.unreferenced_blob_grace", true},
		{"a grace at the session age", "oci:\n  unreferenced_blob_grace: 20h\n", "oci.unreferenced_blob_grace", true},
		{"a grace above the session age", "oci:\n  unreferenced_blob_grace: 21h\n", "oci.unreferenced_blob_grace", false},
		{
			"a grace above a raised session age",
			"storage:\n  uploads:\n    session_max_age: 22h\n  orphans:\n    multipart_max_age: 48h\noci:\n  unreferenced_blob_grace: 21h\n",
			"oci.unreferenced_blob_grace", true,
		},
		{"a session age at the multipart age", "storage:\n  uploads:\n    session_max_age: 24h\n", "storage.uploads.session_max_age", true},
		{"a writer lease at the session ttl", "storage:\n  uploads:\n    writer_lease: 4h\n", "storage.uploads.writer_lease", true},
		{"a writer lease of 1s with the default part size is sendable at 22 MiB/s", "storage:\n  uploads:\n    writer_lease: 1s\n", "storage.uploads.writer_lease", false},
		{"no open session per principal and repository", "storage:\n  uploads:\n    max_open_sessions: 0\n", "storage.uploads.max_open_sessions", true},
		{"a writer lease below the session ttl", "storage:\n  uploads:\n    writer_lease: 3h\n", "storage.uploads.writer_lease", false},
		{"a part size above 5GiB, the largest part of an S3 upload", "storage:\n  uploads:\n    part_size: 6GiB\n", "storage.uploads.part_size", true},
		{"a part size of 5GiB", "storage:\n  uploads:\n    part_size: 5GiB\n    writer_lease: 4m\n  upload:\n    max_size: 10GiB\n", "storage.uploads.writer_lease", false},
		{"a part of 5GiB with the default lease cannot be sent in time", "storage:\n  uploads:\n    part_size: 5GiB\n  upload:\n    max_size: 10GiB\n", "storage.uploads.writer_lease", true},
		{"a part size below 5MiB", "storage:\n  uploads:\n    part_size: 4MiB\n", "storage.uploads.part_size", true},
		{"a maximum upload that needs more than 10,000 parts", "storage:\n  upload:\n    max_size: 200GiB\n", "storage.upload.max_size", true},
		{"a maximum upload of 10,000 parts", "storage:\n  upload:\n    max_size: 160000MiB\n  spool:\n    max_bytes: 200000MiB\n", "storage.upload.max_size", false},
		{"a larger part size lifts the cap", "storage:\n  upload:\n    max_size: 200GiB\n  spool:\n    max_bytes: 300GiB\n  uploads:\n    part_size: 32MiB\n", "storage.upload.max_size", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadM2(t, c.yaml)
			if got := hasIssue(err, c.key); got != c.bad {
				t.Fatalf("issue on %s = %v, want %v (%v)", c.key, got, c.bad, err)
			}
		})
	}
}

func TestOCIEnvironment(t *testing.T) {
	l := mustLoad(t, Options{Environ: servableEnv("TROVE_OCI_TOKEN_LIFETIME=600s"), Command: CommandServe})
	if l.Config.OCI.TokenLifetime != 600*time.Second || l.Sources["oci.token_lifetime"].Kind != "env" {
		t.Errorf("token_lifetime = %s, source = %+v", l.Config.OCI.TokenLifetime, l.Sources["oci.token_lifetime"])
	}
}
