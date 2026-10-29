// Package testenv gives integration tests typed access to the endpoints of the
// stack that `make integration-up` starts. Every accessor fails the test with a
// message that names the missing variable and the command that sets it; a test
// never skips for lack of its environment (an integration test without its
// dependencies is a failure, not a pass).
package testenv

import (
	"os"
	"strings"
	"testing"
)

const hint = "run `make integration-up`, then `make test-integration` (it sources .dev/integration/env)"

// Get returns the value of a TROVE_TEST_* variable, failing the test if it is unset.
func Get(tb testing.TB, name string) string {
	tb.Helper()
	v := os.Getenv(name)
	if v == "" {
		tb.Fatalf("%s is not set: the integration stack is not configured; %s", name, hint)
	}
	return v
}

// PostgresDSN is the DSN of PostgreSQL, direct.
func PostgresDSN(tb testing.TB) string { return Get(tb, "TROVE_TEST_PG_DSN") }

// PgBouncerDSN is the DSN through PgBouncer in transaction mode.
func PgBouncerDSN(tb testing.TB) string { return Get(tb, "TROVE_TEST_PG_PGBOUNCER_DSN") }

// ValkeyLine is the Valkey line under test, "9.0" or "9.1".
func ValkeyLine(tb testing.TB) string { return Get(tb, "TROVE_TEST_VALKEY_LINE") }

// Valkey describes how to reach Valkey through its Sentinels.
type Valkey struct {
	Sentinels        []string // host:port of every Sentinel
	Nodes            []string // host:port of the data nodes, for direct inspection
	PrimaryName      string   // the name in the Sentinels' monitor line
	Username         string   // the least-privilege ACL user of the example rule
	Password         string
	SentinelPassword string
	AdminUsername    string // an unrestricted user, for failover orchestration and ACL LOG
	AdminPassword    string
	CAFile           string // CA that signed the stack's certificate
	KeyPrefix        string
	Containers       []string // container names, primary first: for kill and restart
}

// ValkeyConfig returns the Valkey settings.
func ValkeyConfig(tb testing.TB) Valkey {
	tb.Helper()
	return Valkey{
		Sentinels:        split(Get(tb, "TROVE_TEST_VALKEY_SENTINELS")),
		Nodes:            split(Get(tb, "TROVE_TEST_VALKEY_NODES")),
		PrimaryName:      Get(tb, "TROVE_TEST_VALKEY_PRIMARY_NAME"),
		Username:         Get(tb, "TROVE_TEST_VALKEY_USERNAME"),
		Password:         Get(tb, "TROVE_TEST_VALKEY_PASSWORD"),
		SentinelPassword: Get(tb, "TROVE_TEST_VALKEY_SENTINEL_PASSWORD"),
		AdminUsername:    Get(tb, "TROVE_TEST_VALKEY_ADMIN_USERNAME"),
		AdminPassword:    Get(tb, "TROVE_TEST_VALKEY_ADMIN_PASSWORD"),
		CAFile:           Get(tb, "TROVE_TEST_VALKEY_CA_FILE"),
		KeyPrefix:        Get(tb, "TROVE_TEST_VALKEY_KEY_PREFIX"),
		Containers:       split(Get(tb, "TROVE_TEST_VALKEY_CONTAINERS")),
	}
}

// S3 describes the S3 gateway.
type S3 struct {
	Endpoint       string
	Region         string
	AccessKey      string
	SecretKey      string
	BlobsBucket    string // versioning on
	BackupsBucket  string
	ForcePathStyle bool
}

// S3Config returns the object-store settings.
func S3Config(tb testing.TB) S3 {
	tb.Helper()
	return S3{
		Endpoint:       Get(tb, "TROVE_TEST_S3_ENDPOINT"),
		Region:         Get(tb, "TROVE_TEST_S3_REGION"),
		AccessKey:      Get(tb, "TROVE_TEST_S3_ACCESS_KEY"),
		SecretKey:      Get(tb, "TROVE_TEST_S3_SECRET_KEY"),
		BlobsBucket:    Get(tb, "TROVE_TEST_S3_BUCKET_BLOBS"),
		BackupsBucket:  Get(tb, "TROVE_TEST_S3_BUCKET_BACKUPS"),
		ForcePathStyle: Get(tb, "TROVE_TEST_S3_FORCE_PATH_STYLE") == "true",
	}
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
