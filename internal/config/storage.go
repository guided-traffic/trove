package config

import (
	"net/url"
	"strings"
	"time"
)

// Storage configures the content-addressed blob store and its spool
// (R-STOR-001 to R-STOR-046). The credential files of the object store are
// file-only secrets.
type Storage struct {
	S3               StorageS3               `yaml:"s3"`
	Spool            StorageSpool            `yaml:"spool"`
	Upload           StorageUpload           `yaml:"upload"`
	Uploads          StorageUploads          `yaml:"uploads" doc:"Multi-request upload sessions (P1)."`
	DeployByChecksum StorageDeployByChecksum `yaml:"deploy_by_checksum"`
	Sidecars         StorageSidecars         `yaml:"sidecars"`
	Download         StorageDownload         `yaml:"download"`
	Stats            StorageStats            `yaml:"stats"`
	Delete           StorageDelete           `yaml:"delete"`
	Properties       StorageProperties       `yaml:"properties"`
	Usage            StorageUsage            `yaml:"usage"`
	GC               StorageGC               `yaml:"gc"`
	Orphans          StorageOrphans          `yaml:"orphans"`
	Scrub            StorageScrub            `yaml:"scrub"`
}

// StorageS3 is the S3-compatible object store (R-STOR-030 to R-STOR-036).
type StorageS3 struct {
	Endpoint              string               `yaml:"endpoint" default:"" doc:"Empty means AWS endpoint resolution for the region."`
	Region                string               `yaml:"region" default:""`
	Bucket                string               `yaml:"bucket" default:"" doc:"Blob bucket; required to serve."`
	Prefix                string               `yaml:"prefix" default:"trove"`
	ForcePathStyle        bool                 `yaml:"force_path_style" default:"false" doc:"True for SeaweedFS, MinIO and Ceph RGW."`
	AllowInsecureEndpoint bool                 `yaml:"allow_insecure_endpoint" default:"false"`
	CAFile                string               `yaml:"ca_file" default:"" doc:"Empty means the system trust store."`
	StorageClass          string               `yaml:"storage_class" default:""`
	PayloadChecksum       string               `yaml:"payload_checksum" default:"sha256" validate:"oneof=sha256|md5|none"`
	Checksums             StorageS3Checksums   `yaml:"checksums"`
	Credentials           StorageS3Credentials `yaml:"credentials"`
	SSE                   StorageS3SSE         `yaml:"sse"`
	HTTP                  StorageS3HTTP        `yaml:"http"`
	Retry                 StorageS3Retry       `yaml:"retry"`
	CircuitBreaker        StorageS3Breaker     `yaml:"circuit_breaker"`
	Multipart             StorageS3Multipart   `yaml:"multipart"`
	Presign               StorageS3Presign     `yaml:"presign"`
	StartupCheck          bool                 `yaml:"startup_check" default:"true" doc:"Object-store conformance check before the first readiness (R-STOR-035)."`
}

// StorageS3Checksums are the SDK's checksum behaviours.
type StorageS3Checksums struct {
	RequestCalculation string `yaml:"request_calculation" default:"when_supported" validate:"oneof=when_supported|when_required"`
	ResponseValidation string `yaml:"response_validation" default:"when_supported" validate:"oneof=when_supported|when_required"`
}

// StorageS3Credentials selects where the object-store credential comes from
// (R-STOR-031). Every `_file` key is a file-only secret.
type StorageS3Credentials struct {
	Source               string `yaml:"source" default:"default" validate:"oneof=default|webIdentity|static"`
	RoleARN              string `yaml:"role_arn" default:""`
	WebIdentityTokenFile string `yaml:"web_identity_token_file" default:"" secret:"file"`
	STSEndpoint          string `yaml:"sts_endpoint" default:""`
	AccessKeyIDFile      string `yaml:"access_key_id_file" default:"" secret:"file"`
	SecretAccessKeyFile  string `yaml:"secret_access_key_file" default:"" secret:"file"`
	SessionTokenFile     string `yaml:"session_token_file" default:"" secret:"file"`
}

// StorageS3SSE is server-side encryption of stored objects.
type StorageS3SSE struct {
	Mode      string `yaml:"mode" default:"none" validate:"oneof=none|s3|kms"`
	KMSKeyID  string `yaml:"kms_key_id" default:""`
	BucketKey bool   `yaml:"bucket_key" default:"true"`
}

// StorageS3HTTP tunes the object store's HTTP transport (R-STOR-033).
type StorageS3HTTP struct {
	MaxConnections        int           `yaml:"max_connections" default:"256" validate:"min=1"`
	DialTimeout           time.Duration `yaml:"dial_timeout" default:"5s" validate:"min=100ms"`
	TLSHandshakeTimeout   time.Duration `yaml:"tls_handshake_timeout" default:"5s" validate:"min=100ms"`
	ResponseHeaderTimeout time.Duration `yaml:"response_header_timeout" default:"30s" validate:"min=1s"`
	IdleConnTimeout       time.Duration `yaml:"idle_conn_timeout" default:"90s"`
	IdleTransferTimeout   time.Duration `yaml:"idle_transfer_timeout" default:"60s"`
	ProxyURL              string        `yaml:"proxy_url" default:"" doc:"Empty means the environment."`
	ProxyCredentialsFile  string        `yaml:"proxy_credentials_file" default:"" secret:"file"`
	NoProxy               string        `yaml:"no_proxy" default:""`
}

// StorageS3Retry bounds SDK retries.
type StorageS3Retry struct {
	MaxAttempts int           `yaml:"max_attempts" default:"5" validate:"min=1"`
	MaxBackoff  time.Duration `yaml:"max_backoff" default:"20s" validate:"min=100ms"`
}

// StorageS3Breaker is the object store's circuit breaker.
type StorageS3Breaker struct {
	FailureThreshold int           `yaml:"failure_threshold" default:"5" validate:"min=1"`
	OpenDuration     time.Duration `yaml:"open_duration" default:"10s" validate:"min=100ms"`
}

// StorageS3Multipart configures multipart transfers.
type StorageS3Multipart struct {
	Threshold          ByteSize `yaml:"threshold" default:"100MiB" validate:"min=5MiB"`
	PartSize           ByteSize `yaml:"part_size" default:"16MiB" validate:"min=5MiB"`
	Concurrency        int      `yaml:"concurrency" default:"4" validate:"min=1" doc:"Parallel parts per upload."`
	MaxConcurrentParts int      `yaml:"max_concurrent_parts" default:"64" validate:"min=1" doc:"Per replica."`
}

// StorageS3Presign configures redirects to presigned URLs.
type StorageS3Presign struct {
	Endpoint string        `yaml:"endpoint" default:"" doc:"Empty means storage.s3.endpoint."`
	TTL      time.Duration `yaml:"ttl" default:"60s" validate:"min=1s"`
	MinSize  ByteSize      `yaml:"min_size" default:"1MiB" validate:"min=0"`
}

// StorageSpool is the replica-local scratch space (R-STOR-004, R-HA-035).
type StorageSpool struct {
	Dir             string   `yaml:"dir" default:"/var/lib/trove/spool" validate:"required"`
	MaxBytes        ByteSize `yaml:"max_bytes" default:"50GiB" validate:"min=1MiB" doc:"Budget shared by all concurrent uploads of a replica; the chart derives it."`
	MinFreeBytes    ByteSize `yaml:"min_free_bytes" default:"2GiB" validate:"min=0"`
	MemoryThreshold ByteSize `yaml:"memory_threshold" default:"1MiB" validate:"min=0"`
	WarnPercent     int      `yaml:"warn_percent" default:"80" validate:"min=1,max=100"`
}

// StorageUpload bounds one upload and its commit (R-STOR-005).
type StorageUpload struct {
	MaxSize         ByteSize      `yaml:"max_size" default:"10GiB" validate:"min=1MiB" doc:"Largest upload; must fit spool.max_bytes on the roles web and all (a worker accepts no uploads)."`
	CommitAttempts  int           `yaml:"commit_attempts" default:"5" validate:"min=1"`
	CommitDeadline  time.Duration `yaml:"commit_deadline" default:"1h" validate:"min=1s"`
	BlobWaitTimeout time.Duration `yaml:"blob_wait_timeout" default:"120s" validate:"min=1s"`
}

// StorageUploads configures multi-request upload sessions (R-STOR-011, P1).
type StorageUploads struct {
	PartSize      ByteSize      `yaml:"part_size" default:"16MiB" validate:"min=5MiB"`
	WriterLease   time.Duration `yaml:"writer_lease" default:"60s" validate:"min=1s"`
	SessionTTL    time.Duration `yaml:"session_ttl" default:"4h" validate:"min=1m"`
	SessionMaxAge time.Duration `yaml:"session_max_age" default:"20h" validate:"min=1m"`
}

// StorageDeployByChecksum configures checksum-based deploys.
type StorageDeployByChecksum struct {
	Enabled       bool `yaml:"enabled" default:"true"`
	MaxCandidates int  `yaml:"max_candidates" default:"100" validate:"min=1"`
}

// StorageSidecars configures pending sidecar declarations.
type StorageSidecars struct {
	PendingTTL time.Duration `yaml:"pending_ttl" default:"1h" validate:"min=1m"`
}

// StorageDownload tunes downloads (R-STOR-012).
type StorageDownload struct {
	BufferSize ByteSize `yaml:"buffer_size" default:"256KiB" validate:"min=4KiB"`
	Verify     bool     `yaml:"verify" default:"true"`
	HoldBack   ByteSize `yaml:"hold_back" default:"64KiB" validate:"min=0"`
}

// StorageStats batches download statistics (R-STOR-015).
type StorageStats struct {
	FlushInterval time.Duration `yaml:"flush_interval" default:"10s" validate:"min=1s"`
}

// StorageDelete bounds synchronous deletes.
type StorageDelete struct {
	MaxSyncItems int `yaml:"max_sync_items" default:"10000" validate:"min=1"`
	BatchSize    int `yaml:"batch_size" default:"5000" validate:"min=1"`
}

// StorageProperties bounds item properties.
type StorageProperties struct {
	MaxValuesPerItem  int `yaml:"max_values_per_item" default:"1000" validate:"min=1"`
	MaxRecursiveItems int `yaml:"max_recursive_items" default:"100000" validate:"min=1"`
}

// StorageUsage paces the usage fold (R-STOR-003).
type StorageUsage struct {
	FoldInterval time.Duration `yaml:"fold_interval" default:"5s" validate:"min=1s"`
}

// StorageGC configures blob garbage collection (R-STOR-024).
type StorageGC struct {
	GracePeriod       time.Duration `yaml:"grace_period" default:"24h" validate:"min=0s"`
	DeleteSettle      time.Duration `yaml:"delete_settle" default:"30s" validate:"min=0s"`
	HandoverDelay     time.Duration `yaml:"handover_delay" default:"10m" validate:"min=0s"`
	BatchSize         int           `yaml:"batch_size" default:"1000" validate:"min=1"`
	BatchDelete       bool          `yaml:"batch_delete" default:"true" doc:"False for stores without DeleteObjects."`
	DeleteConcurrency int           `yaml:"delete_concurrency" default:"8" validate:"min=1"`
}

// StorageOrphans configures the orphan sweep (R-STOR-026).
type StorageOrphans struct {
	MinAge          time.Duration `yaml:"min_age" default:"24h" validate:"min=1s"`
	MultipartMaxAge time.Duration `yaml:"multipart_max_age" default:"24h" validate:"min=1s"`
}

// StorageScrub configures integrity checks (R-STOR-027).
type StorageScrub struct {
	CheckInterval      time.Duration `yaml:"check_interval" default:"168h" validate:"min=1h"`
	DeepInterval       time.Duration `yaml:"deep_interval" default:"2160h" validate:"min=1h"`
	DeepBytesPerSecond ByteSize      `yaml:"deep_bytes_per_second" default:"50MiB" validate:"min=1KiB"`
}

// Validate checks the cross-field rules of the section.
func (s *Storage) Validate(r *Report) {
	s.validateS3Connection(r)
	if s.Upload.CommitDeadline >= s.Orphans.MinAge/2 {
		r.Add("upload.commit_deadline", "must be below orphans.min_age / 2 (R-STOR-026)")
	}
	if s.Upload.BlobWaitTimeout <= 2*s.GC.DeleteSettle {
		r.Add("upload.blob_wait_timeout", "must exceed 2 times gc.delete_settle (R-STOR-024)")
	}
	if s.Uploads.SessionMaxAge >= s.Orphans.MultipartMaxAge {
		r.Add("uploads.session_max_age", "must be below orphans.multipart_max_age")
	}
	if s.S3.Credentials.Source == "static" && (s.S3.Credentials.AccessKeyIDFile == "" || s.S3.Credentials.SecretAccessKeyFile == "") {
		r.Add("s3.credentials", "source static needs access_key_id_file and secret_access_key_file")
	}
	if s.S3.Credentials.Source == "webIdentity" && s.S3.Credentials.WebIdentityTokenFile == "" {
		r.Add("s3.credentials.web_identity_token_file", "is required with source webIdentity")
	}
	if s.S3.SSE.Mode == "kms" && s.S3.SSE.KMSKeyID == "" {
		r.Add("s3.sse.kms_key_id", "is required with mode kms")
	}
	if s.S3.Multipart.PartSize > s.S3.Multipart.Threshold {
		r.Add("s3.multipart.part_size", "must not exceed s3.multipart.threshold")
	}
}

// ValidateFor requires the blob bucket where a replica serves and checks the
// spool against the largest upload on the roles that accept uploads.
func (s *Storage) ValidateFor(cmd Command, r *Report) {
	if !cmd.runsReplica() {
		return
	}
	if s.S3.Bucket == "" {
		r.Add("s3.bucket", "is required")
	}
	// Only web (and all, which runs both) accepts uploads from clients; a worker
	// spools reports and remote-cache fills, which R-HA-029 does not size to
	// upload.max_size. `config validate` runs without a role and checks nothing
	// here; `serve` always has one.
	if role := r.Role(); role == "web" || role == "all" {
		if s.Upload.MaxSize > s.Spool.MaxBytes {
			r.Add("upload.max_size", "must not exceed spool.max_bytes (is %s, spool.max_bytes is %s)", s.Upload.MaxSize, s.Spool.MaxBytes)
		}
	}
}

// validateS3Connection applies the startup validation of R-STOR-030: the prefix
// and the endpoint.
func (s *Storage) validateS3Connection(r *Report) {
	p := s.S3.Prefix
	switch {
	case strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/"):
		r.Add("s3.prefix", "must not start or end with /")
	case strings.Contains(p, "//"):
		r.Add("s3.prefix", "must not contain //")
	case len(p) > 256:
		r.Add("s3.prefix", "must be at most 256 characters")
	default:
		for _, c := range p {
			if !isKeyChar(c) {
				r.Add("s3.prefix", "contains %q; allowed are letters, digits and !_.*'()/-", c)
				break
			}
		}
	}
	if e := s.S3.Endpoint; e != "" {
		u, err := url.Parse(e)
		switch {
		case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
			r.Add("s3.endpoint", "must be an absolute https URL (is %q)", e)
		case u.Scheme == "http" && !s.S3.AllowInsecureEndpoint:
			r.Add("s3.endpoint", "uses http: set s3.allow_insecure_endpoint to allow it")
		}
	}
}

// isKeyChar reports a character the key prefix may contain (R-STOR-030).
func isKeyChar(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!_.*'()/-", c)
}
