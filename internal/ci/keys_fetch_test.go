package ci

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/test/oidcstub"
)

// The fetch of R-CI-003 against the issuer double, in process: through the outbound factory with the
// egress address policy (the loopback address of the double allowed, the metadata address still denied).

type double struct {
	stub *oidcstub.Server
	run  *oidcstub.Running
	ca   *oidcstub.SelfSigned
}

func startDouble(t *testing.T) *double {
	t.Helper()
	stub, err := oidcstub.New(oidcstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	run, ca, err := stub.StartSelfSigned()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	return &double{stub: stub, run: run, ca: ca}
}

// bundle writes the double's CA to a file and returns the ca_bundle_ref of it.
func (d *double) bundle(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, d.ca.CAPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return "file:" + path
}

func (d *double) issuer(t *testing.T, withBundle bool) Issuer {
	t.Helper()
	is := Issuer{ID: "i-1", Name: "stub", Kind: KindGitLab, Issuer: d.stub.Issuer(), AllowedAlgorithms: []string{"RS256"}}
	if withBundle {
		ref := d.bundle(t)
		is.CABundleRef = &ref
	}
	return is
}

func testFetcher(t *testing.T, mut func(*config.CISigningKeys)) *fetcher {
	t.Helper()
	out := config.Outbound{
		TLS: config.OutboundTLS{UseSystemRoots: true, MinVersion: "1.2"},
		AddressPolicy: config.OutboundAddressPolicy{
			DenyCIDRs:  []string{"127.0.0.0/8", "169.254.0.0/16", "10.0.0.0/8"},
			AllowCIDRs: []string{"127.0.0.0/8"},
		},
	}
	f := outbound.New(out)
	if err := f.Err(); err != nil {
		t.Fatal(err)
	}
	cfg := config.CISigningKeys{
		DefaultRefreshInterval: time.Hour, MinRefreshInterval: 5 * time.Minute, MaxRefreshInterval: 24 * time.Hour,
		FetchTimeout: 2 * time.Second, MaxResponseBytes: 1 << 20,
	}
	if mut != nil {
		mut(&cfg)
	}
	return newFetcher(f, out, cfg, time.Now)
}

func fetchErr(t *testing.T, err error) *FetchError {
	t.Helper()
	var fe *FetchError
	if !errors.As(err, &fe) {
		t.Fatalf("error %v is no FetchError", err)
	}
	return fe
}

func TestR_CI_003_FetchesDiscoveryAndKeysThroughTheIssuersOwnBundle(t *testing.T) {
	d := startDouble(t)
	f := testFetcher(t, nil)
	res, err := f.fetch(context.Background(), d.issuer(t, true))
	if err != nil {
		t.Fatal(err)
	}
	// The double serves what GitLab 19.0.1 served (test/oidcstub/testdata/issuer.json): two RSA keys and
	// Cache-Control: max-age=86400.
	if len(res.Set.Usable) != 2 || res.JWKSURI != d.stub.JWKSURL() {
		t.Errorf("set %d keys from %q, want GitLab's two", len(res.Set.Usable), res.JWKSURI)
	}
	if res.MaxAge != 24*time.Hour {
		t.Errorf("max-age = %v, the double announces GitLab's 24 hours", res.MaxAge)
	}
	if c := d.stub.Counts(); c.Discovery != 1 || c.JWKS != 1 {
		t.Errorf("counts %+v", c)
	}
}

func TestR_CI_003_AnIssuerWithoutItsBundleFailsTheTLSCheck(t *testing.T) {
	d := startDouble(t)
	_, err := testFetcher(t, nil).fetch(context.Background(), d.issuer(t, false))
	if fe := fetchErr(t, err); fe.Class != classTLS || fe.Host != "127.0.0.1" {
		t.Errorf("error %v, want %s", fe, classTLS)
	}
}

func TestR_CI_003_ABundleThatIsMissingIsATrustStoreFailure(t *testing.T) {
	d := startDouble(t)
	is := d.issuer(t, false)
	ref := "file:" + filepath.Join(t.TempDir(), "missing.crt")
	is.CABundleRef = &ref
	_, err := testFetcher(t, nil).fetch(context.Background(), is)
	if fe := fetchErr(t, err); fe.Class != classTrustStore {
		t.Errorf("error %v", fe)
	}
}

func TestR_CI_003_AJWKSURIAtTheMetadataAddressGetsNoConnection(t *testing.T) {
	d := startDouble(t)
	if err := d.stub.SetFault(oidcstub.Fault{JWKSURI: "https://169.254.169.254/keys"}); err != nil {
		t.Fatal(err)
	}
	_, err := testFetcher(t, nil).fetch(context.Background(), d.issuer(t, true))
	fe := fetchErr(t, err)
	if fe.Class != classEgressDenied || fe.Host != "169.254.169.254" {
		t.Errorf("error %v, want %s at the metadata host", fe, classEgressDenied)
	}
	if strings.Contains(fe.Error(), "169.254.0.0/16") || strings.Contains(fe.Error(), "address") {
		t.Errorf("the error names the rule: %v", fe)
	}
}

func TestR_CI_003_ANonJSONBodyLeavesNoByteInTheError(t *testing.T) {
	for _, target := range []string{oidcstub.TargetDiscovery, oidcstub.TargetJWKS} {
		d := startDouble(t)
		if err := d.stub.SetFault(oidcstub.Fault{NotJSON: true, Target: target}); err != nil {
			t.Fatal(err)
		}
		_, err := testFetcher(t, nil).fetch(context.Background(), d.issuer(t, true))
		fe := fetchErr(t, err)
		if fe.Class != classInvalidResponse {
			t.Errorf("%s: error %v", target, fe)
		}
		if strings.Contains(fe.Error(), oidcstub.NonJSONMarker) || strings.Contains(err.Error(), oidcstub.NonJSONMarker) {
			t.Errorf("%s: the error holds a byte of the body: %v", target, err)
		}
	}
}

func TestR_CI_003_FailureClasses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fault  oidcstub.Fault
		cfg    func(*config.CISigningKeys)
		class  string
		status int
	}{
		{"an HTTP error status", oidcstub.Fault{Status: 503, Target: oidcstub.TargetJWKS}, nil, classHTTPStatus, 503},
		{"the issuer's discovery answers 500", oidcstub.Fault{Status: 500, Target: oidcstub.TargetDiscovery}, nil, classHTTPStatus, 500},
		{"a discovery document of another issuer", oidcstub.Fault{IssuerMismatch: "https://other.example.com"}, nil, classIssuerMismatch, 0},
		{"no common algorithm", oidcstub.Fault{NoCommonAlgorithm: true}, nil, classAlgorithmsNotOffer, 0},
		{"a response above the limit", oidcstub.Fault{OversizeBytes: 5000, Target: oidcstub.TargetJWKS}, func(c *config.CISigningKeys) { c.MaxResponseBytes = 4096 }, classResponseTooLarge, 0},
		{"a delay past the timeout", oidcstub.Fault{Delay: oidcstub.Duration(3 * time.Second), Target: oidcstub.TargetJWKS}, func(c *config.CISigningKeys) { c.FetchTimeout = time.Second }, classTimeout, 0},
		{"an unreachable issuer", oidcstub.Fault{Unreachable: true}, nil, classConnection, 0},
		{"four redirects", oidcstub.Fault{Redirect: &oidcstub.Redirect{Hops: 4}, Target: oidcstub.TargetJWKS}, nil, classTooManyRedirects, 0},
		{"a redirect out of TLS", oidcstub.Fault{Redirect: &oidcstub.Redirect{Hops: 1, ToHTTP: true}, Target: oidcstub.TargetJWKS}, nil, classInsecureRedirect, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := startDouble(t)
			if err := d.stub.SetFault(tc.fault); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			_, err := testFetcher(t, tc.cfg).fetch(context.Background(), d.issuer(t, true))
			fe := fetchErr(t, err)
			if fe.Class != tc.class || fe.Status != tc.status {
				t.Errorf("error %q, want class %s status %d", fe, tc.class, tc.status)
			}
			if time.Since(start) > 2500*time.Millisecond {
				t.Errorf("the fetch took %v", time.Since(start))
			}
		})
	}
}

func TestR_CI_003_ThreeRedirectsAreFollowedAndNoPlainRequestIsMade(t *testing.T) {
	d := startDouble(t)
	if err := d.stub.SetFault(oidcstub.Fault{Redirect: &oidcstub.Redirect{Hops: 3}, Target: oidcstub.TargetJWKS}); err != nil {
		t.Fatal(err)
	}
	if _, err := testFetcher(t, nil).fetch(context.Background(), d.issuer(t, true)); err != nil {
		t.Fatalf("three hops: %v", err)
	}
	if err := d.stub.SetFault(oidcstub.Fault{Redirect: &oidcstub.Redirect{Hops: 1, ToHTTP: true}, Target: oidcstub.TargetJWKS}); err != nil {
		t.Fatal(err)
	}
	d.stub.Reset()
	_, _ = testFetcher(t, nil).fetch(context.Background(), d.issuer(t, true))
	if c := d.stub.Counts(); c.Plain != 0 {
		t.Errorf("a request reached the plain listener: %+v", c)
	}
}

func TestR_CI_003_NoUsableKeyIsAFailure(t *testing.T) {
	stub, err := oidcstub.New(oidcstub.Config{NoDefaultKey: true})
	if err != nil {
		t.Fatal(err)
	}
	run, ca, err := stub.StartSelfSigned()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Close() }()
	d := &double{stub: stub, run: run, ca: ca}
	_, err = testFetcher(t, nil).fetch(context.Background(), d.issuer(t, true))
	if fe := fetchErr(t, err); fe.Class != classNoUsableKeys {
		t.Errorf("error %v", fe)
	}
	if _, err := stub.AddKey(oidcstub.KeyOptions{Use: "enc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := stub.AddKey(oidcstub.KeyOptions{Type: oidcstub.RSA1024}); err != nil {
		t.Fatal(err)
	}
	_, err = testFetcher(t, nil).fetch(context.Background(), d.issuer(t, true))
	if fe := fetchErr(t, err); fe.Class != classNoUsableKeys {
		t.Errorf("an enc key and a 1024-bit key: %v", fe)
	}
}

func TestR_CI_003_TheJWKSURIOverrideAndAnHTTPURLAreHonoured(t *testing.T) {
	d := startDouble(t)
	is := d.issuer(t, true)
	override := d.run.URL + oidcstub.JWKSPath
	is.JWKSURI = &override
	if _, err := testFetcher(t, nil).fetch(context.Background(), is); err != nil {
		t.Fatal(err)
	}
	plain := d.run.PlainURL + oidcstub.JWKSPath
	is.JWKSURI = &plain
	d.stub.Reset()
	_, err := testFetcher(t, nil).fetch(context.Background(), is)
	if fe := fetchErr(t, err); fe.Class != classInvalidURL {
		t.Errorf("an http key URL: %v", fe)
	}
	if c := d.stub.Counts(); c.Plain != 0 {
		t.Errorf("the plain listener was contacted: %+v", c)
	}
}

func TestR_CI_003_TheRefreshTimeFollowsMaxAgeClamped(t *testing.T) {
	cfg := config.CISigningKeys{DefaultRefreshInterval: time.Hour, MinRefreshInterval: 5 * time.Minute, MaxRefreshInterval: 24 * time.Hour}
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{
		{"", time.Hour},
		{"max-age=600", 10 * time.Minute},
		{"public, max-age=30", 5 * time.Minute},
		{"max-age=9999999", 24 * time.Hour},
		{"no-store", time.Hour},
		{"max-age=abc", time.Hour},
		{"MAX-AGE=900", 15 * time.Minute},
	} {
		if got := nextRefresh(cfg, maxAge(tc.header)); got != tc.want {
			t.Errorf("%q: %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestR_CI_003_TheFailureLineHoldsClassStatusAndHostOnly(t *testing.T) {
	fe := &FetchError{Class: classHTTPStatus, Status: 502, Host: "gitlab.example.com"}
	if fe.Error() != "http_status status=502 host=gitlab.example.com" {
		t.Errorf("line = %q", fe.Error())
	}
	if (&FetchError{Class: classEgressDenied, Host: "169.254.169.254"}).Error() != "egress_denied host=169.254.169.254" {
		t.Error("an egress line")
	}
	for class, code := range map[string]string{
		classIssuerMismatch: "issuer_mismatch", classNoUsableKeys: "no_usable_keys", classAlgorithmsNotOffer: "algorithms_not_offered",
		classTimeout: "discovery_unreachable", classEgressDenied: "discovery_unreachable", classHTTPStatus: "discovery_unreachable",
	} {
		if got := string((&FetchError{Class: class}).code()); got != code {
			t.Errorf("%s: code %q, want %q", class, got, code)
		}
	}
}

func TestBundleRefSyntax(t *testing.T) {
	for ref, ok := range map[string]bool{
		"file:/etc/ssl/ca.crt": true, "file:relative.crt": false, "env:CA": false, "/etc/ca.crt": false, "file:": false, "file:/a\nb": false, "env:TROVE_CIC_GITLAB": true, "env:TROVE_CIC_": false, "env:TROVE_SECRETS_X": false, "env:": false,
	} {
		if err := validBundleRef(ref); (err == nil) != ok {
			t.Errorf("%q: %v", ref, err)
		}
	}
}

func TestQ_CI_31_ABundleInAnEnvironmentVariableAddsToTheTrust(t *testing.T) {
	d := startDouble(t)
	t.Setenv("TROVE_CIC_STUB", string(d.ca.CAPEM))
	is := d.issuer(t, false)
	ref := "env:TROVE_CIC_STUB"
	is.CABundleRef = &ref
	if _, err := testFetcher(t, nil).fetch(context.Background(), is); err != nil {
		t.Fatalf("fetch with an env bundle: %v", err)
	}
	ref = "env:TROVE_CIC_UNSET"
	_, err := testFetcher(t, nil).fetch(context.Background(), is)
	if fe := fetchErr(t, err); fe.Class != classTrustStore {
		t.Errorf("an unset variable: %v", fe)
	}
}
