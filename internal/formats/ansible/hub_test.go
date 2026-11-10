package ansible

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

func TestR_ANS_019_TheExchangeIsTheRefreshTokenGrantAtAuthUrl(t *testing.T) {
	a := remoteAdapter{f: New()}
	rc := formats.RemoteContext{Settings: []byte(`{"authUrl":"https://sso.example/auth/token","clientId":"my-client"}`)}
	req, key, ok, err := a.ExchangeRequest(context.Background(), rc, "the offline token")
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	body, _ := io.ReadAll(req.Body)
	if req.Method != http.MethodPost || req.URL.String() != "https://sso.example/auth/token" || req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
		string(body) != "client_id=my-client&grant_type=refresh_token&refresh_token=the+offline+token" {
		t.Errorf("%s %s %q", req.Method, req.URL, body)
	}
	if key != "hub/my-client" || req.Header.Get("Authorization") != "" {
		t.Errorf("key %q, Authorization %q", key, req.Header.Get("Authorization"))
	}
	// The client id defaults, and a repository without an authUrl does no exchange.
	rc.Settings = []byte(`{"authUrl":"https://sso.example/t"}`)
	req, key, _, _ = a.ExchangeRequest(context.Background(), rc, "x")
	if b, _ := io.ReadAll(req.Body); string(b) != "client_id=cloud-services&grant_type=refresh_token&refresh_token=x" || key != "hub/cloud-services" {
		t.Errorf("default client: %q %q", b, key)
	}
	for _, settings := range []string{``, `{}`, `{"clientId":"c"}`, `not json`} {
		if _, _, ok, err := a.ExchangeRequest(context.Background(), formats.RemoteContext{Settings: []byte(settings)}, "x"); ok || err != nil {
			t.Errorf("%q: ok %v, err %v", settings, ok, err)
		}
	}
}

func TestR_ANS_019_TheAccessTokenIsUsedUntilShortlyBeforeItExpires(t *testing.T) {
	a := remoteAdapter{f: New()}
	auth, ttl, err := a.ReadExchange(context.Background(), formats.RemoteContext{}, 200, []byte(`{"access_token":"abc","expires_in":300,"token_type":"Bearer"}`))
	// The engine takes ten seconds of the lifetime as its skew, so the token stops 30 seconds before the realm's expiry.
	if err != nil || auth != "Bearer abc" || ttl != 280*time.Second {
		t.Errorf("%q %v %v", auth, ttl, err)
	}
	if _, ttl, _ = a.ReadExchange(context.Background(), formats.RemoteContext{}, 200, []byte(`{"access_token":"abc","expires_in":5}`)); ttl != time.Second {
		t.Errorf("a short lifetime: %v", ttl)
	}
	for _, body := range []string{``, `{}`, `{"access_token":""}`, `{"access_token":"a b"}`, `{"access_token":"a\nb"}`, `{"access_token":"x","token_type":"mac"}`, `[]`} {
		if _, _, err := a.ReadExchange(context.Background(), formats.RemoteContext{}, 200, []byte(body)); errs.KindOf(err) != errs.UpstreamFailed {
			t.Errorf("%q: %v", body, err)
		}
	}
}
