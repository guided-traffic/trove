package ci

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
)

func limits() issuerLimits {
	return issuerLimits{BaseURL: "https://packages.example.com", TokenMaxTTL: time.Hour}
}

func base() IssuerInput {
	return IssuerInput{Name: "gitlab-main", Kind: KindGitLab, Issuer: "https://gitlab.example.com"}
}

func codes(fields []errs.FieldError) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Pointer+" "+f.Code)
	}
	return out
}

func TestR_CI_001_DefaultsAreFilledIn(t *testing.T) {
	is, fields := buildIssuer(base(), limits())
	if len(fields) > 0 {
		t.Fatalf("a minimal issuer is refused: %v", codes(fields))
	}
	if !slices.Equal(is.AllowedAudiences, []string{"https://packages.example.com", "npm:packages.example.com"}) {
		t.Errorf("default audiences = %v", is.AllowedAudiences)
	}
	if !is.ExclusiveAudience || !is.AllowMissingTyp || !is.PublicIssuer || !is.Enabled || is.SingleUseSubjectTokens || is.UseOutboundProxy {
		t.Errorf("flag defaults wrong: %+v", is)
	}
	if !slices.Equal(is.AllowedAlgorithms, []string{"RS256"}) || !slices.Equal(is.AllowedTyp, []string{"JWT"}) || is.ClockSkewSeconds != 60 {
		t.Errorf("algorithm, typ and skew defaults wrong: %v %v %d", is.AllowedAlgorithms, is.AllowedTyp, is.ClockSkewSeconds)
	}
	if !slices.Equal(is.PinClaims, []string{"project_id", "namespace_id", "job_project_id", "job_namespace_id"}) {
		t.Errorf("pin claims = %v", is.PinClaims)
	}
	if !slices.Contains(is.RequiredClaims, "project_id") || !slices.Contains(is.RequiredClaims, "jti") || len(is.RequiredClaims) != 18 {
		t.Errorf("required claims = %v", is.RequiredClaims)
	}
	if is.MaxTTLSeconds != nil || is.CABundleRef != nil || is.DiscoveryURL != nil || is.JWKSURI != nil {
		t.Errorf("optional members are not nil: %+v", is)
	}
}

func TestR_CI_001_TheDefaultAudiencesFollowTheBaseURLWithoutItsPort(t *testing.T) {
	l := limits()
	l.BaseURL = "https://packages.example.com:8443"
	is, fields := buildIssuer(base(), l)
	if len(fields) > 0 {
		t.Fatal(codes(fields))
	}
	want := []string{"https://packages.example.com:8443", "npm:packages.example.com"}
	if !slices.Equal(is.AllowedAudiences, want) {
		t.Errorf("audiences = %v, want %v", is.AllowedAudiences, want)
	}
	l.BaseURL = ""
	if _, fields := buildIssuer(base(), l); !slices.Contains(codes(fields), "/allowedAudiences required") {
		t.Errorf("a missing base URL without audiences is not refused: %v", codes(fields))
	}
	in := base()
	in.AllowedAudiences = []string{"https://other.example.com"}
	if is, fields := buildIssuer(in, l); len(fields) > 0 || len(is.AllowedAudiences) != 1 {
		t.Errorf("explicit audiences = %v %v", is.AllowedAudiences, codes(fields))
	}
}

func TestR_CI_001_RefusalsOfTheSettings(t *testing.T) {
	str := func(s string) *string { return &s }
	off, five := false, 301
	long := int64(7200)
	zero := int64(0)
	for _, tc := range []struct {
		name string
		edit func(*IssuerInput)
		want string
	}{
		{"HS256 is no algorithm", func(in *IssuerInput) { in.AllowedAlgorithms = []string{"HS256"} }, "/allowedAlgorithms algorithm_not_allowed"},
		{"none is no algorithm", func(in *IssuerInput) { in.AllowedAlgorithms = []string{"RS256", "none"} }, "/allowedAlgorithms algorithm_not_allowed"},
		{"an empty algorithm list", func(in *IssuerInput) { in.AllowedAlgorithms = []string{} }, "/allowedAlgorithms invalid_algorithms"},
		{"http issuer", func(in *IssuerInput) { in.Issuer = "http://gitlab.example.com" }, "/issuer invalid_issuer"},
		{"an issuer with a credential", func(in *IssuerInput) { in.Issuer = "https://user:pw@gitlab.example.com" }, "/issuer invalid_issuer"},
		{"an issuer with a fragment", func(in *IssuerInput) { in.Issuer = "https://gitlab.example.com/#x" }, "/issuer invalid_issuer"},
		{"an http discovery URL", func(in *IssuerInput) { in.DiscoveryURL = str("http://gitlab.example.com/d") }, "/discoveryUrl invalid_url"},
		{"an http key URL", func(in *IssuerInput) { in.JWKSURI = str("http://gitlab.example.com/k") }, "/jwksUri invalid_url"},
		{"a path claim in pin_claims", func(in *IssuerInput) { in.PinClaims = []string{"project_path"} }, "/pinClaims pin_claim_not_immutable"},
		{"gitlab.com as a private issuer", func(in *IssuerInput) { in.Issuer = "https://gitlab.com"; in.PublicIssuer = &off }, "/publicIssuer public_issuer_required"},
		{"kind github", func(in *IssuerInput) { in.Kind = KindGitHub }, "/kind kind_not_available"},
		{"kind generic", func(in *IssuerInput) { in.Kind = KindGeneric }, "/kind kind_not_available"},
		{"a display name as the kind", func(in *IssuerInput) { in.Kind = "GitHub" }, "/kind invalid_kind"},
		{"an invalid name", func(in *IssuerInput) { in.Name = "Gitlab Main" }, "/name invalid_name"},
		{"a clock skew above 300", func(in *IssuerInput) { in.ClockSkewSeconds = &five }, "/clockSkewSeconds invalid_clock_skew"},
		{"a max TTL above the ceiling", func(in *IssuerInput) { in.MaxTTLSeconds = &long }, "/maxTtlSeconds invalid_max_ttl"},
		{"a max TTL of zero", func(in *IssuerInput) { in.MaxTTLSeconds = &zero }, "/maxTtlSeconds invalid_max_ttl"},
		{"the job-token fallback", func(in *IssuerInput) { in.JobTokenFallback = &JobTokenFallback{Enabled: true} }, "/jobTokenFallback/enabled job_token_fallback_not_available"},
		{"a bundle variable outside the prefix", func(in *IssuerInput) { in.CABundleRef = str("env:GITLAB_CA") }, "/caBundleRef invalid_ca_bundle_ref"},
		{"a relative bundle path", func(in *IssuerInput) { in.CABundleRef = str("file:ca.crt") }, "/caBundleRef invalid_ca_bundle_ref"},
		{"an empty audience", func(in *IssuerInput) { in.AllowedAudiences = []string{""} }, "/allowedAudiences invalid_audiences"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.edit(&in)
			_, fields := buildIssuer(in, limits())
			if !slices.Contains(codes(fields), tc.want) {
				t.Errorf("fields = %v, want %q", codes(fields), tc.want)
			}
		})
	}
	five = 300
	if _, fields := buildIssuer(IssuerInput{Name: "gitlab-main", Kind: KindGitLab, Issuer: "https://gitlab.example.com", ClockSkewSeconds: &five}, limits()); len(fields) > 0 {
		t.Errorf("a skew of 300 is refused: %v", codes(fields))
	}
}

func TestR_CI_001_AllowedPinClaimsAndBundles(t *testing.T) {
	ref := "file:/var/run/secrets/gitlab-ca/ca.crt"
	in := base()
	in.PinClaims = []string{"project_id"}
	in.CABundleRef = &ref
	is, fields := buildIssuer(in, limits())
	if len(fields) > 0 {
		t.Fatal(codes(fields))
	}
	if !slices.Equal(is.PinClaims, []string{"project_id"}) || is.CABundleRef == nil || *is.CABundleRef != ref {
		t.Errorf("pin claims %v, bundle %v", is.PinClaims, is.CABundleRef)
	}
}

func TestQ_CI_30_AValidationErrorCarriesANamedRuleAsItsCode(t *testing.T) {
	err := validationError([]errs.FieldError{field("/kind", "kind_not_available", "x")})
	e, ok := errs.As(err)
	if !ok || e.Code != "kind_not_available" || e.Status() != 422 || len(e.Fields) != 1 {
		t.Fatalf("one field with a management code: %+v", e)
	}
	err = validationError([]errs.FieldError{field("/kind", "invalid_kind", "x")})
	if e, _ := errs.As(err); e.Code != errs.CodeValidation || e.Status() != 422 {
		t.Errorf("a field code that is no management code: %+v", e)
	}
	err = validationError([]errs.FieldError{field("/name", "invalid_name", "y"), field("/kind", "kind_not_available", "x")})
	if e, _ := errs.As(err); e.Code != "kind_not_available" || len(e.Fields) != 2 {
		t.Errorf("a named rule among several fields is the code, all stay in errors[]: %+v", e)
	}
	err = validationError([]errs.FieldError{field("/name", "invalid_name", "y"), field("/kind", "invalid_kind", "x")})
	if e, _ := errs.As(err); e.Code != errs.CodeValidation || !strings.Contains(e.Detail, "2 fields") {
		t.Errorf("several fields without a named rule: %+v", e)
	}
	if validationError(nil) != nil {
		t.Error("no fields is an error")
	}
}

func TestR_CI_002_TheEventOfAPutFollowsEnabled(t *testing.T) {
	on, off := Issuer{Enabled: true}, Issuer{Enabled: false}
	for _, tc := range []struct {
		before, after Issuer
		want          string
	}{
		{on, off, "ci.issuer.disabled"}, {off, on, "ci.issuer.enabled"}, {on, on, "ci.issuer.updated"}, {off, off, "ci.issuer.updated"},
	} {
		if got := replaceEvent(tc.before, tc.after); got != tc.want {
			t.Errorf("%v -> %v: %q, want %q", tc.before.Enabled, tc.after.Enabled, got, tc.want)
		}
	}
}

func TestR_CI_002_TheEventRecordsBeforeAfterAndTheJournalEntry(t *testing.T) {
	before := Issuer{ID: "i-1", Name: "gitlab-main", Issuer: "https://gitlab.example.com", Enabled: true, Version: 1, Source: SourceAPI}
	after := before
	after.Enabled, after.Version = false, 2
	ev := issuerEvent("ci.issuer.disabled", authActor(), &before, &after, false, 0)
	if ev.Journal == nil || !strings.Contains(string(ev.Journal), `"before"`) || strings.Contains(string(ev.Journal), `"after"`) {
		t.Errorf("a disable journals the record before the change: %s", ev.Journal)
	}
	if !strings.Contains(string(ev.Details), `"before"`) || !strings.Contains(string(ev.Details), `"after"`) {
		t.Errorf("the event shows both records: %s", ev.Details)
	}
	up := issuerEvent("ci.issuer.updated", authActor(), &before, &after, false, 0)
	if !strings.Contains(string(up.Journal), `"after"`) {
		t.Errorf("an update journals before and after: %s", up.Journal)
	}
	created := issuerEvent("ci.issuer.created", authActor(), nil, &after, false, 0)
	if created.Journal != nil {
		t.Errorf("a create is not journaled: %s", created.Journal)
	}
	del := issuerEvent("ci.issuer.deleted", authActor(), &before, nil, true, 3)
	if !strings.Contains(string(del.Details), `"cascade":true`) || !strings.Contains(string(del.Details), `"mappingsDeleted":3`) || del.Journal == nil {
		t.Errorf("delete event: %s / %s", del.Details, del.Journal)
	}
	if strings.Contains(string(ev.Details), "caBundleRef\":\"file") {
		t.Error("unexpected bundle content")
	}
}

func TestR_CI_001_SameSettingsIgnoresVersionAndTimes(t *testing.T) {
	a, fields := buildIssuer(base(), limits())
	if len(fields) > 0 {
		t.Fatal(codes(fields))
	}
	b := a
	b.Version, b.MappingsVersion, b.MappingCount = 7, 3, 12
	if !sameSettings(a, b) {
		t.Error("versions and counts are settings")
	}
	b.Description = "changed"
	if sameSettings(a, b) {
		t.Error("a changed description is not the same")
	}
}

func TestFlightSharesOneCall(t *testing.T) {
	var f flight[int]
	release := make(chan struct{})
	started := make(chan struct{})
	calls := 0
	done := make(chan int, 3)
	run := func() {
		v, _, err := f.do("k", func() (int, error) {
			calls++
			close(started)
			<-release
			return 42, nil
		})
		if err != nil {
			t.Error(err)
		}
		done <- v
	}
	go run()
	<-started
	go run()
	go run()
	time.Sleep(50 * time.Millisecond)
	close(release)
	for range 3 {
		if v := <-done; v != 42 {
			t.Errorf("value %d", v)
		}
	}
	if calls != 1 {
		t.Errorf("the function ran %d times", calls)
	}
	// A finished call leaves nothing behind.
	if _, shared, _ := f.do("k", func() (int, error) { return 1, errors.New("x") }); shared {
		t.Error("a later call joined a finished one")
	}
}
