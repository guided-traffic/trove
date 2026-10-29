package cluster

import (
	"bytes"
	"errors"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
)

// valkeyMethods is the exported method set of *Valkey. Nothing on it reads or
// writes a Valkey key: the commands are unexported (Q-ARCH-16).
func valkeyMethods() []string {
	return []string{
		"Available", "Close", "CountCacheLookup", "CountDegraded", "OnPrimaryChange", "OnSubscribed",
		"SetKeyspaceID", "StartupError", "Status",
	}
}

func TestValkeySurface(t *testing.T) {
	t.Run("R-ARCH-030 R-ARCH-031 the exported methods of the Valkey client include no raw command", func(t *testing.T) {
		typ := reflect.TypeFor[*Valkey]()
		var got []string
		for i := range typ.NumMethod() {
			got = append(got, typ.Method(i).Name)
		}
		want := valkeyMethods()
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("exported methods of *Valkey = %v, want %v", got, want)
		}
		for _, raw := range []string{"Get", "Set", "SetNX", "SetIfEq", "DelIfEq", "Incr", "Ping", "Key", "KeyTTL"} {
			if slices.Contains(got, raw) {
				t.Errorf("%s is exported", raw)
			}
		}
	})

	t.Run("R-ARCH-030 no package outside internal/cluster can register a key family", func(t *testing.T) {
		fset := token.NewFileSet()
		files, err := filepath.Glob("valkey*.go")
		if err != nil {
			t.Fatal(err)
		}
		var exported []string
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			src, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			if integrationOnly(f) {
				if name != "valkey_integration.go" {
					t.Errorf("%s is compiled only with the integration tag; only valkey_integration.go may be", name)
				}
				continue
			}
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.IsExported() {
					exported = append(exported, fd.Name.Name)
				}
			}
		}
		for _, banned := range []string{"RegisterKeyFamily", "RegisterChannelFamily", "Families"} {
			if slices.Contains(exported, banned) {
				t.Errorf("%s is exported in the build without the integration tag", banned)
			}
		}
	})

	t.Run("R-ARCH-030 a zero family forms neither a key nor a channel", func(t *testing.T) {
		v := bareValkey("trove:", "0123456789ab")
		if _, err := v.formKey(&KeyFamily{}, "x"); err == nil {
			t.Error("key of a zero family")
		}
		if _, err := v.channel(&ChannelFamily{}); err == nil {
			t.Error("channel of a zero family")
		}
	})
}

// integrationOnly reports whether a file carries a build constraint that
// requires the integration tag.
func integrationOnly(f *ast.File) bool {
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				return false
			}
			return !expr.Eval(func(tag string) bool { return tag != "integration" })
		}
	}
	return false
}

func TestValkeyReasonCodes(t *testing.T) {
	inVocabulary := func(t *testing.T, st ValkeyStatus) {
		t.Helper()
		if !slices.Contains(ReasonCodes(), st.ReasonCode) {
			t.Fatalf("reason code %q is not in the vocabulary (status %+v)", st.ReasonCode, st)
		}
	}
	t.Run("R-OPS-001 every Sentinel unreachable is no_sentinel, the text keeps the detail", func(t *testing.T) {
		v, _ := newTestValkey(t, sentinelCfg("127.0.0.1:1", "127.0.0.1:2"))
		st := v.Status()
		inVocabulary(t, st)
		if st.ReasonCode != ReasonNoSentinel || !strings.Contains(st.DegradedReason, "127.0.0.1") {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("R-OPS-001 a rejected credential is auth_failed", func(t *testing.T) {
		srv := errorServer(t, "WRONGPASS invalid username-password pair or user is disabled.")
		v, _ := newTestValkey(t, standaloneCfg(srv.addr()))
		if st := v.Status(); st.ReasonCode != ReasonAuthFailed {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("R-OPS-001 a server below 9.0 is unsupported", func(t *testing.T) {
		srv := standaloneServer(t, "valkey", "8.1.5")
		v, _ := newTestValkey(t, standaloneCfg(srv.addr()))
		eventually(t, "version check", 5*time.Second, func() bool { return v.Status().Version != "" })
		if st := v.Status(); st.ReasonCode != ReasonUnsupported {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("R-OPS-001 a refused connection is connecting, a closed client closed, an available one has no code", func(t *testing.T) {
		v, _ := newTestValkey(t, standaloneCfg("127.0.0.1:1"))
		if st := v.Status(); st.ReasonCode != ReasonConnecting {
			t.Fatalf("status = %+v", st)
		}
		v.Close()
		if st := v.Status(); st.ReasonCode != ReasonClosed {
			t.Fatalf("status = %+v", st)
		}
		srv := standaloneServer(t, "valkey", "9.1.0")
		ok, _ := newTestValkey(t, standaloneCfg(srv.addr()))
		eventually(t, "availability", 5*time.Second, ok.Available)
		if st := ok.Status(); st.ReasonCode != ReasonNone || st.DegradedReason != "" {
			t.Fatalf("status = %+v", st)
		}
	})
	t.Run("R-OPS-001 the code of a connection attempt follows its error kind", func(t *testing.T) {
		silent := []sentinelProbe{{Addr: "s1", Err: errors.New("refused")}}
		answered := []sentinelProbe{{Addr: "s1", Primary: "p:6379"}}
		for _, c := range []struct {
			config bool
			kind   ErrorKind
			probes []sentinelProbe
			want   ReasonCode
		}{
			{true, KindAuth, answered, ReasonAuthFailed},
			{true, KindTLS, answered, ReasonConfigError},
			{true, KindNoPrimary, answered, ReasonConfigError},
			{false, KindConnection, silent, ReasonNoSentinel},
			{false, KindTimeout, answered, ReasonTimeout},
			{false, KindTimeout, nil, ReasonTimeout},
			{false, KindConnection, nil, ReasonConnecting},
		} {
			if got := connectReason(c.config, c.kind, c.probes); got != c.want {
				t.Errorf("connectReason(%v, %s, %d probes) = %q, want %q", c.config, c.kind, len(c.probes), got, c.want)
			}
		}
		for _, code := range ReasonCodes() {
			if strings.ContainsAny(string(code), " :./") || code == "" {
				t.Errorf("code %q is not a plain word", code)
			}
		}
	})
}

func TestValkeyTrustWarnings(t *testing.T) {
	newLogged := func(t *testing.T, cfg config.Valkey) string {
		t.Helper()
		var buf bytes.Buffer
		v, err := NewValkey(t.Context(), cfg, "pod", "web", slog.New(slog.NewJSONHandler(&buf, nil)), prometheus.NewRegistry(), WithKeyspaceID(testKeyspace))
		if err != nil {
			t.Fatal(err)
		}
		v.Close()
		return buf.String()
	}
	t.Run("R-HA-056 Sentinel mode without Sentinel authentication or with the system roots logs a warning", func(t *testing.T) {
		cfg := sentinelCfg("127.0.0.1:1")
		cfg.TLS = config.ValkeyTLS{Mode: "verify-full"}
		out := newLogged(t, cfg)
		if !strings.Contains(out, "valkey sentinel authentication is not configured") || !strings.Contains(out, "system roots") {
			t.Fatalf("log = %s", out)
		}
	})
	t.Run("R-HA-056 Sentinel authentication and a CA file log no warning", func(t *testing.T) {
		dir := t.TempDir()
		pw := filepath.Join(dir, "pw")
		if err := os.WriteFile(pw, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := sentinelCfg("127.0.0.1:1")
		cfg.Sentinel.PasswordFile = pw
		out := newLogged(t, cfg) // tls.mode disable: no roots to warn about
		if strings.Contains(out, "sentinel authentication is not configured") || strings.Contains(out, "system roots") {
			t.Fatalf("log = %s", out)
		}
	})
	t.Run("R-HA-056 verify-ca without a CA file is refused", func(t *testing.T) {
		if _, err := tlsConfig(config.ValkeyTLS{Mode: "verify-ca"}); err == nil || !strings.Contains(err.Error(), "ca_file") {
			t.Fatalf("err = %v", err)
		}
	})
}
