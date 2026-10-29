package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// minimalEnv is what every command needs besides defaults.
func minimalEnv(extra ...string) []string {
	return append([]string{"TROVE_DATABASE_HOST=db.example", "TROVE_DATABASE_NAME=trove"}, extra...)
}

// servableEnv adds what `trove serve` additionally requires.
func servableEnv(extra ...string) []string {
	return minimalEnv(append([]string{
		"TROVE_PROCESS_ROLE=web",
		"TROVE_VALKEY_SENTINEL_ADDRESSES=[\"s1:26379\"]",
		"TROVE_VALKEY_SENTINEL_PRIMARY_NAME=trove",
		"TROVE_STORAGE_S3_BUCKET=blobs",
		"TROVE_SECRETS_ENCRYPTION_KEY_FILES=[\"/keys/2026-10.key\"]",
		"TROVE_SECRETS_ACTIVE_KEY_ID=2026-10",
	}, extra...)...)
}

func fakeFS(files map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if c, ok := files[p]; ok {
			return []byte(c), nil
		}
		return nil, &os.PathError{Op: "open", Path: p, Err: os.ErrNotExist}
	}
}

func mustLoad(t *testing.T, o Options) *Loaded {
	t.Helper()
	l, err := Load(o)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return l
}

func issuesOf(t *testing.T, err error) []string {
	t.Helper()
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("want *config.Error, got %T: %v", err, err)
	}
	return ce.Messages()
}

func containsAll(t *testing.T, msgs []string, wants ...string) {
	t.Helper()
	all := strings.Join(msgs, "\n")
	for _, w := range wants {
		if !strings.Contains(all, w) {
			t.Errorf("missing %q in:\n%s", w, all)
		}
	}
}

func TestSchemaOfTheTree(t *testing.T) {
	t.Run("R-ARCH-020 the compiled-in tree is valid", func(t *testing.T) {
		if _, errs := Describe(reflect.TypeOf(Config{})); len(errs) > 0 {
			t.Fatal(errors.Join(errs...))
		}
	})
	t.Run("R-ARCH-020 two keys deriving the same environment variable fail", func(t *testing.T) {
		type inner struct {
			C int `yaml:"c"`
		}
		type a struct {
			BC int `yaml:"b_c"`
		}
		type tree struct {
			A  a     `yaml:"a"`
			AB inner `yaml:"a_b"`
		}
		_, errs := Describe(reflect.TypeOf(tree{}))
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), "TROVE_A_B_C") {
			t.Fatalf("want one duplicate-env error naming TROVE_A_B_C, got %v", errs)
		}
	})
	t.Run("R-ARCH-020 a field without a yaml tag fails", func(t *testing.T) {
		type tree struct {
			Listen string
		}
		_, errs := Describe(reflect.TypeOf(tree{}))
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), "no yaml tag") || !strings.Contains(errs[0].Error(), "Listen") {
			t.Fatalf("got %v", errs)
		}
	})
	t.Run("R-ARCH-020 a yaml name that is not snake_case fails", func(t *testing.T) {
		type tree struct {
			X string `yaml:"someKey"`
		}
		if _, errs := Describe(reflect.TypeOf(tree{})); len(errs) != 1 {
			t.Fatalf("got %v", errs)
		}
	})
	t.Run("R-ARCH-020 an unparsable default fails", func(t *testing.T) {
		type tree struct {
			X time.Duration `yaml:"x" default:"soon"`
		}
		if _, errs := Describe(reflect.TypeOf(tree{})); len(errs) != 1 {
			t.Fatalf("got %v", errs)
		}
	})
	t.Run("R-OPS-028 environment names follow TROVE_ plus the upper-cased path", func(t *testing.T) {
		s := MustDescribe()
		for path, env := range map[string]string{
			"log.request.enabled":                         "TROVE_LOG_REQUEST_ENABLED",
			"metrics.label_limits.repository":             "TROVE_METRICS_LABEL_LIMITS_REPOSITORY",
			"runtime.memory_limit_bytes":                  "TROVE_RUNTIME_MEMORY_LIMIT_BYTES",
			"runtime.termination_grace_period":            "TROVE_RUNTIME_TERMINATION_GRACE_PERIOD",
			"storage.spool.max_bytes":                     "TROVE_STORAGE_SPOOL_MAX_BYTES",
			"database.preflight.expected_web_replicas":    "TROVE_DATABASE_PREFLIGHT_EXPECTED_WEB_REPLICAS",
			"database.preflight.expected_worker_replicas": "TROVE_DATABASE_PREFLIGHT_EXPECTED_WORKER_REPLICAS",
		} {
			k, ok := s.ByPath(path)
			if !ok || k.Env != env {
				t.Errorf("%s: env %q (found %v), want %q", path, k.Env, ok, env)
			}
		}
	})
	t.Run("R-OPS-028 chart-set runtime and preflight keys exist as schema keys", func(t *testing.T) {
		s := MustDescribe()
		for _, p := range []string{
			"runtime.memory_limit_bytes", "runtime.termination_grace_period", "storage.spool.max_bytes",
			"database.preflight.expected_web_replicas", "database.preflight.expected_worker_replicas",
		} {
			if _, ok := s.ByPath(p); !ok {
				t.Errorf("schema lacks %s", p)
			}
		}
	})
}

func TestProcessRole(t *testing.T) {
	t.Run("R-ARCH-001 serve without a role fails naming process.role", func(t *testing.T) {
		_, err := Load(Options{
			File: "/none", ReadFile: fakeFS(map[string]string{"/none": ""}),
			Environ: minimalEnv(), Command: CommandServe,
		})
		msgs := issuesOf(t, err)
		containsAll(t, msgs, "process.role must be web, worker or all")
	})
	t.Run("R-ARCH-001 an invalid role is rejected", func(t *testing.T) {
		_, err := Load(Options{Environ: servableEnv("TROVE_PROCESS_ROLE=backup"), Command: CommandServe, ReadFile: fakeFS(nil)})
		containsAll(t, issuesOf(t, err), "process.role must be one of web, worker, all")
	})
	t.Run("R-ARCH-001 the role flag overrides the file and the environment", func(t *testing.T) {
		l := mustLoad(t, Options{
			File:     "/c.yaml",
			ReadFile: fakeFS(map[string]string{"/c.yaml": "process:\n  role: worker\n"}),
			Environ:  servableEnv("TROVE_PROCESS_ROLE=all"),
			Flags:    map[string]string{"process.role": "web"}, FlagNames: map[string]string{"process.role": "--role"},
			Command: CommandServe,
		})
		if l.Config.Process.Role != "web" || l.Sources["process.role"].String() != "flag:--role" {
			t.Fatalf("role %q source %v", l.Config.Process.Role, l.Sources["process.role"])
		}
	})
	t.Run("R-ARCH-001 migrate and admin need no role", func(t *testing.T) {
		for _, c := range []Command{CommandMigrate, CommandAdmin} {
			mustLoad(t, Options{Environ: minimalEnv(), Command: c, ReadFile: fakeFS(nil)})
		}
	})
}

func TestPrecedence(t *testing.T) {
	file := "log:\n  level: warn\n  format: text\nserver:\n  max_in_flight: 100\n"
	opts := func(env ...string) Options {
		return Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": file}), Environ: minimalEnv(env...), Command: CommandAdmin}
	}
	t.Run("R-OPS-028 defaults, then file, then environment, then flags", func(t *testing.T) {
		o := opts("TROVE_LOG_LEVEL=error")
		o.Flags = map[string]string{"log.level": "debug"}
		o.FlagNames = map[string]string{"log.level": "--log-level"}
		l := mustLoad(t, o)
		want := map[string]string{
			"log.level":            "flag:--log-level",
			"log.format":           "file",
			"server.max_in_flight": "file",
			"server.listen":        "default",
		}
		for k, v := range want {
			if got := l.Sources[k].String(); got != v {
				t.Errorf("source of %s = %q, want %q", k, got, v)
			}
		}
		if l.Config.Log.Level != "debug" || l.Config.Log.Format != "text" || l.Config.Server.MaxInFlight != 100 || l.Config.Server.Listen != ":8080" {
			t.Fatalf("unexpected values: %+v %+v", l.Config.Log, l.Config.Server)
		}
	})
	t.Run("R-OPS-028 environment beats the file", func(t *testing.T) {
		l := mustLoad(t, opts("TROVE_LOG_LEVEL=error"))
		if l.Config.Log.Level != "error" || l.Sources["log.level"].String() != "env" {
			t.Fatalf("level %q source %v", l.Config.Log.Level, l.Sources["log.level"])
		}
	})
	t.Run("R-OPS-028 a variable inside a nested section is reported with source env", func(t *testing.T) {
		l := mustLoad(t, opts("TROVE_METRICS_LABEL_LIMITS_REPOSITORY=50"))
		if l.Config.Metrics.LabelLimits.Repository != 50 || l.Sources["metrics.label_limits.repository"].Kind != "env" {
			t.Fatalf("got %d source %v", l.Config.Metrics.LabelLimits.Repository, l.Sources["metrics.label_limits.repository"])
		}
		var buf bytes.Buffer
		if err := l.WriteTable(&buf); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), "metrics.label_limits.repository") {
			t.Fatal("table lacks the key")
		}
	})
	t.Run("R-OPS-028 lists and maps in variables are JSON", func(t *testing.T) {
		l := mustLoad(t, opts(`TROVE_LOG_LEVELS={"proxy":"debug"}`, `TROVE_SERVER_TRUSTED_PROXIES=["10.0.0.0/8"]`))
		if l.Config.Log.Levels["proxy"] != "debug" || len(l.Config.Server.TrustedProxies) != 1 {
			t.Fatalf("got %+v %+v", l.Config.Log.Levels, l.Config.Server.TrustedProxies)
		}
	})
	t.Run("R-OPS-028 durations and sizes accept their text forms", func(t *testing.T) {
		l := mustLoad(t, opts("TROVE_SERVER_SHUTDOWN_TIMEOUT=40s", "TROVE_STORAGE_SPOOL_MAX_BYTES=20GiB", "TROVE_STORAGE_UPLOAD_MAX_SIZE=1GiB"))
		if l.Config.Server.ShutdownTimeout != 40*time.Second || l.Config.Storage.Spool.MaxBytes != 20*GiB {
			t.Fatalf("got %v %v", l.Config.Server.ShutdownTimeout, l.Config.Storage.Spool.MaxBytes)
		}
	})
	t.Run("R-OPS-028 the file path comes from --config, then TROVE_CONFIG_FILE, then the default", func(t *testing.T) {
		fsys := fakeFS(map[string]string{"/env.yaml": "log:\n  level: warn\n", DefaultFile: "log:\n  level: error\n"})
		l := mustLoad(t, Options{ReadFile: fsys, Environ: minimalEnv("TROVE_CONFIG_FILE=/env.yaml"), Command: CommandAdmin})
		if l.Config.Log.Level != "warn" || l.File != "/env.yaml" {
			t.Fatalf("env file: level %q file %q", l.Config.Log.Level, l.File)
		}
		l = mustLoad(t, Options{ReadFile: fsys, Environ: minimalEnv(), Command: CommandAdmin})
		if l.Config.Log.Level != "error" || l.File != DefaultFile {
			t.Fatalf("default file: level %q file %q", l.Config.Log.Level, l.File)
		}
	})
	t.Run("R-OPS-028 a missing default file is fine, a missing named file is not", func(t *testing.T) {
		mustLoad(t, Options{ReadFile: fakeFS(nil), Environ: minimalEnv(), Command: CommandAdmin})
		_, err := Load(Options{File: "/nope.yaml", ReadFile: fakeFS(nil), Environ: minimalEnv(), Command: CommandAdmin})
		containsAll(t, issuesOf(t, err), "/nope.yaml")
	})
}

func TestStrictValidation(t *testing.T) {
	base := func(file string, env ...string) Options {
		return Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": file}), Environ: minimalEnv(env...), Command: CommandAdmin}
	}
	t.Run("R-OPS-028 an unknown variable names it and suggests the closest", func(t *testing.T) {
		_, err := Load(base("", "TROVE_LOG_LEVL=debug"))
		containsAll(t, issuesOf(t, err), "TROVE_LOG_LEVL", "TROVE_LOG_LEVEL")
	})
	t.Run("R-OPS-028 an unknown file key names it, its line and the closest key", func(t *testing.T) {
		_, err := Load(base("log:\n  levl: debug\n"))
		containsAll(t, issuesOf(t, err), "/c.yaml:2", `"log.levl"`, `"log.level"`)
	})
	t.Run("R-OPS-028 an unknown section is rejected", func(t *testing.T) {
		_, err := Load(base("logg:\n  level: debug\n"))
		containsAll(t, issuesOf(t, err), `unknown key "logg"`, `"log"`)
	})
	t.Run("R-OPS-028 reserved variables are exempt", func(t *testing.T) {
		mustLoad(t, base("", "TROVE_POD_NAME=p", "TROVE_POD_NAMESPACE=n", "TROVE_NODE_NAME=x", "TROVE_POOL_NAME=y", "TROVE_CAC_ORG_TOKEN=z", "TROVE_TEST_PG_DSN=q"))
	})
	t.Run("R-OPS-028 every error is reported in one pass", func(t *testing.T) {
		_, err := Load(base("log:\n  level: loud\n  levl: x\nserver:\n  max_in_flight: many\n", "TROVE_LOG_FORMA=json", "TROVE_JOBS_POLL_INTERVAL=soon"))
		msgs := issuesOf(t, err)
		if len(msgs) < 4 {
			t.Fatalf("want at least 4 issues, got %d: %v", len(msgs), msgs)
		}
		containsAll(t, msgs, "log.levl", "max_in_flight", "TROVE_LOG_FORMA", "TROVE_JOBS_POLL_INTERVAL")
	})
	t.Run("R-OPS-028 type errors name the key", func(t *testing.T) {
		_, err := Load(base("server:\n  max_in_flight: many\n  idle_timeout: 120\n  trusted_proxies: 10.0.0.0/8\n"))
		containsAll(t, issuesOf(t, err), "server.max_in_flight", "server.idle_timeout", "server.trusted_proxies")
	})
	t.Run("R-OPS-028 range and enum errors name the key and the bound", func(t *testing.T) {
		_, err := Load(base("database:\n  pool:\n    max_conns: 1\nlog:\n  level: loud\n"))
		containsAll(t, issuesOf(t, err), "database.pool.max_conns must be at least 2", "log.level must be one of debug, info, warn, error")
	})
	t.Run("R-OPS-028 required keys are reported", func(t *testing.T) {
		_, err := Load(Options{ReadFile: fakeFS(nil), Environ: []string{}, Command: CommandAdmin})
		containsAll(t, issuesOf(t, err), "database.host is required", "database.name is required")
	})
}

func TestSecrets(t *testing.T) {
	type mail struct {
		Host     string `yaml:"host" default:"smtp.example.com"`
		Password Secret `yaml:"password"`
	}
	type tree struct {
		Mail mail   `yaml:"mail"`
		Key  string `yaml:"key_file" secret:"file" default:""`
	}
	load := func(file string, env []string, files map[string]string) (*Loaded, error) {
		if files == nil {
			files = map[string]string{}
		}
		files["/c.yaml"] = file
		return loadInto(&tree{}, Options{File: "/c.yaml", ReadFile: fakeFS(files), Environ: env})
	}
	t.Run("R-OPS-028 a secret written inline into the file fails naming the _file key and the variable", func(t *testing.T) {
		_, err := load("mail:\n  password: hunter2\n", nil, nil)
		msgs := issuesOf(t, err)
		containsAll(t, msgs, "mail.password_file", "TROVE_MAIL_PASSWORD")
		if strings.Contains(strings.Join(msgs, "\n"), "hunter2") {
			t.Fatal("the secret value appears in the error")
		}
	})
	t.Run("R-OPS-028 the secret may come from the environment", func(t *testing.T) {
		l, err := load("", []string{"TROVE_MAIL_PASSWORD=s3cret"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if l.root.FieldByName("Mail").FieldByName("Password").Field(0).String() != "s3cret" {
			t.Fatal("secret not set")
		}
	})
	t.Run("R-OPS-028 the _file companion is read and its source is the file", func(t *testing.T) {
		l, err := load("mail:\n  password_file: /run/smtp\n", nil, map[string]string{"/run/smtp": "from-file\n"})
		if err != nil {
			t.Fatal(err)
		}
		if got := l.root.FieldByName("Mail").FieldByName("Password").Field(0).String(); got != "from-file" {
			t.Fatalf("got %q", got)
		}
		if l.Sources["mail.password"].String() != "file:/run/smtp" {
			t.Fatalf("source %v", l.Sources["mail.password"])
		}
	})
	t.Run("R-OPS-028 the companion is settable through the environment", func(t *testing.T) {
		_, err := load("", []string{"TROVE_MAIL_PASSWORD_FILE=/run/smtp"}, map[string]string{"/run/smtp": "x"})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-OPS-028 setting the value and the file is an error", func(t *testing.T) {
		_, err := load("mail:\n  password_file: /run/smtp\n", []string{"TROVE_MAIL_PASSWORD=x"}, map[string]string{"/run/smtp": "y"})
		containsAll(t, issuesOf(t, err), "mail.password and mail.password_file are both set")
	})
	t.Run("R-OPS-028 an unreadable secret file is an error", func(t *testing.T) {
		_, err := load("mail:\n  password_file: /run/missing\n", nil, nil)
		containsAll(t, issuesOf(t, err), "/run/missing")
	})
	t.Run("R-OPS-028 the effective view redacts the value and shows the file source", func(t *testing.T) {
		l, err := load("mail:\n  password_file: /run/smtp\n", nil, map[string]string{"/run/smtp": "from-file"})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := l.EffectiveJSON()
		if strings.Contains(string(raw), "from-file") {
			t.Fatalf("secret leaked: %s", raw)
		}
		var doc struct {
			Values map[string]struct {
				Value  any    `json:"value"`
				Source string `json:"source"`
			} `json:"values"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if p := doc.Values["mail.password"]; p.Value != Redacted || p.Source != "file:/run/smtp" {
			t.Fatalf("mail.password = %+v", p)
		}
	})
	t.Run("R-OPS-028 a Secret never prints its value", func(t *testing.T) {
		s := Secret{Value: "hunter2", File: "/f"}
		if got := strings.Join([]string{s.String(), s.GoString()}, " "); strings.Contains(got, "hunter2") {
			t.Fatalf("leaked: %s", got)
		}
	})
	t.Run("R-OPS-028 a file-only secret is not read at load", func(t *testing.T) {
		l, err := load("key_file: /never/read\n", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if l.Sources["key_file"].Kind != "file" {
			t.Fatalf("source %v", l.Sources["key_file"])
		}
	})
}

func TestAdminConfigValidateListing(t *testing.T) {
	t.Run("R-ARCH-020 the listing has every key of the architecture sections with its source", func(t *testing.T) {
		l := mustLoad(t, Options{Environ: servableEnv(), ReadFile: fakeFS(nil), Command: CommandServe})
		var buf bytes.Buffer
		if err := l.WriteTable(&buf); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		for _, k := range l.Schema.Keys {
			switch strings.SplitN(k.Path, ".", 2)[0] {
			case "process", "server", "jobs", "cluster", "cache", "secrets":
				line := lineWith(out, k.Path)
				if line == "" {
					t.Errorf("listing lacks %s", k.Path)
					continue
				}
				if !strings.Contains(line, "default") && !strings.Contains(line, "env") {
					t.Errorf("line for %s has no source: %q", k.Path, line)
				}
			}
		}
		if got := lineWith(out, "process.role"); !strings.Contains(got, "env") {
			t.Errorf("process.role line %q should show source env", got)
		}
	})
	t.Run("R-ARCH-020 the redacted view of the real tree holds no inline secret", func(t *testing.T) {
		l := mustLoad(t, Options{Environ: servableEnv(), ReadFile: fakeFS(nil), Command: CommandServe})
		for _, e := range l.Effective() {
			if e.Secret && strings.HasSuffix(e.Key, "_file") == false && e.Value != "" && e.Value != Redacted {
				t.Errorf("%s shows %v", e.Key, e.Value)
			}
		}
	})
}

func lineWith(table, key string) string {
	for _, ln := range strings.Split(table, "\n") {
		if strings.HasPrefix(ln, key+" ") {
			return ln
		}
	}
	return ""
}

func TestSectionRules(t *testing.T) {
	load := func(file string, cmd Command) error {
		_, err := Load(Options{File: "/c.yaml", ReadFile: fakeFS(map[string]string{"/c.yaml": file}), Environ: servableEnv(), Command: cmd})
		return err
	}
	t.Run("R-OPS-005 base_url must be an origin", func(t *testing.T) {
		for _, bad := range []string{"https://shared.example.com/trove", "https://trove.example.com/?x=1", "https://user@trove.example.com", "ftp://x.example.com"} {
			err := load("server:\n  base_url: \""+bad+"\"\n", CommandServe)
			if err == nil || !strings.Contains(err.Error(), "server.base_url") {
				t.Errorf("%s: want an error naming server.base_url, got %v", bad, err)
			}
		}
		for _, good := range []string{"https://trove.example.com/", "https://trove.example.com:8443", "http://localhost:8080"} {
			if err := load("server:\n  base_url: \""+good+"\"\n", CommandServe); err != nil {
				t.Errorf("%s: %v", good, err)
			}
		}
	})
	t.Run("R-OPS-005 additional_base_urls follow the same rule", func(t *testing.T) {
		err := load("server:\n  additional_base_urls: [\"https://npm.example.com/registry\"]\n", CommandServe)
		if err == nil || !strings.Contains(err.Error(), "server.additional_base_urls") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("R-ARCH-016 lease_duration must be at least three heartbeats", func(t *testing.T) {
		err := load("jobs:\n  lease_duration: 20s\n  heartbeat_interval: 15s\n", CommandServe)
		if err == nil || !strings.Contains(err.Error(), "jobs.lease_duration") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("R-ARCH-031 valkey tls disable needs allow_insecure", func(t *testing.T) {
		err := load("valkey:\n  tls:\n    mode: disable\n", CommandServe)
		if err == nil || !strings.Contains(err.Error(), "valkey.allow_insecure") {
			t.Fatalf("got %v", err)
		}
		if err := load("valkey:\n  allow_insecure: true\n  tls:\n    mode: disable\n", CommandServe); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-ARCH-021 a missing key ring stops serve but not migrate", func(t *testing.T) {
		env := minimalEnv("TROVE_PROCESS_ROLE=web", "TROVE_VALKEY_SENTINEL_ADDRESSES=[\"s:1\"]", "TROVE_VALKEY_SENTINEL_PRIMARY_NAME=t", "TROVE_STORAGE_S3_BUCKET=b")
		_, err := Load(Options{ReadFile: fakeFS(nil), Environ: env, Command: CommandServe})
		containsAll(t, issuesOf(t, err), "secrets.encryption_key_files", "secrets.active_key_id")
		if _, err := Load(Options{ReadFile: fakeFS(nil), Environ: env, Command: CommandMigrate}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-ARCH-021 the active key must be one of the listed files", func(t *testing.T) {
		_, err := Load(Options{ReadFile: fakeFS(nil), Environ: servableEnv("TROVE_SECRETS_ACTIVE_KEY_ID=2027-01"), Command: CommandServe})
		containsAll(t, issuesOf(t, err), "secrets.active_key_id", "2027-01")
	})
	t.Run("R-ARCH-031 sentinel mode needs addresses and a primary name", func(t *testing.T) {
		env := minimalEnv("TROVE_PROCESS_ROLE=web", "TROVE_STORAGE_S3_BUCKET=b", "TROVE_SECRETS_ENCRYPTION_KEY_FILES=[\"/k/a.key\"]", "TROVE_SECRETS_ACTIVE_KEY_ID=a")
		_, err := Load(Options{ReadFile: fakeFS(nil), Environ: env, Command: CommandServe})
		containsAll(t, issuesOf(t, err), "valkey.sentinel.addresses", "valkey.sentinel.primary_name")
	})
	t.Run("R-STOR-004 the maximum upload must fit the spool", func(t *testing.T) {
		err := load("storage:\n  spool:\n    max_bytes: 1GiB\n", CommandServe)
		if err == nil || !strings.Contains(err.Error(), "storage.upload.max_size") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestByteSize(t *testing.T) {
	cases := map[string]ByteSize{"0": 0, "512": 512, "1KiB": KiB, "256MiB": 256 * MiB, "10GiB": 10 * GiB, "2TiB": 2 * TiB, "5MB": 5000000}
	for in, want := range cases {
		t.Run("R-OPS-028 parse "+in, func(t *testing.T) {
			got, err := ParseByteSize(in)
			if err != nil || got != want {
				t.Fatalf("got %d, %v want %d", got, err, want)
			}
		})
	}
	for _, bad := range []string{"", "-1MiB", "1.5GiB", "ten", "1PiB"} {
		t.Run("R-OPS-028 reject "+bad, func(t *testing.T) {
			if _, err := ParseByteSize(bad); err == nil {
				t.Fatal("want error")
			}
		})
	}
	t.Run("R-OPS-028 String uses the largest exact binary unit", func(t *testing.T) {
		for in, want := range map[ByteSize]string{0: "0", 1536: "1536", 2 * KiB: "2KiB", 50 * GiB: "50GiB", 3 * MiB: "3MiB"} {
			if in.String() != want {
				t.Errorf("%d -> %q, want %q", int64(in), in.String(), want)
			}
		}
	})
}

func TestGeneratedSchemaIsCommitted(t *testing.T) {
	t.Run("R-ARCH-020 schema.json equals the schema generated from the tree", func(t *testing.T) {
		want, err := MustDescribe().JSONSchema()
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile("schema.json")
		if err != nil {
			t.Fatal(err)
		}
		if string(bytes.TrimSpace(got)) != string(want) {
			t.Fatal("schema.json is stale: run go generate ./internal/config")
		}
	})
	t.Run("R-ARCH-020 the schema marks required keys and rejects unknown ones", func(t *testing.T) {
		var doc struct {
			AdditionalProperties bool `json:"additionalProperties"`
			Properties           map[string]struct {
				Required []string `json:"required"`
			} `json:"properties"`
		}
		raw, _ := MustDescribe().JSONSchema()
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.AdditionalProperties {
			t.Error("root allows additional properties")
		}
		if got := doc.Properties["database"].Required; len(got) != 2 {
			t.Errorf("database.required = %v", got)
		}
	})
}

func TestSecretNeverSerializes(t *testing.T) {
	s := Secret{Value: "hunter2", File: "/f"}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "secret", s, "wrapped", struct{ S Secret }{s})
	j, _ := json.Marshal(map[string]any{"s": s, "p": &s, "w": struct{ S Secret }{s}})
	y, _ := yaml.Marshal(map[string]any{"s": s})
	txt, _ := s.MarshalText()
	for name, out := range map[string]string{
		"R-OPS-013 slog": buf.String(), "R-OPS-013 json": string(j), "R-OPS-013 yaml": string(y), "R-OPS-013 text": string(txt),
		"R-OPS-013 Sprintf %v": fmt.Sprintf("%v %+v %s", s, s, s), "R-OPS-013 Sprintf %#v": fmt.Sprintf("%#v", s),
	} {
		t.Run(name+" never contains the value", func(t *testing.T) {
			if strings.Contains(out, "hunter2") {
				t.Fatalf("leaked: %s", out)
			}
			if out == "" {
				t.Fatal("empty rendering")
			}
		})
	}
	t.Run("R-OPS-013 an unset Secret renders empty", func(t *testing.T) {
		if j, _ := json.Marshal(Secret{}); string(j) != `""` {
			t.Fatalf("%s", j)
		}
	})
}
