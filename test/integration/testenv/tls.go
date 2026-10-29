package testenv

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

// Every connection of the integration stack is TLS, verified against the stack's own CA
// (INV-20, R-HA-063). The accessors below hand out the CA and the TLS settings of each
// dependency; [Main] and [RequireTLS] fail a suite whose environment names a plaintext endpoint.

// CAFile is the path of the CA certificate that signed every certificate of the stack
// (TROVE_TEST_CA_FILE).
func CAFile(tb testing.TB) string {
	tb.Helper()
	return Get(tb, "TROVE_TEST_CA_FILE")
}

// CertDir is the directory that holds the stack's certificates and keys
// (TROVE_TEST_CERT_DIR); see test/integration/certs.sh for the layout.
func CertDir(tb testing.TB) string {
	tb.Helper()
	return Get(tb, "TROVE_TEST_CERT_DIR")
}

// RootCAs is a pool with only the stack's CA: the system roots are deliberately not part of it,
// so a client built from it cannot be satisfied by a certificate from anywhere else.
func RootCAs(tb testing.TB) *x509.CertPool {
	tb.Helper()
	pem, err := os.ReadFile(CAFile(tb))
	if err != nil {
		tb.Fatalf("read TROVE_TEST_CA_FILE: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		tb.Fatalf("TROVE_TEST_CA_FILE %s holds no certificate", CAFile(tb))
	}
	return pool
}

// TLSConfig is a client configuration that verifies the server against [RootCAs] (TLS 1.2 or later).
func TLSConfig(tb testing.TB) *tls.Config {
	tb.Helper()
	return &tls.Config{RootCAs: RootCAs(tb), MinVersion: tls.VersionTLS12}
}

// PgDSN is a verify-full DSN of the stack's PostgreSQL (direct) for the database name, using the
// stack's superuser. The CA is in the DSN, so a pgx client needs nothing else.
func PgDSN(tb testing.TB, database string) string {
	tb.Helper()
	return pgDSN(tb, Get(tb, "TROVE_TEST_PG_PORT"), database)
}

// PgBouncerDSNFor is the same through PgBouncer for the database name.
func PgBouncerDSNFor(tb testing.TB, database string) string {
	tb.Helper()
	return pgDSN(tb, Get(tb, "TROVE_TEST_PG_PGBOUNCER_PORT"), database)
}

func pgDSN(tb testing.TB, port, database string) string {
	tb.Helper()
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(Get(tb, "TROVE_TEST_PG_USER"), Get(tb, "TROVE_TEST_PG_PASSWORD")),
		Host:     Get(tb, "TROVE_TEST_PG_HOST") + ":" + port,
		Path:     "/" + database,
		RawQuery: url.Values{"sslmode": {"verify-full"}, "sslrootcert": {CAFile(tb)}}.Encode(),
	}
	return u.String()
}

// SecureDatabase sets the TLS settings of a trove database configuration: ssl_mode verify-full
// and ssl_root_cert_file pointing at the stack's CA. It never touches allow_insecure.
func SecureDatabase(tb testing.TB, cfg config.Database) config.Database {
	tb.Helper()
	cfg.SSLMode = "verify-full"
	cfg.SSLRootCertFile = CAFile(tb)
	cfg.AllowInsecure = false
	return cfg
}

// SecureS3 sets the TLS settings of a trove S3 storage configuration: an https endpoint and
// ca_file pointing at the stack's CA, with the insecure-endpoint switch off.
func SecureS3(tb testing.TB, cfg config.StorageS3) config.StorageS3 {
	tb.Helper()
	cfg.Endpoint = S3Config(tb).Endpoint
	// Under TROVE_TEST_SCOPE=s3 the store may be one with a publicly trusted certificate (AWS S3):
	// without a stack CA the system roots verify it. Every other run has the stack's CA.
	if os.Getenv("TROVE_TEST_SCOPE") != "s3" || os.Getenv("TROVE_TEST_CA_FILE") != "" {
		cfg.CAFile = CAFile(tb)
	}
	cfg.AllowInsecureEndpoint = false
	return cfg
}

// RequireTLS fails tb when the environment names an endpoint that is not TLS-verified: a
// PostgreSQL DSN without sslmode=verify-full and sslrootcert, an S3 endpoint that is not https, a
// missing or unreadable CA file. A variable that is unset fails too; the stack is not configured.
func RequireTLS(tb testing.TB) {
	tb.Helper()
	if err := checkTLS(os.Getenv); err != nil {
		tb.Fatalf("%v", err)
	}
}

// Main is the TestMain body of an integration suite: it runs [RequireTLS]'s check once before
// any test and exits non-zero with the reason if it fails. Use it as
//
//	func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }
func Main(m *testing.M) int {
	if err := CheckTLS(); err != nil {
		fmt.Fprintf(os.Stderr, "integration environment: %v\n", err)
		return 1
	}
	return m.Run()
}

// CheckTLS is the check [Main] runs, for a suite whose TestMain does more than run the tests.
func CheckTLS() error { return checkTLS(os.Getenv) }

// checkTLS validates the TLS side of the environment through get (os.Getenv in production).
//
// TROVE_TEST_SCOPE selects what the environment must provide. Unset (or "stack") is the
// integration stack of make integration-up: every endpoint must be set and TLS-verified. "s3" is
// the nightly conformance leg against another store (MinIO, AWS S3), which runs only the
// Conformance tests and provides only the TROVE_TEST_S3_* variables: the S3 endpoint must be https,
// and a CA file, when set, must be readable; without one the system roots verify the store.
func checkTLS(get func(string) string) error {
	hint := "; run `make integration-up`, then `make test-integration`"
	switch scope := get("TROVE_TEST_SCOPE"); scope {
	case "", "stack":
	case "s3":
		if ca := get("TROVE_TEST_CA_FILE"); ca != "" {
			if st, err := os.Stat(ca); err != nil || st.IsDir() {
				return fmt.Errorf("TROVE_TEST_CA_FILE %s is not a readable file", ca)
			}
		}
		return checkS3Endpoint(get, "")
	default:
		return fmt.Errorf("TROVE_TEST_SCOPE %q is not stack or s3", scope)
	}
	ca := get("TROVE_TEST_CA_FILE")
	if ca == "" {
		return fmt.Errorf("TROVE_TEST_CA_FILE is not set: the integration stack is not configured for TLS%s", hint)
	}
	if st, err := os.Stat(ca); err != nil || st.IsDir() {
		return fmt.Errorf("TROVE_TEST_CA_FILE %s is not a readable file%s", ca, hint)
	}
	for _, name := range []string{"TROVE_TEST_PG_DSN", "TROVE_TEST_PG_PGBOUNCER_DSN"} {
		dsn := get(name)
		if dsn == "" {
			return fmt.Errorf("%s is not set%s", name, hint)
		}
		u, err := url.Parse(dsn)
		if err != nil {
			return fmt.Errorf("%s is not a URL: %w", name, err)
		}
		q := u.Query()
		if q.Get("sslmode") != "verify-full" || q.Get("sslrootcert") == "" {
			return fmt.Errorf("%s is plaintext or unverified (need sslmode=verify-full and sslrootcert); the stack is TLS-only (INV-20)", name)
		}
	}
	if err := checkS3Endpoint(get, hint); err != nil {
		return err
	}
	if get("TROVE_TEST_VALKEY_CA_FILE") == "" {
		return fmt.Errorf("TROVE_TEST_VALKEY_CA_FILE is not set%s", hint)
	}
	return nil
}

// HTTPClient is an HTTP client that verifies servers against [RootCAs]. It is for tests that talk
// to the stack directly (the S3 gateway); trove's own code gets its clients from internal/outbound.
func HTTPClient(tb testing.TB) *http.Client {
	tb.Helper()
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: TLSConfig(tb)},
	}
}

// checkS3Endpoint requires TROVE_TEST_S3_ENDPOINT to be set and https.
func checkS3Endpoint(get func(string) string, hint string) error {
	ep := get("TROVE_TEST_S3_ENDPOINT")
	if ep == "" {
		return fmt.Errorf("TROVE_TEST_S3_ENDPOINT is not set%s", hint)
	}
	if !strings.HasPrefix(strings.ToLower(ep), "https://") {
		return fmt.Errorf("TROVE_TEST_S3_ENDPOINT %q is plaintext; the stack is TLS-only (INV-20)", ep)
	}
	return nil
}
