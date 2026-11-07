package config

import (
	"net/url"
	"strings"
	"time"
)

// Backup configures trove's own backup and restore (R-BAK-011) and the revocation journal (R-HA-054). The target
// credentials are file-only secrets; web and worker replicas read expected_interval and manual_run and never the target
// (R-BAK-011, R-BAK-015). recovery_window and object_versioning declare the platform protection that the task backup-check verifies (R-HA-023).
type Backup struct {
	Target            BackupTarget            `yaml:"target"`
	Database          BackupDatabase          `yaml:"database"`
	Copy              BackupCopy              `yaml:"copy"`
	CompressionLevel  int                     `yaml:"compression_level" default:"3" validate:"min=1,max=19" doc:"zstd level of table files and the blob list."`
	Exclude           BackupExclude           `yaml:"exclude"`
	Retention         BackupRetention         `yaml:"retention"`
	ExpectedInterval  time.Duration           `yaml:"expected_interval" default:"24h" validate:"min=1m" doc:"How often a backup is expected. TroveBackupStale and the overdue mark of the API and the UI fire after 1.5 times this value (R-BAK-014)."`
	ManualRun         BackupManualRun         `yaml:"manual_run"`
	LeaseDuration     time.Duration           `yaml:"lease_duration" default:"5m" validate:"min=5s"`
	HeartbeatInterval time.Duration           `yaml:"heartbeat_interval" default:"15s" validate:"min=1s"`
	StallTimeout      time.Duration           `yaml:"stall_timeout" default:"30m" validate:"min=1m" doc:"A run without progress for this long cancels itself and exits 75 (R-BAK-013)."`
	FinalizeTimeout   time.Duration           `yaml:"finalize_timeout" default:"30s" validate:"min=1s" doc:"Request deadline of the manifest write; twice this value must stay below lease_duration (R-BAK-010)."`
	VerifySample      int                     `yaml:"verify_sample" default:"1000" validate:"min=0" doc:"Pool objects HEAD-checked before each manifest."`
	RunHistory        time.Duration           `yaml:"run_history" default:"2160h" validate:"min=1h" doc:"backup_run rows older than this are deleted by the next run."`
	RevocationJournal BackupRevocationJournal `yaml:"revocation_journal"`
	Source            BackupSource            `yaml:"source"`
	RecoveryWindow    time.Duration           `yaml:"recovery_window" default:"0s" validate:"min=0s" doc:"The point-in-time recovery window the platform keeps (R-HA-023); 0s is undeclared. The task backup-check verifies it and a replica refuses to start when it exceeds storage.gc.grace_period without object_versioning (R-HA-024)."`
	ObjectVersioning  bool                    `yaml:"object_versioning" default:"false" doc:"Asserts that the blob bucket is versioned and keeps noncurrent versions for at least recovery_window (R-HA-023)."`
}

// BackupTarget is the S3 location backups are written to (R-BAK-002, R-BAK-011). It carries the keys of storage.s3 that a
// second bucket needs; the target has its own client and its own credentials.
type BackupTarget struct {
	Endpoint              string               `yaml:"endpoint" default:"" doc:"Empty means AWS endpoint resolution for the region. A host without a scheme means https://."`
	Region                string               `yaml:"region" default:""`
	Bucket                string               `yaml:"bucket" default:"" doc:"Required by every backup command."`
	Prefix                string               `yaml:"prefix" default:"trove"`
	ForcePathStyle        bool                 `yaml:"force_path_style" default:"false"`
	AllowInsecureEndpoint bool                 `yaml:"allow_insecure_endpoint" default:"false"`
	CAFile                string               `yaml:"ca_file" default:"" doc:"Empty means the system trust store."`
	StorageClass          string               `yaml:"storage_class" default:""`
	AllowSameBucket       bool                 `yaml:"allow_same_bucket" default:"false" doc:"Allows the target on the blob store's endpoint and bucket, for single-bucket evaluation setups; the prefixes still must not contain each other (R-BAK-002)."`
	Credentials           StorageS3Credentials `yaml:"credentials"`
	SSE                   BackupSSE            `yaml:"sse"`
}

// BackupSource is the credential with which the backup reads the blob bucket (R-BAK-015: the backup never writes or deletes
// there). Without separate_credentials the backup reads with storage.s3.credentials, which is the right choice for a
// workload identity of the backup's ServiceAccount; with static keys the chart sets it, so the backup pod does not mount the
// runtime credential (Q-BAK-49).
type BackupSource struct {
	SeparateCredentials bool                 `yaml:"separate_credentials" default:"false" doc:"Read the blob bucket with backup.source.credentials instead of storage.s3.credentials."`
	Credentials         StorageS3Credentials `yaml:"credentials"`
}

// BackupSSE is the server-side encryption of the target (R-BAK-026). The default is s3, unlike the blob store's none.
type BackupSSE struct {
	Mode      string `yaml:"mode" default:"s3" validate:"oneof=s3|kms|none"`
	KMSKeyID  string `yaml:"kms_key_id" default:""`
	BucketKey bool   `yaml:"bucket_key" default:"true"`
}

// BackupDatabase tunes the table export (R-BAK-005).
type BackupDatabase struct {
	Parallel      int           `yaml:"parallel" default:"2" validate:"min=1" doc:"Export connections besides the snapshot connection."`
	LockTimeout   time.Duration `yaml:"lock_timeout" default:"60s" validate:"min=1s" doc:"Wait for the table locks of the snapshot."`
	MigrationWait time.Duration `yaml:"migration_wait" default:"15m" validate:"min=1s" doc:"Wait for a running migration's lock."`
	PartSize      ByteSize      `yaml:"part_size" default:"16MiB" validate:"min=5MiB" doc:"Multipart part size of table files."`
}

// BackupCopy tunes the blob copy into the pool (R-BAK-006).
type BackupCopy struct {
	Concurrency    int      `yaml:"concurrency" default:"16" validate:"min=1"`
	ServerSide     string   `yaml:"server_side" default:"auto" validate:"oneof=auto|always|never"`
	PartSize       ByteSize `yaml:"part_size" default:"256MiB" validate:"min=5MiB" doc:"Raised so that an object fits 10,000 parts."`
	CheckExisting  string   `yaml:"check_existing" default:"auto" validate:"oneof=auto|always|never"`
	BytesPerSecond ByteSize `yaml:"bytes_per_second" default:"0" validate:"min=0" doc:"Transfer rate cap per run; 0 is unlimited."`
}

// BackupExclude lists what a backup leaves out (R-BAK-008).
type BackupExclude struct {
	RemoteCaches bool `yaml:"remote_caches" default:"false" doc:"Leave out blobs that only remote-repository caches reference."`
}

// BackupRetention is the retention policy (R-BAK-016, R-BAK-017).
type BackupRetention struct {
	KeepLast      int           `yaml:"keep_last" default:"7" validate:"min=1"`
	KeepWithin    time.Duration `yaml:"keep_within" default:"0s" validate:"min=0" doc:"0s means count only."`
	SweepInterval time.Duration `yaml:"sweep_interval" default:"168h" validate:"min=1h" doc:"A full pool sweep at least this often."`
}

// BackupManualRun is what web replicas show for a manual run (R-API-050, R-UI-048). The chart sets both.
type BackupManualRun struct {
	Namespace string `yaml:"namespace" default:""`
	CronJob   string `yaml:"cronjob" default:""`
}

// BackupRevocationJournal configures the revocation journal in object storage (R-HA-054).
type BackupRevocationJournal struct {
	Enabled      bool          `yaml:"enabled" default:"true"`
	ShipInterval time.Duration `yaml:"ship_interval" default:"5s" validate:"min=1s"`
	ReplayMargin time.Duration `yaml:"replay_margin" default:"5m" validate:"min=1s" doc:"Longer than any transaction (R-HA-015 timeouts)."`
}

// Validate applies the rules of R-BAK-011 that span the section's own keys.
func (b *Backup) Validate(r *Report) {
	if b.HeartbeatInterval*4 > b.LeaseDuration {
		r.Add("heartbeat_interval", "(%s) times 4 must not exceed backup.lease_duration (%s)", b.HeartbeatInterval, b.LeaseDuration)
	}
	if b.FinalizeTimeout*2 >= b.LeaseDuration {
		r.Add("finalize_timeout", "(%s) times 2 must stay below backup.lease_duration (%s)", b.FinalizeTimeout, b.LeaseDuration)
	}
	if b.Database.MigrationWait >= b.StallTimeout {
		r.Add("database.migration_wait", "(%s) must stay below backup.stall_timeout (%s)", b.Database.MigrationWait, b.StallTimeout)
	}
	if s := b.Source; s.SeparateCredentials && s.Credentials.Source == "static" && (s.Credentials.AccessKeyIDFile == "" || s.Credentials.SecretAccessKeyFile == "") {
		r.Add("source.credentials", "source static needs access_key_id_file and secret_access_key_file")
	}
	checkKeyPrefix(r, "target.prefix", b.Target.Prefix)
	t := b.Target
	rejectUserinfo(r, "target.endpoint", NormalizeEndpoint(t.Endpoint), "backup.target.credentials.access_key_id_file")
	rejectUserinfo(r, "target.credentials.sts_endpoint", t.Credentials.STSEndpoint, "")
	if e := NormalizeEndpoint(t.Endpoint); e != "" {
		u, err := url.Parse(e)
		switch {
		case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
			r.Add("target.endpoint", "must be an absolute https URL (is %q)", redactUserinfo(e))
		case u.Scheme == "http" && !t.AllowInsecureEndpoint:
			r.Add("target.endpoint", "uses http: set backup.target.allow_insecure_endpoint to allow it")
		}
	}
	if t.Credentials.Source == "static" && (t.Credentials.AccessKeyIDFile == "" || t.Credentials.SecretAccessKeyFile == "") {
		r.Add("target.credentials", "source static needs access_key_id_file and secret_access_key_file")
	}
	if t.Credentials.Source == "webIdentity" && t.Credentials.WebIdentityTokenFile == "" {
		r.Add("target.credentials.web_identity_token_file", "is required with source webIdentity")
	}
	if t.SSE.Mode == "kms" && t.SSE.KMSKeyID == "" {
		r.Add("target.sse.kms_key_id", "is required with mode kms")
	}
	if b.Copy.PartSize < 5*MiB {
		r.Add("copy.part_size", "must be at least 5MiB")
	}
}

// ValidateFor requires the target bucket where a backup command runs (R-BAK-011).
func (b *Backup) ValidateFor(cmd Command, r *Report) {
	if cmd == CommandBackup && b.Target.Bucket == "" {
		r.Add("target.bucket", "is required by every backup command")
	}
}

// validateBackupCoupling applies the rule of R-BAK-002 that spans backup.target and storage.s3: the target must not share
// the blob store's endpoint and bucket unless allow_same_bucket is set, and even then neither prefix may contain the
// other. It only runs where a target is configured.
func (c *Config) validateBackupCoupling(r *Report) {
	t, s := c.Backup.Target, c.Storage.S3
	if t.Bucket == "" || s.Bucket == "" || t.Bucket != s.Bucket {
		return
	}
	if NormalizeEndpoint(t.Endpoint) != NormalizeEndpoint(s.Endpoint) {
		return
	}
	if !t.AllowSameBucket {
		r.Add("backup.target.bucket", "(%s) is the blob store's bucket on the same endpoint; use a bucket of its own or set backup.target.allow_same_bucket (R-BAK-002)", t.Bucket)
		return
	}
	if prefixContains(t.Prefix, s.Prefix) || prefixContains(s.Prefix, t.Prefix) {
		r.Add("backup.target.prefix", "(%q) and storage.s3.prefix (%q) must not contain each other (R-BAK-002)", t.Prefix, s.Prefix)
	}
}

// validateRecoveryWindow applies the startup rule of R-HA-024: a declared recovery window longer than the garbage-collection
// grace period is safe only when the bucket keeps deleted objects as noncurrent versions.
func (c *Config) validateRecoveryWindow(r *Report) {
	b := c.Backup
	if b.RecoveryWindow > 0 && b.RecoveryWindow > c.Storage.GC.GracePeriod && !b.ObjectVersioning {
		r.Add("backup.recovery_window", "(%s) exceeds storage.gc.grace_period (%s): a restore to an older point could reference a deleted object; raise the grace period or keep deleted objects and set backup.object_versioning: true (R-HA-024)", b.RecoveryWindow, c.Storage.GC.GracePeriod)
	}
}

// prefixContains reports whether key prefix a is b or a parent of b. An empty prefix contains every prefix.
func prefixContains(a, b string) bool {
	return a == "" || a == b || strings.HasPrefix(b, a+"/")
}
