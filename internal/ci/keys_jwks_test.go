package ci

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/ci/subject"
)

func authActor() audit.Actor { return audit.Actor{Kind: "user", Name: "alice"} }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func rsaJWK(t *testing.T, bits int, extra map[string]any) map[string]any {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{"kty": "RSA", "n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes())}
	for n, v := range extra {
		m[n] = v
	}
	return m
}

func ecJWK(t *testing.T, curve elliptic.Curve, name string, extra map[string]any) map[string]any {
	t.Helper()
	k, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := k.PublicKey.Bytes()
	size := (curve.Params().BitSize + 7) / 8
	m := map[string]any{"kty": "EC", "crv": name, "x": b64(raw[1 : 1+size]), "y": b64(raw[1+size:])}
	for n, v := range extra {
		m[n] = v
	}
	return m
}

func edJWK(t *testing.T, extra map[string]any) map[string]any {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{"kty": "OKP", "crv": "Ed25519", "x": b64(pub)}
	for n, v := range extra {
		m[n] = v
	}
	return m
}

func jwks(keys ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"keys": keys})
	return b
}

func parse(t *testing.T, body []byte) keySet {
	t.Helper()
	stored, err := reduceJWKS(body)
	if err != nil {
		t.Fatal(err)
	}
	set, err := parseStored(stored)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func reasons(set keySet) map[string]string {
	out := map[string]string{}
	for _, i := range set.Infos {
		if i.Ignored {
			out[i.KID] = i.IgnoredReason
		}
	}
	return out
}

func TestR_CI_003_TheKeyRulesIgnoreAndReport(t *testing.T) {
	set := parse(t, jwks(
		rsaJWK(t, 2048, map[string]any{"kid": "good-rsa", "use": "sig", "alg": "RS256"}),
		rsaJWK(t, 2048, map[string]any{"kid": "no-use"}),
		rsaJWK(t, 2048, map[string]any{"kid": "verify", "key_ops": []string{"verify"}}),
		rsaJWK(t, 2048, map[string]any{"kid": "enc", "use": "enc"}),
		rsaJWK(t, 2048, map[string]any{"kid": "sign-only", "key_ops": []string{"sign"}}),
		rsaJWK(t, 2048, map[string]any{"kid": "empty-ops", "key_ops": []string{}}),
		rsaJWK(t, 1024, map[string]any{"kid": "weak"}),
		map[string]any{"kty": "oct", "kid": "secret", "k": "c2VjcmV0"},
		ecJWK(t, elliptic.P256(), "P-256", map[string]any{"kid": "ec256"}),
		ecJWK(t, elliptic.P384(), "P-384", map[string]any{"kid": "ec384"}),
		ecJWK(t, elliptic.P521(), "P-521", map[string]any{"kid": "ec521"}),
		edJWK(t, map[string]any{"kid": "ed"}),
		map[string]any{"kty": "RSA", "kid": "broken", "n": "!", "e": "AQAB"},
		rsaJWK(t, 2048, map[string]any{"kid": "good-rsa"}),
	))
	usable := map[string]bool{}
	for _, k := range set.Usable {
		usable[k.KID] = true
	}
	for _, kid := range []string{"good-rsa", "no-use", "verify", "ec256", "ec384", "ed"} {
		if !usable[kid] {
			t.Errorf("key %q is not usable", kid)
		}
	}
	want := map[string]string{
		"enc": `use is not "sig"`, "sign-only": `key_ops does not contain "verify"`, "empty-ops": `key_ops does not contain "verify"`,
		"weak": "fewer than 2048 bits", "secret": "no verification algorithm", "ec521": "no verification algorithm", "broken": "base64url",
	}
	got := reasons(set)
	for kid, frag := range want {
		if !strings.Contains(got[kid], frag) {
			t.Errorf("key %q: reason %q does not contain %q", kid, got[kid], frag)
		}
	}
	// The second key of the same kid is ignored, the first stays.
	dup := 0
	for _, i := range set.Infos {
		if i.KID == "good-rsa" && i.Ignored {
			dup++
		}
	}
	if dup != 1 {
		t.Errorf("duplicate kid ignored %d times, want 1", dup)
	}
	if len(set.Infos) != 14 {
		t.Errorf("every key is reported: %d infos, want 14", len(set.Infos))
	}
}

func TestR_CI_003_APrivateMemberNeverReachesTheStoredSet(t *testing.T) {
	k := rsaJWK(t, 2048, map[string]any{"kid": "k", "d": "PRIVATE-EXPONENT", "p": "PRIVATE-P", "q": "PRIVATE-Q", "dp": "x", "x5c": []string{"CERT"}, "x5u": "https://attacker.example/c", "k": "SECRET"})
	stored, err := reduceJWKS(jwks(k))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"PRIVATE", "SECRET", "CERT", "attacker.example", `"d"`, `"x5c"`} {
		if strings.Contains(string(stored), leak) {
			t.Errorf("the stored set holds %q: %s", leak, stored)
		}
	}
}

func TestR_CI_003_TheStoredFormIsTheSameWhateverTheIssuersMemberOrder(t *testing.T) {
	a := []byte(`{"keys":[{"kty":"RSA","kid":"k","e":"AQAB","n":"abc","use":"sig","extra":1}], "other": true}`)
	b := []byte(`{"other": false, "keys":[{"use":"sig","n":"abc","e":"AQAB","kid":"k","kty":"RSA"}]}`)
	sa, errA := reduceJWKS(a)
	sb, errB := reduceJWKS(b)
	if errA != nil || errB != nil || string(sa) != string(sb) {
		t.Fatalf("%s vs %s (%v, %v)", sa, sb, errA, errB)
	}
}

func TestR_CI_003_ADocumentThatIsNoKeySet(t *testing.T) {
	for _, body := range []string{`<html>NOT A KEY SET</html>`, `{"keys": 3}`, `{}`, `[]`, ``, `null`} {
		_, err := reduceJWKS([]byte(body))
		if err == nil {
			t.Errorf("%q is accepted", body)
		} else if strings.Contains(err.Error(), "NOT A KEY SET") {
			t.Errorf("the error quotes the body: %v", err)
		}
	}
	if stored, err := reduceJWKS([]byte(`{"keys": []}`)); err != nil || string(stored) != `{"keys":[]}` {
		t.Errorf("an empty set: %s, %v", stored, err)
	}
	// A key_ops member of the wrong type is not a list that contains verify.
	set := parse(t, []byte(`{"keys":[{"kty":"oct","kid":"x","key_ops":"verify"}]}`))
	if len(set.Usable) != 0 {
		t.Error("an oct key is usable")
	}
}

func TestR_CI_003_AtMostMaxStoredKeysAreKept(t *testing.T) {
	keys := make([]map[string]any, 0, maxStoredKeys+20)
	for i := range maxStoredKeys + 20 {
		keys = append(keys, map[string]any{"kty": "oct", "kid": string(rune('a'+i%26)) + strings.Repeat("x", i)})
	}
	stored, err := reduceJWKS(jwks(keys...))
	if err != nil {
		t.Fatal(err)
	}
	var set storedSet
	if err := json.Unmarshal(stored, &set); err != nil || len(set.Keys) != maxStoredKeys {
		t.Errorf("%d keys stored, want %d (%v)", len(set.Keys), maxStoredKeys, err)
	}
}

func TestR_CI_003_FindByKidAndWithoutKid(t *testing.T) {
	set := parse(t, jwks(
		rsaJWK(t, 2048, map[string]any{"kid": "r1"}), rsaJWK(t, 2048, map[string]any{"kid": "r2"}),
		ecJWK(t, elliptic.P256(), "P-256", map[string]any{"kid": "e1"}), edJWK(t, nil),
	))
	if keys := set.find("r2", "RS256"); len(keys) != 1 || keys[0].KID != "r2" {
		t.Errorf("by kid: %+v", keys)
	}
	if keys := set.find("nope", "RS256"); keys != nil {
		t.Errorf("unknown kid: %+v", keys)
	}
	if keys := set.find("", "RS256"); len(keys) != 2 {
		t.Errorf("without kid, RS256: %d keys", len(keys))
	}
	if keys := set.find("", "PS384"); len(keys) != 2 {
		t.Errorf("without kid, PS384: %d keys", len(keys))
	}
	if keys := set.find("", "ES256"); len(keys) != 1 || keys[0].KTY != "EC" {
		t.Errorf("without kid, ES256: %+v", keys)
	}
	if keys := set.find("", "EdDSA"); len(keys) != 1 || keys[0].KTY != "OKP" {
		t.Errorf("without kid, EdDSA: %+v", keys)
	}
	if keys := set.find("", "HS256"); len(keys) != 0 {
		t.Errorf("without kid, HS256 fits a key: %+v", keys)
	}
	many := make([]map[string]any, 0, 15)
	for range 15 {
		many = append(many, edJWK(t, nil))
	}
	if keys := parse(t, jwks(many...)).find("", "EdDSA"); len(keys) != subject.MaxKeysWithoutKID {
		t.Errorf("without kid, %d keys tried, want %d", len(keys), subject.MaxKeysWithoutKID)
	}
}

func TestR_CI_003_LookupCountsAMissingSetAsUnknownKid(t *testing.T) {
	var none *keyRow
	if _, ok := lookup(&keyRow{}, "", "RS256"); ok {
		t.Error("no row, no kid: found")
	}
	if _, ok := lookup(&keyRow{Exists: true}, "", "RS256"); ok {
		t.Error("a placeholder row, no kid: found")
	}
	set := parse(t, jwks(rsaJWK(t, 2048, map[string]any{"kid": "a"})))
	row := &keyRow{Exists: true, Set: set, FetchedAt: time.Unix(1700000000, 0)}
	if keys, ok := lookup(row, "", "RS256"); !ok || len(keys) != 1 {
		t.Errorf("a stored set, no kid: %v %v", keys, ok)
	}
	if _, ok := lookup(row, "b", "RS256"); ok {
		t.Error("unknown kid found")
	}
	if none.hasSet() {
		t.Error("a nil row has a set")
	}
}
