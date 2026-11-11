package auth

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// countingDB is a database that records every read and transaction: the proof that a request was refused
// before the token table was asked.
type countingDB struct{ reads, txs atomic.Int64 }

func (d *countingDB) InTx(context.Context, func(meta.Tx) error) error {
	d.txs.Add(1)
	return errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "the database is not available")
}

func (d *countingDB) Read(context.Context, func(q.DBTX) error) error {
	d.reads.Add(1)
	return errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "the database is not available")
}

// TestR_BAS_005_ABastionTokenIsNoCredentialOnAnyOtherRoute: $BST as Authorization: Bearer, as the Basic password
// and as X-NuGet-ApiKey, on the npm route, the raw file route and the management API, answers 401 invalid_token
// from the real token service, and the token table is not read for it.
func TestR_BAS_005_ABastionTokenIsNoCredentialOnAnyOtherRoute(t *testing.T) {
	bst := newToken(t, token.TypeBST)
	reg, err := cache.NewRegistry(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	db := &countingDB{}
	svc := token.NewService(token.ServiceOptions{DB: db, Cache: reg, Gate: principal.Gate{Bound: time.Second}, Tokens: config.AuthTokens{CacheTTL: time.Minute}})
	s := New(Options{Tokens: svc, Uses: svc, Principals: fakeStore{}, BasicEnabled: true, APIKeyHeader: true, ClientAddr: clientAddr("203.0.113.9")})

	rules := defaultRules()
	rules.APIKeyRoute = func(*http.Request) bool { return true } // the key is read on every route of this test
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/npm/npm-all/lodash"},
		{http.MethodGet, "/files/npm-local/x"},
		{http.MethodGet, "/api/v1/repositories"},
	}
	forms := []struct {
		name string
		hdr  [2]string
	}{
		{"Authorization: Bearer", [2]string{"Authorization", "Bearer " + bst}},
		{"the Basic password", [2]string{"Authorization", basic("partners", bst)}},
		{"X-NuGet-ApiKey", [2]string{HeaderAPIKey, bst}},
	}
	for _, rt := range routes {
		for _, f := range forms {
			t.Run(f.name+" on "+rt.path, func(t *testing.T) {
				_, err := s.Authenticate(req(rt.method, rt.path, f.hdr[0], f.hdr[1]), rules)
				wantErr(t, err, http.StatusUnauthorized, errs.CodeInvalidToken)
			})
		}
	}
	if db.reads.Load() != 0 || db.txs.Load() != 0 {
		t.Fatalf("the token table was asked for a Bastion token: %d reads, %d transactions", db.reads.Load(), db.txs.Load())
	}
}
