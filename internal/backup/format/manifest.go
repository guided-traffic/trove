package format

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ManifestFormat is the format number this binary writes and the highest it reads.
const ManifestFormat = 1

// Manifest is manifest.json (R-BAK-003), the last object of a backup. A reader ignores unknown fields and refuses a
// higher format.
type Manifest struct {
	Format           int         `json:"format"`
	BackupID         ID          `json:"backupId"`
	InstallationID   string      `json:"installationId"`
	Incarnation      string      `json:"incarnation"`
	Trove            TroveInfo   `json:"trove"`
	Schema           SchemaInfo  `json:"schema"`
	Postgres         PostgresRef `json:"postgres"`
	StartedAt        time.Time   `json:"startedAt"`
	SnapshotAt       time.Time   `json:"snapshotAt"`
	CompletedAt      time.Time   `json:"completedAt"`
	PreviousBackupID *ID         `json:"previousBackupId"`
	Options          Options     `json:"options"`
	Source           Source      `json:"source"`
	Tables           []TableFile `json:"tables"`
	Sequences        []Sequence  `json:"sequences"`
	Blobs            BlobsInfo   `json:"blobs"`
	Secrets          SecretsInfo `json:"secrets"`
	Encryption       Encryption  `json:"encryption"`
}

// TroveInfo names the build that took the backup.
type TroveInfo struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
}

// SchemaInfo is the migration state inside the snapshot: the highest applied step and every applied step with the SHA-256
// of its file from schema_migration (R-BAK-003, R-BAK-023).
type SchemaInfo struct {
	Step  int          `json:"step"`
	Steps []SchemaStep `json:"steps"`
}

// SchemaStep is one applied migration step.
type SchemaStep struct {
	Step   int    `json:"step"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// PostgresRef records the server the snapshot was taken on.
type PostgresRef struct {
	ServerVersion string `json:"serverVersion"`
	Isolation     string `json:"isolation"`
}

// Options are the run options that change what the backup holds.
type Options struct {
	ExcludeRemoteCaches bool `json:"excludeRemoteCaches"`
}

// Source says where the source's blob store was (no credentials) and whether its revocation journal was enabled, so that
// a restore finds the journal it must replay (R-BAK-024).
type Source struct {
	Storage           StorageRef `json:"storage"`
	RevocationJournal bool       `json:"revocationJournal"`
}

// StorageRef is the location of a blob store.
type StorageRef struct {
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
	Prefix   string `json:"prefix"`
}

// TableFile is one exported table (R-BAK-002, R-BAK-003). Columns are [name, type] pairs in the order of the file.
// SHA256, Bytes and CompressedBytes are computed while the file streams; Rows counts what COPY produced.
type TableFile struct {
	Name            string      `json:"name"`
	Class           string      `json:"class"`
	File            string      `json:"file"`
	Columns         [][2]string `json:"columns"`
	Rows            int64       `json:"rows"`
	Bytes           int64       `json:"bytes"`
	CompressedBytes int64       `json:"compressedBytes"`
	SHA256          string      `json:"sha256"`
}

// Sequence is the state of one sequence at the end of the export.
type Sequence struct {
	Name      string `json:"name"`
	LastValue int64  `json:"lastValue"`
	IsCalled  bool   `json:"isCalled"`
}

// BlobsInfo describes the blob list and the copy.
type BlobsInfo struct {
	File            string        `json:"file"`
	SHA256          string        `json:"sha256"`
	Referenced      int64         `json:"referenced"`
	ReferencedBytes int64         `json:"referencedBytes"`
	Copied          int64         `json:"copied"`
	CopiedBytes     int64         `json:"copiedBytes"`
	Damaged         []DamagedBlob `json:"damaged"`
}

// DamagedBlob is a digest whose source object was missing or had the wrong size during the run. It is in the blob list and
// not in the pool (R-BAK-006). Status is the blobs.content_status value the restore loads.
type DamagedBlob struct {
	SHA256 string `json:"sha256"`
	Status string `json:"status"`
}

// SecretsInfo lists the key IDs the sealed values of the snapshot need. Key material is never written.
type SecretsInfo struct {
	KeyIDs []string `json:"keyIds"`
}

// Encryption records the protection of the files on the target (R-BAK-026, R-BAK-027). SSE is the mode actually applied:
// "AES256", "aws:kms" or "none". ClientSide is nil or "age"; Recipients are the age public keys.
type Encryption struct {
	SSE        string   `json:"sse"`
	ClientSide *string  `json:"clientSide"`
	Recipients []string `json:"recipients,omitempty"`
}

// Encode is the JSON form of the manifest, indented, newline-terminated.
func (m *Manifest) Encode() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// DecodeManifest parses a manifest. A higher format than ManifestFormat yields an [*UnknownFormatError] (exit 6); unknown
// fields are ignored, as R-BAK-003 requires.
func DecodeManifest(b []byte) (*Manifest, error) {
	var head struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if head.Format > ManifestFormat {
		return nil, &UnknownFormatError{What: "manifest", Got: head.Format, Known: ManifestFormat}
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks the shape a reader relies on, not the content of the files.
func (m *Manifest) Validate() error {
	var errs []error
	if m.Format < 1 {
		errs = append(errs, errors.New("format must be at least 1"))
	}
	if _, err := ParseID(string(m.BackupID)); err != nil {
		errs = append(errs, err)
	}
	if m.InstallationID == "" || m.Incarnation == "" {
		errs = append(errs, errors.New("installationId and incarnation are required"))
	}
	if m.Schema.Step < 1 || len(m.Schema.Steps) == 0 {
		errs = append(errs, errors.New("schema.step and schema.steps are required"))
	}
	if m.SnapshotAt.IsZero() || m.StartedAt.IsZero() || m.CompletedAt.IsZero() {
		errs = append(errs, errors.New("startedAt, snapshotAt and completedAt are required"))
	}
	seen := map[string]bool{}
	for _, t := range m.Tables {
		switch {
		case t.Name == "" || t.File == "":
			errs = append(errs, errors.New("every table needs a name and a file"))
		case seen[t.Name]:
			errs = append(errs, fmt.Errorf("table %s is listed twice", t.Name))
		case t.Class != "data" && t.Class != "blobs":
			errs = append(errs, fmt.Errorf("table %s has class %q; only data and blobs tables are exported", t.Name, t.Class))
		case len(t.Columns) == 0:
			errs = append(errs, fmt.Errorf("table %s has no columns", t.Name))
		case len(t.SHA256) != 64:
			errs = append(errs, fmt.Errorf("table %s has no sha256", t.Name))
		}
		seen[t.Name] = true
	}
	if m.Blobs.File == "" || len(m.Blobs.SHA256) != 64 {
		errs = append(errs, errors.New("blobs.file and blobs.sha256 are required"))
	}
	for _, d := range m.Blobs.Damaged {
		if _, err := ParseDigest(d.SHA256); err != nil {
			errs = append(errs, fmt.Errorf("blobs.damaged: %w", err))
		}
		if d.Status != "missing" && d.Status != "corrupt" {
			errs = append(errs, fmt.Errorf("blobs.damaged: status %q is not missing or corrupt", d.Status))
		}
	}
	if len(m.Secrets.KeyIDs) == 0 {
		// A snapshot always holds at least the canary rows of the key ring (R-ARCH-021).
		errs = append(errs, errors.New("secrets.keyIds is empty"))
	}
	return errors.Join(errs...)
}

// DamagedSet returns the damaged digests as a set of hex strings.
func (m *Manifest) DamagedSet() map[string]string {
	out := make(map[string]string, len(m.Blobs.Damaged))
	for _, d := range m.Blobs.Damaged {
		out[d.SHA256] = d.Status
	}
	return out
}

// Table returns the entry of a table.
func (m *Manifest) Table(name string) (TableFile, bool) {
	for _, t := range m.Tables {
		if t.Name == name {
			return t, true
		}
	}
	return TableFile{}, false
}
