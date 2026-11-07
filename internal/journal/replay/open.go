package replay

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"

	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
)

// LoadCredentials reads the credentials file of `--credentials-file` and `--journal-credentials-file` (R-HA-025 step 5,
// R-BAK-015 "Journal reader"): a YAML document with the keys of storage.s3.credentials (source, access_key_id_file,
// secret_access_key_file, session_token_file, role_arn, web_identity_token_file, sts_endpoint). Unknown keys are refused, so
// a misspelled key cannot silently fall back to the runtime credential.
func LoadCredentials(path string) (config.StorageS3Credentials, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the operator names the credentials file on the command line
	if err != nil {
		return config.StorageS3Credentials{}, fmt.Errorf("reading the credentials file: %w", err)
	}
	creds := config.StorageS3Credentials{Source: "default"}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&creds); err != nil {
		return config.StorageS3Credentials{}, fmt.Errorf("the credentials file %s: %w", path, err)
	}
	return creds, nil
}

// OpenJournal builds the reader of the journal in the bucket s3cfg describes. With a credentials file the client uses the
// credentials of the file and lists object versions as well, so a delete marker set with the runtime credential cannot hide an
// entry (R-HA-054); without one it uses the blob-store credentials in s3cfg and the plain listing, because the runtime
// credential deliberately lacks s3:ListBucketVersions (R-HA-023).
func OpenJournal(ctx context.Context, s3cfg config.StorageS3, out *outbound.Factory, credentialsFile string) (*Reader, error) {
	versions := credentialsFile != ""
	if versions {
		creds, err := LoadCredentials(credentialsFile)
		if err != nil {
			return nil, err
		}
		s3cfg.Credentials = creds
	}
	client, err := s3.NewFromFactory(ctx, s3cfg, out)
	if err != nil {
		return nil, fmt.Errorf("journal store: %w", err)
	}
	return NewReader(s3.NewJournalStore(client, versions), s3cfg.Prefix), nil
}
