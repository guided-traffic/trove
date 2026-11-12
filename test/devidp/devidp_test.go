package devidp

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	clientID = "trove-ui-dev"
	secret   = "client-secret"
	verifier = "a-verifier-of-enough-length-for-pkce-0123456789"
	redirect = "https://localhost:4200/auth/oidc/ui-dev/callback"
)

func newServer(t *testing.T, now func() time.Time) (*Provider, *httptest.Server) {
	t.Helper()
	p, err := New(Config{Issuer: "https://idp.test", ClientID: clientID, ClientSecret: secret, ClientBase: "https://localhost:4200", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(p.Handler())
	t.Cleanup(srv.Close)
	return p, srv
}

func noRedirects(srv *httptest.Server) *http.Client {
	c := srv.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

func challenge() string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func flow() url.Values {
	return url.Values{
		"client_id": {clientID}, "redirect_uri": {redirect}, "state": {"st"}, "nonce": {"nn"},
		"code_challenge": {challenge()}, "code_challenge_method": {"S256"}, "response_type": {"code"}, "scope": {"openid"},
	}
}

// signIn chooses the user on the provider's page and returns the code it sends back.
func signIn(t *testing.T, srv *httptest.Server, username string) string {
	t.Helper()
	form := flow()
	form.Set("username", username)
	resp, err := noRedirects(srv).PostForm(srv.URL+"/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: %d", resp.StatusCode)
	}
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Scheme + "://" + back.Host + back.Path; got != redirect {
		t.Fatalf("redirect to %s", got)
	}
	if back.Query().Get("state") != "st" || back.Query().Get("iss") != "https://idp.test" {
		t.Fatalf("redirect query %s", back.RawQuery)
	}
	return back.Query().Get("code")
}

func exchange(t *testing.T, srv *httptest.Server, code, clientSecret string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/token", strings.NewReader(url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier},
	}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func TestTheChooserListsTheUsersAndCarriesTheFlow(t *testing.T) {
	_, srv := newServer(t, nil)
	resp, err := srv.Client().Get(srv.URL + "/authorize?" + flow().Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(resp.Body)
	for _, want := range []string{`value="sam"`, `value="priya"`, `value="ada"`, `name="nonce" value="nn"`, `name="state" value="st"`} {
		if !strings.Contains(string(page), want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	bad := flow()
	bad.Set("code_challenge_method", "plain")
	resp, err = srv.Client().Get(srv.URL + "/authorize?" + bad.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a flow without S256: %d", resp.StatusCode)
	}
}

func TestTheIDTokenNamesTheChosenUserAndVerifiesWithThePublishedKey(t *testing.T) {
	_, srv := newServer(t, nil)
	code := signIn(t, srv, "priya")
	status, body := exchange(t, srv, code, secret)
	if status != http.StatusOK {
		t.Fatalf("token: %d %v", status, body)
	}
	parts := strings.Split(body["id_token"].(string), ".")
	if len(parts) != 3 {
		t.Fatalf("id_token has %d parts", len(parts))
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"iss": "https://idp.test", "aud": clientID, "sub": "devidp-priya", "nonce": "nn", "preferred_username": "priya"} {
		if claims[k] != want {
			t.Errorf("%s = %v, want %v", k, claims[k], want)
		}
	}

	resp, err := srv.Client().Get(srv.URL + "/jwks")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var set struct {
		Keys []struct{ N, E string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil || len(set.Keys) != 1 {
		t.Fatalf("jwks: %v %v", err, set)
	}
	n, _ := base64.RawURLEncoding.DecodeString(set.Keys[0].N)
	e, _ := base64.RawURLEncoding.DecodeString(set.Keys[0].E)
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("the signature does not verify with the published key: %v", err)
	}
}

func TestACodeIsExchangedOnceWithTheSecretAndBeforeItExpires(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	_, srv := newServer(t, func() time.Time { return now })

	code := signIn(t, srv, "sam")
	if status, _ := exchange(t, srv, code, "wrong"); status != http.StatusUnauthorized {
		t.Fatalf("a wrong secret: %d", status)
	}
	code = signIn(t, srv, "sam")
	if status, _ := exchange(t, srv, code, secret); status != http.StatusOK {
		t.Fatalf("the first exchange: %d", status)
	}
	if status, _ := exchange(t, srv, code, secret); status != http.StatusBadRequest {
		t.Fatalf("a second exchange of the same code: %d", status)
	}
	code = signIn(t, srv, "sam")
	now = now.Add(2 * time.Minute)
	if status, _ := exchange(t, srv, code, secret); status != http.StatusBadRequest {
		t.Fatalf("an expired code: %d", status)
	}
}

func TestARedirectOutsideTheClientIsRefused(t *testing.T) {
	_, srv := newServer(t, nil)
	bad := flow()
	bad.Set("redirect_uri", "https://elsewhere.test/auth/oidc/ui-dev/callback")
	resp, err := srv.Client().Get(srv.URL + "/authorize?" + bad.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a foreign redirect_uri: %d", resp.StatusCode)
	}
	resp, err = noRedirects(srv).Get(srv.URL + "/end_session?post_logout_redirect_uri=" + url.QueryEscape("https://elsewhere.test/"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a foreign post-logout target: %d", resp.StatusCode)
	}
}

func TestAnUnknownUserIsRefused(t *testing.T) {
	_, srv := newServer(t, nil)
	form := flow()
	form.Set("username", "mallory")
	resp, err := noRedirects(srv).PostForm(srv.URL+"/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown user: %d", resp.StatusCode)
	}
}
