package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guided-traffic/trove/internal/config"
)

// The connection classes of R-ARCH-019. The class is the last part of the
// application_name of every connection.
const (
	classMain    = "main"
	classLease   = "lease"
	classMigrate = "migrate"
	classBackup  = "backup"
)

// maxApplicationName is the longest application_name PostgreSQL keeps
// (NAMEDATALEN - 1).
const maxApplicationName = 63

// ApplicationName is trove:<instance>:<class>, the name every connection sets so
// pg_stat_activity attributes it (R-ARCH-019, R-HA-015). An instance name too
// long for PostgreSQL is cut, never the class: the class is what the lock
// diagnostics read.
func ApplicationName(instance, class string) string {
	const prefix = "trove:"
	room := maxApplicationName - len(prefix) - 1 - len(class)
	if len(instance) > room {
		instance = instance[:room]
	}
	return prefix + instance + ":" + class
}

// endpoint is where one connection class connects.
type endpoint struct {
	host, name string
	port       int
}

func (e endpoint) String() string { return fmt.Sprintf("%s:%d/%s", e.host, e.port, e.name) }

// mainEndpoint is the database host, which may be a pooler (R-HA-058).
func mainEndpoint(c config.Database) endpoint {
	return endpoint{host: c.Host, port: c.Port, name: c.Name}
}

// sessionEndpoint is where session-scoped connections go: the session host with
// a transaction-mode pooler, the database host otherwise (R-ARCH-017). Empty
// session keys mean the main endpoint.
func sessionEndpoint(c config.Database) endpoint {
	e := mainEndpoint(c)
	if c.SessionHost != "" {
		e.host = c.SessionHost
	}
	if c.SessionPort != 0 {
		e.port = c.SessionPort
	}
	return e
}

// controlEndpoint is the control pooler (Q-HA-20); empty keys mean the main
// endpoint, which is right without a pooler and behind a session-mode pooler.
func controlEndpoint(c config.Database) endpoint {
	e := mainEndpoint(c)
	if c.ControlHost != "" {
		e.host = c.ControlHost
	}
	if c.ControlPort != 0 {
		e.port = c.ControlPort
	}
	if c.ControlName != "" {
		e.name = c.ControlName
	}
	return e
}

var schemaName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// checkConfig refuses the settings Open cannot work with, naming the key.
func checkConfig(c config.Database) error {
	if c.Pool.MaxConns < 2 {
		return fmt.Errorf("database.pool.max_conns must be at least 2 (one connection is the control connection), got %d", c.Pool.MaxConns)
	}
	if !schemaName.MatchString(c.Schema) {
		return fmt.Errorf("database.schema %q is not a plain lower-case identifier", c.Schema)
	}
	if msg := c.TLSProblem(); msg != "" {
		return fmt.Errorf("database.ssl_mode: %s", msg)
	}
	if c.Pooler.Mode == "transaction" && c.SessionHost == "" {
		return errors.New("database.session_host is required with database.pooler.mode transaction: session work would land on the pooler")
	}
	if c.UsernameFile == "" {
		return errors.New("database.username_file is empty: credentials come from files only")
	}
	return nil
}

// quote renders a value of a libpq key/value connection string.
func quote(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	return "'" + r.Replace(v) + "'"
}

// connString is the libpq key/value string of an endpoint. It carries no user
// and no password: BeforeConnect reads those from their files for every new
// connection, so a rotated Secret applies without a restart (R-HA-015, R-HA-017).
func connString(c config.Database, e endpoint) string {
	kv := []string{
		"host=" + quote(e.host),
		"port=" + strconv.Itoa(e.port),
		"dbname=" + quote(e.name),
		"sslmode=" + quote(c.SSLMode),
		"target_session_attrs=" + quote(c.TargetSessionAttrs),
	}
	if c.SSLRootCertFile != "" {
		kv = append(kv, "sslrootcert="+quote(c.SSLRootCertFile))
	}
	if c.SSLCertFile != "" {
		kv = append(kv, "sslcert="+quote(c.SSLCertFile))
	}
	if c.SSLKeyFile != "" {
		kv = append(kv, "sslkey="+quote(c.SSLKeyFile))
	}
	return strings.Join(kv, " ")
}

// readSecretFile reads a one-value file and trims the trailing newline.
func readSecretFile(key, path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path is configuration, mounted from a Secret
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

// beforeConnect fills user and password from their files on every new
// connection.
func beforeConnect(c config.Database) func(context.Context, *pgx.ConnConfig) error {
	return func(_ context.Context, cc *pgx.ConnConfig) error {
		user, err := readSecretFile("database.username_file", c.UsernameFile)
		if err != nil {
			return err
		}
		if user == "" {
			return errors.New("database.username_file is empty")
		}
		cc.User = user
		if c.PasswordFile != "" {
			pw, err := readSecretFile("database.password_file", c.PasswordFile)
			if err != nil {
				return err
			}
			cc.Password = pw
		}
		return nil
	}
}

// connKind says which session parameters a connection gets.
type connKind struct {
	class   string
	pooled  bool // the endpoint is a transaction-mode pooler: startup parameters are limited
	noLimit bool // migrations: statement_timeout 0
}

// runtimeParams are the startup parameters of a connection (R-HA-015). Behind a
// transaction-mode pooler only application_name and TimeZone travel as startup
// parameters, because PgBouncer refuses the others; role defaults supply the
// rest and preflight verifies them (R-HA-058).
func runtimeParams(c config.Database, instance string, k connKind) map[string]string {
	p := map[string]string{
		"application_name": ApplicationName(instance, k.class),
		"TimeZone":         "UTC",
	}
	if k.pooled {
		return p
	}
	stmt := c.Session.StatementTimeout
	if k.noLimit {
		stmt = 0
	}
	p["statement_timeout"] = strconv.FormatInt(stmt.Milliseconds(), 10)
	p["lock_timeout"] = strconv.FormatInt(c.Session.LockTimeout.Milliseconds(), 10)
	p["idle_in_transaction_session_timeout"] = strconv.FormatInt(c.Session.IdleInTransactionSessionTimeout.Milliseconds(), 10)
	p["search_path"] = c.Schema
	p["tcp_keepalives_idle"] = strconv.Itoa(wholeSeconds(c.ServerKeepalive.Idle))
	p["tcp_keepalives_interval"] = strconv.Itoa(wholeSeconds(c.ServerKeepalive.Interval))
	p["tcp_keepalives_count"] = strconv.Itoa(c.ServerKeepalive.Count)
	return p
}

func wholeSeconds(d time.Duration) int {
	s := int(d / time.Second)
	if s < 1 {
		s = 1
	}
	return s
}

// execMode is the pgx query mode of a connection kind. Behind a transaction-mode
// pooler the main pool uses the Exec mode: parameterised statements run through
// the extended protocol on the unnamed statement, so no connection holds a
// prepared statement that the next pooled transaction would not find. It works
// on every pooler, with and without max_prepared_statements (R-ARCH-019,
// Q-ARCH-14). Everywhere else pgx's default cache_statement applies.
func execMode(c config.Database, pooled bool) pgx.QueryExecMode {
	if pooled && c.Pooler.Mode == "transaction" {
		return pgx.QueryExecModeExec
	}
	return pgx.QueryExecModeCacheStatement
}

// tuneDial adds client-side keepalive and TCP_USER_TIMEOUT to the connections
// pgx dials (R-HA-015). The dial itself stays pgx's; this wraps its result.
func tuneDial(base pgconn.DialFunc, k config.DatabaseKeepalive) pgconn.DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := base(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetKeepAliveConfig(net.KeepAliveConfig{Enable: true, Idle: k.Idle, Interval: k.Interval, Count: k.Count})
			setUserTimeout(tc, k.UserTimeout)
		}
		return conn, nil
	}
}

// poolSpec describes one pool.
type poolSpec struct {
	kind                 connKind
	ep                   endpoint
	instance             string
	maxConns, minConns   int32
	lifetime, jitter     time.Duration
	idleTime, healthEach time.Duration
}

// newPoolConfig builds the pgxpool configuration of one pool.
func newPoolConfig(c config.Database, s poolSpec) (*pgxpool.Config, error) {
	pc, err := pgxpool.ParseConfig(connString(c, s.ep))
	if err != nil {
		return nil, fmt.Errorf("database connection settings for %s: %w", s.ep, err)
	}
	cc := pc.ConnConfig
	cc.ConnectTimeout = c.ConnectTimeout
	cc.RuntimeParams = runtimeParams(c, s.instance, s.kind)
	cc.DefaultQueryExecMode = execMode(c, s.kind.pooled)
	cc.DialFunc = tuneDial(cc.DialFunc, c.Keepalive)
	pc.BeforeConnect = beforeConnect(c)
	pc.MaxConns, pc.MinConns = s.maxConns, s.minConns
	pc.MaxConnLifetime, pc.MaxConnLifetimeJitter = s.lifetime, s.jitter
	pc.MaxConnIdleTime, pc.HealthCheckPeriod = s.idleTime, s.healthEach
	return pc, nil
}

// connectConfig builds the configuration of a single session connection, for
// the migration runner and the backup commands.
func connectConfig(c config.Database, ep endpoint, instance, class string, noLimit bool) (*pgx.ConnConfig, error) {
	cc, err := pgx.ParseConfig(connString(c, ep))
	if err != nil {
		return nil, fmt.Errorf("database connection settings for %s: %w", ep, err)
	}
	cc.ConnectTimeout = c.ConnectTimeout
	cc.RuntimeParams = runtimeParams(c, instance, connKind{class: class, noLimit: noLimit})
	cc.DialFunc = tuneDial(cc.DialFunc, c.Keepalive)
	if err := beforeConnect(c)(context.Background(), cc); err != nil {
		return nil, err
	}
	return cc, nil
}
