package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

const forwardedBST = "trv_bst_forwardedforwardedforwardedforwar"

// oneBastion knows one Bastion token, forwardedBST.
type oneBastion struct{ seen []bastionmain.AuthRequest }

func (o *oneBastion) Authenticate(_ context.Context, raw string, req bastionmain.AuthRequest) (bastionmain.BastionToken, error) {
	o.seen = append(o.seen, req)
	if raw != forwardedBST {
		return bastionmain.BastionToken{}, bastionmain.ErrTokenInvalid
	}
	return bastionmain.BastionToken{TokenID: "t-bst", BastionID: "b-1", Name: "partners", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

// forwardedChain is the default endpoint's chain with its real resolver (server.trusted_proxies), the recognition
// with the composition root's re-derivation (bastions.trusted_proxies) and a handler that answers what the chain
// made of the request.
func forwardedChain(t *testing.T, serverProxies, bastionProxies []string) (http.Handler, *oneBastion, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	a := &App{cfg: &config.Config{}, parts: &wiring{}, log: slog.New(slog.NewTextHandler(&logs, nil))}
	a.cfg.Server.BaseURL = "https://trove.example.com"
	a.cfg.Server.AdditionalBaseURLs = []string{"https://packages.example.com"}
	a.cfg.Server.ForwardedHeader = string(httpserver.HeaderXForwardedFor)
	a.cfg.Bastions.TrustedProxies = bastionProxies
	rt := &endpointRuntime{cfg: config.ResolvedEndpoint{Name: "default", Default: true, BaseURL: a.cfg.Server.BaseURL, TrustedProxies: serverProxies}}
	forwarded, err := a.bastionForwarded(rt)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := httpserver.NewResolver(serverProxies, a.cfg.Server.BaseURL, a.cfg.Server.AdditionalBaseURLs)
	if err != nil {
		t.Fatal(err)
	}
	tokens := &oneBastion{}
	router := httpserver.NewClientRouter()
	router.HandleFunc("GET /api/npm/{projectKey}/{repoKey}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		info := httpserver.InfoFrom(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]string{"client": info.Client.String(), "base": info.BaseURL})
	})
	h := httpserver.Chain{
		Router: router, Resolver: resolver,
		Bastion: bastionmain.Recognize(bastionmain.RecognizeOptions{
			Tokens: tokens, Forwarded: forwarded,
			Source: func(r *http.Request) (string, string) {
				src := httpserver.SourceFrom(r.Context())
				return src.ClientAddr, src.UserAgent
			},
			Problem: func(w http.ResponseWriter, r *http.Request, err error) { errs.Write(w, r, err, "", nil) },
		}),
	}.Handler()
	return h, tokens, &logs
}

func through(t *testing.T, h http.Handler, peer, xff, bst string) (client, base string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/npm/it/npm-all/lodash", nil)
	r.RemoteAddr = peer + ":40000"
	r.Header.Set("X-Forwarded-For", xff)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "packages.example.com")
	if bst != "" {
		r.Header.Set(iface.HeaderAuthorization, "Bearer "+bst)
		r.Header.Set(iface.HeaderInterface, strconv.Itoa(iface.InterfaceVersion))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		return "", ""
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return got["client"], got["base"]
}

// TestR_BAS_017_TheBastionsForwardedHeadersCountOnlyForARecognizedBastionRequest: with the Bastion's egress in
// bastions.trusted_proxies and not in server.trusted_proxies (Q-BAS-116), a request from that address without a
// Bastion token cannot choose its client address or the response base URL; the same request with a valid Bastion
// token is resolved to the caller's address and the Bastion's host. A valid token from an address that is no
// listed egress forwards nothing. Behind the main installation's own ingress the ingress keeps working as before,
// and the entry it appends for the Bastion is what bastions.trusted_proxies is matched against. The token's source
// is the address the request came from, before the Bastion's headers count.
func TestR_BAS_017_TheBastionsForwardedHeadersCountOnlyForARecognizedBastionRequest(t *testing.T) {
	const egress, caller = "198.51.100.20", "203.0.113.7"
	t.Run("the Bastion connects directly", func(t *testing.T) {
		h, tokens, _ := forwardedChain(t, nil, []string{egress + "/32"})
		if c, b := through(t, h, egress, caller, ""); c != egress || b != "https://trove.example.com" {
			t.Errorf("no Bastion token: client %s, base %s", c, b)
		}
		if c, b := through(t, h, egress, caller, "trv_bst_notavalidtokennotavalidtokennotav"); c != "" || b != "" {
			t.Errorf("a rejected Bastion token was served: client %s, base %s", c, b)
		}
		if c, b := through(t, h, egress, caller, forwardedBST); c != caller || b != "https://packages.example.com" {
			t.Errorf("a recognized Bastion request: client %s, base %s", c, b)
		}
		if c, b := through(t, h, "192.0.2.50", caller, forwardedBST); c != "192.0.2.50" || b != "https://trove.example.com" {
			t.Errorf("a valid Bastion token from an address that is no listed egress: client %s, base %s", c, b)
		}
		for _, req := range tokens.seen {
			if req.ClientAddr == caller {
				t.Errorf("the token's source is the forwarded caller, not the sender: %+v", req)
			}
		}
	})
	t.Run("behind the main installation's ingress", func(t *testing.T) {
		const ingress = "10.0.0.5"
		h, _, _ := forwardedChain(t, []string{ingress}, []string{egress})
		if c, _ := through(t, h, ingress, "192.0.2.9", ""); c != "192.0.2.9" {
			t.Errorf("an ordinary client behind the ingress: %s", c)
		}
		if c, b := through(t, h, ingress, caller+", "+egress, ""); c != egress || b != "https://packages.example.com" {
			// The ingress itself is trusted for the host, as before (R-OPS-005); the address is the Bastion's.
			t.Errorf("the Bastion's egress without a token: client %s, base %s", c, b)
		}
		if c, _ := through(t, h, ingress, caller+", "+egress, forwardedBST); c != caller {
			t.Errorf("a recognized Bastion request behind the ingress: client %s", c)
		}
		if c, _ := through(t, h, ingress, caller+", 192.0.2.50", forwardedBST); c != "192.0.2.50" {
			t.Errorf("a valid Bastion token from elsewhere behind the ingress: client %s", c)
		}
	})
	t.Run("an egress that server.trusted_proxies covers is trusted for everyone, which is logged", func(t *testing.T) {
		_, _, logs := forwardedChain(t, []string{"198.51.100.0/24"}, []string{egress})
		if !strings.Contains(logs.String(), "bastions.trusted_proxies is also in server.trusted_proxies") {
			t.Errorf("no warning: %s", logs.String())
		}
	})
	t.Run("without bastions.trusted_proxies nothing is re-derived", func(t *testing.T) {
		h, _, _ := forwardedChain(t, nil, nil)
		if c, _ := through(t, h, egress, caller, forwardedBST); c != egress {
			t.Errorf("client %s", c)
		}
	})
}
