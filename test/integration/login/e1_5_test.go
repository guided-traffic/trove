//go:build integration

package login

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/rawfiles"
	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/idapi"
	"github.com/guided-traffic/trove/test/integration/stack"
)

// auditStream keeps the records of the audit log stream (log: audit), as the
// operator's log pipeline sees them (R-OPS-016).
type auditStream struct {
	mu   sync.Mutex
	recs []map[string]any
}

func (s *auditStream) Write(p []byte) (int, error) {
	var m map[string]any
	if json.Unmarshal(bytes.TrimSpace(p), &m) == nil {
		s.mu.Lock()
		s.recs = append(s.recs, m)
		s.mu.Unlock()
	}
	return len(p), nil
}

// has reports whether the stream holds a record of the action with the event ID.
func (s *auditStream) has(action, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.recs {
		if r["msg"] == "audit" && r["action"] == action && r["id"] == id {
			return true
		}
	}
	return false
}

// noBlobs is the blob store of a replica that never stores content: the denied
// deploy of this test is refused before the body is read.
type noBlobs struct{}

var errNoBlobs = errors.New("this test replica has no blob store")

func (noBlobs) Ingest(context.Context, io.Reader, blob.IngestOptions) (*blob.Spooled, error) {
	return nil, errNoBlobs
}

func (noBlobs) Commit(context.Context, *blob.Spooled, blob.LinkFunc) (blob.CommitResult, error) {
	return blob.CommitResult{}, errNoBlobs
}

func (noBlobs) Open(context.Context, blob.Digest, int64, blob.OpenOptions) (io.ReadCloser, error) {
	return nil, errNoBlobs
}

// TestE1_5_EveryProducerIsAuditedAndLogged runs every kind of event that exit
// criterion E1.5 names through the real HTTP routes on the harness of the
// identity and login tests (test/integration/idapi) with the fake provider: a
// login, a failed login, a token creation and revocation, a policy change and a
// denied deploy. Each produces an audit event that GET /api/v1/audit/events finds
// and a record in the audit log stream (R-OPS-015 to R-OPS-017).
func TestE1_5_EveryProducerIsAuditedAndLogged(t *testing.T) {
	e := newEnv(t)
	e.realAPI = true
	ctx := context.Background()

	// The real audit writer behind the spy: events land in the audit table through
	// the transaction of the change, or through the buffer for the asynchronous ones,
	// and every committed one is logged.
	stream := &auditStream{}
	adb, err := postgres.Open(ctx, e.cfg, "e15-audit", meta.RoleWeb, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adb.Close)
	writer, err := audit.New(audit.Options{
		DB: adb, Replica: "web-0", AppLog: quiet,
		Config: config.Audit{AsyncBuffer: 1000, DenialDedupWindow: time.Minute},
		Log:    slog.New(slog.NewJSONHandler(stream, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	if err != nil {
		t.Fatal(err)
	}
	writer.Start()
	t.Cleanup(func() { _ = writer.Flush(context.Background()) })
	e.audit.next = v1.WithRequestSource(writer)
	store := audit.NewStore(adb)

	e.extend = func(c *idapi.Components, p replicaParts) {
		reader, err := repository.NewReader(repository.ReaderOptions{
			Runner: p.db, Catalog: stack.FormatCatalog(), Caches: p.caches, Poller: p.changes,
			SafetyTTL: 5 * time.Minute, UnknownKeys: 1000, LookupWait: 5 * time.Second, Log: quiet,
		})
		if err != nil {
			t.Fatal(err)
		}
		policies := authz.NewService(authz.ServiceOptions{
			DB: p.db, Changes: p.changes, Principals: p.principals, Repositories: reader, Recorder: e.audit,
		})
		c.Options = func(engine *authz.Engine, o *v1.Options) {
			o.AuditStore = store
			o.Authz = v1.AuthzServices{Policies: policies, Engine: engine}
		}
		c.Routes = func(engine *authz.Engine, r *httpserver.Router) {
			svc, err := files.New(files.Options{
				Runner: p.db, Blobs: noBlobs{}, Repos: reader, Authz: engine, Recorder: e.audit,
				Storage: config.Storage{}, Log: quiet,
			})
			if err != nil {
				t.Fatal(err)
			}
			rawfiles.New(rawfiles.Options{Service: svc, API: idapi.APIConfig()}).Register(r)
		}
	}
	// The repository exists before the replica builds its grant index.
	if _, err := e.admin.Exec(ctx, `INSERT INTO repositories (key, class, format, config) VALUES ('generic-local', 'local', 'generic', '{}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	e.start(1, e.defaultAuth())

	// 1. A login.
	b := e.browser()
	b.mustLogin("corp")
	_, body := b.get(e.baseURL + "/api/v1/auth/whoami")
	alice, _ := jsonOf(t, body)["principal"].(map[string]any)
	if alice == nil || alice["name"] != "alice" {
		t.Fatalf("whoami: %s", body)
	}
	e.bindAdmin(alice["id"].(string))
	if !eventually(t, 20*time.Second, func() bool {
		resp, _ := b.get(e.baseURL + "/api/v1/audit/events?limit=1")
		return resp.StatusCode == http.StatusOK
	}) {
		t.Fatal("the administrator binding did not reach the decision point")
	}

	// 2. A failed login: the provider answers with the wrong nonce.
	e.idp.set(func(f *fakeIDP) { f.wrongNonce = true })
	if resp, body := e.browser().login("corp"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("failed login: %d %s", resp.StatusCode, body)
	}
	e.idp.set(func(f *fakeIDP) { f.wrongNonce = false })

	// 3. Token creation, twice: one to revoke, one to deploy with.
	create := func(desc string) (id, secret string) {
		resp, body := b.post("/api/v1/tokens", fmt.Sprintf(`{"description":%q}`, desc), nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create token: %d %s", resp.StatusCode, body)
		}
		m := jsonOf(t, body)
		return m["id"].(string), m["token"].(string)
	}
	revokeID, _ := create("e1.5 to revoke")
	_, deploySecret := create("e1.5 to deploy")

	// 4. A policy change.
	policy := `{"name":"e15-read","description":"read only","resources":{"repositories":["generic-local"]},"entries":[{"principal":"user:alice","actions":["read"]}]}`
	if resp, body := b.post("/api/v1/permission-policies", policy, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create policy: %d %s", resp.StatusCode, body)
	}

	// 5. A token revocation.
	if resp, body := b.send(http.MethodDelete, "/api/v1/tokens/"+revokeID, "", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke token: %d %s", resp.StatusCode, body)
	}

	// 6. A denied deploy: alice may read generic-local and not deploy to it.
	req, _ := http.NewRequest(http.MethodPut, e.baseURL+"/files/generic-local/e15.bin", strings.NewReader("payload"))
	req.Header.Set("Authorization", "Bearer "+deploySecret)
	req.Header.Set("Content-Type", "application/octet-stream")
	if resp, body := b.do(req); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("denied deploy: %d %s", resp.StatusCode, body)
	}

	// The asynchronous events go through the buffer.
	if err := writer.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ what, action string }{
		{"a login", audit.EventAuthLoginSucceeded},
		{"a failed login", audit.EventAuthLoginFailed},
		{"a token creation", audit.EventTokenCreated},
		{"a token revocation", "token.revoked"},
		{"a policy change", "authz.policy.created"},
		{"a denied deploy", "authz.denied"},
	} {
		t.Run("E1.5 "+tc.what+" is found through the audit API and in the log stream", func(t *testing.T) {
			var ids []string
			ok := eventually(t, 10*time.Second, func() bool {
				resp, body := b.get(e.baseURL + "/api/v1/audit/events?action=" + url.QueryEscape(tc.action) + "&limit=100")
				if resp.StatusCode != http.StatusOK {
					return false
				}
				ids = ids[:0]
				items, _ := jsonOf(t, body)["items"].([]any)
				for _, it := range items {
					if m, _ := it.(map[string]any); m != nil && m["action"] == tc.action {
						ids = append(ids, m["id"].(string))
					}
				}
				return len(ids) > 0
			})
			if !ok {
				t.Fatalf("GET /api/v1/audit/events?action=%s found nothing", tc.action)
			}
			logged := false
			for _, id := range ids {
				logged = logged || stream.has(tc.action, id)
			}
			if !logged {
				t.Errorf("none of the %d events %v is in the log stream", len(ids), ids)
			}
		})
	}
	t.Run("E1.5 the denied deploy names the action, the repository and the caller", func(t *testing.T) {
		_, body := b.get(e.baseURL + "/api/v1/audit/events?action=authz.denied&repository=generic-local&limit=10")
		items, _ := jsonOf(t, body)["items"].([]any)
		if len(items) == 0 {
			t.Fatalf("no authz.denied event: %s", body)
		}
		ev := items[0].(map[string]any)
		actor, _ := ev["actor"].(map[string]any)
		if ev["repository"] != "generic-local" || actor["name"] != "alice" || !strings.Contains(fmt.Sprint(ev["details"]), "deploy") {
			t.Errorf("event = %v", ev)
		}
	})
}
