package ci

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The rows of the CI issuers: the mapping between the queries of ci_issuers.sql and [Issuer], and the
// record that the audit events carry.

func queriesOf(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("ci: the transaction does not come from the PostgreSQL store")
	}
	return q.New(ptx), nil
}

// newID returns a UUIDv7 (RFC 9562), as the other identity and authorization objects do.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil { // crypto/rand does not fail on supported platforms
		panic(err)
	}
	ms := uint64(time.Now().UnixMilli()) //nolint:gosec // after 1970
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	var s [36]byte
	hex.Encode(s[0:8], b[0:4])
	s[8] = '-'
	hex.Encode(s[9:13], b[4:6])
	s[13] = '-'
	hex.Encode(s[14:18], b[6:8])
	s[18] = '-'
	hex.Encode(s[19:23], b[8:10])
	s[23] = '-'
	hex.Encode(s[24:], b[10:])
	return string(s[:])
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// toIssuer maps the columns every read statement of an issuer returns.
func toIssuer(r q.CiIssuerByIDRow) Issuer {
	is := Issuer{
		ID: r.ID, Name: r.Name, Kind: r.Kind, Issuer: r.Issuer, Description: r.Description, Enabled: r.Enabled,
		DiscoveryURL: optional(r.DiscoveryUrl), JWKSURI: optional(r.JwksUri),
		AllowedAudiences: r.AllowedAudiences, ExclusiveAudience: r.ExclusiveAudience,
		AllowedAlgorithms: r.AllowedAlgorithms, AllowedTyp: r.AllowedTyp, AllowMissingTyp: r.AllowMissingTyp,
		ClockSkewSeconds: int(r.ClockSkewSecs), RequiredClaims: r.RequiredClaims, PublicIssuer: r.PublicIssuer,
		PinClaims: r.PinClaims, SingleUseSubjectTokens: r.SingleUseSubjectTokens,
		CABundleRef: optional(r.CaBundleRef), UseOutboundProxy: r.UseOutboundProxy,
		Source: r.Source, RemovedFromConfiguration: r.RemovedFromConfiguration,
		Version: r.Version, MappingsVersion: r.MappingsVersion,
		CreatedAt: time.UnixMicro(r.CreatedUs).UTC(), UpdatedAt: time.UnixMicro(r.UpdatedUs).UTC(),
	}
	if r.MaxTtlSecs > 0 {
		v := r.MaxTtlSecs
		is.MaxTTLSeconds = &v
	}
	_ = json.Unmarshal(r.JobTokenFallback, &is.JobTokenFallback)
	return is
}

// toIssuerFromList maps a row of the list: the issuer, its mapping count and the key summary.
func toIssuerFromList(r q.CiIssuerListRow) Issuer {
	is := toIssuer(q.CiIssuerByIDRow{
		ID: r.ID, Name: r.Name, Kind: r.Kind, Issuer: r.Issuer, DiscoveryUrl: r.DiscoveryUrl, JwksUri: r.JwksUri,
		AllowedAudiences: r.AllowedAudiences, ExclusiveAudience: r.ExclusiveAudience, AllowedAlgorithms: r.AllowedAlgorithms,
		AllowedTyp: r.AllowedTyp, AllowMissingTyp: r.AllowMissingTyp, ClockSkewSecs: r.ClockSkewSecs,
		RequiredClaims: r.RequiredClaims, PublicIssuer: r.PublicIssuer, PinClaims: r.PinClaims,
		SingleUseSubjectTokens: r.SingleUseSubjectTokens, MaxTtlSecs: r.MaxTtlSecs, CaBundleRef: r.CaBundleRef,
		UseOutboundProxy: r.UseOutboundProxy, JobTokenFallback: r.JobTokenFallback, Enabled: r.Enabled,
		Description: r.Description, Source: r.Source, RemovedFromConfiguration: r.RemovedFromConfiguration,
		Version: r.Version, MappingsVersion: r.MappingsVersion, CreatedUs: r.CreatedUs, UpdatedUs: r.UpdatedUs,
	})
	is.MappingCount = int(r.MappingCount)
	sum := &KeySummary{LastError: r.LastError}
	if set, err := parseStored([]byte(r.Jwks)); err == nil {
		sum.Usable = len(set.Usable)
		sum.Ignored = len(set.Infos) - len(set.Usable)
	}
	if r.FetchedUs > 0 {
		t := time.UnixMicro(r.FetchedUs).UTC()
		sum.FetchedAt = &t
	}
	if r.LastErrorUs > 0 {
		t := time.UnixMicro(r.LastErrorUs).UTC()
		sum.LastErrorAt = &t
	}
	is.Keys = sum
	return is
}

func fallbackJSON(f JobTokenFallback) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func insertParams(is Issuer) q.CiIssuerInsertParams {
	return q.CiIssuerInsertParams{
		ID: is.ID, Name: is.Name, Kind: is.Kind, Issuer: is.Issuer,
		DiscoveryUrl: derefStr(is.DiscoveryURL), JwksUri: derefStr(is.JWKSURI),
		AllowedAudiences: is.AllowedAudiences, ExclusiveAudience: is.ExclusiveAudience,
		AllowedAlgorithms: is.AllowedAlgorithms, AllowedTyp: is.AllowedTyp, AllowMissingTyp: is.AllowMissingTyp,
		ClockSkewSecs: int64(is.ClockSkewSeconds), RequiredClaims: is.RequiredClaims, PublicIssuer: is.PublicIssuer,
		PinClaims: is.PinClaims, SingleUseSubjectTokens: is.SingleUseSubjectTokens,
		MaxTtlSecs: derefInt(is.MaxTTLSeconds), CaBundleRef: derefStr(is.CABundleRef), UseOutboundProxy: is.UseOutboundProxy,
		JobTokenFallback: fallbackJSON(is.JobTokenFallback), Enabled: is.Enabled, Description: is.Description, Source: is.Source,
	}
}

func updateParams(is Issuer) q.CiIssuerUpdateParams {
	return q.CiIssuerUpdateParams{
		ID: is.ID, Kind: is.Kind, Issuer: is.Issuer,
		DiscoveryUrl: derefStr(is.DiscoveryURL), JwksUri: derefStr(is.JWKSURI),
		AllowedAudiences: is.AllowedAudiences, ExclusiveAudience: is.ExclusiveAudience,
		AllowedAlgorithms: is.AllowedAlgorithms, AllowedTyp: is.AllowedTyp, AllowMissingTyp: is.AllowMissingTyp,
		ClockSkewSecs: int64(is.ClockSkewSeconds), RequiredClaims: is.RequiredClaims, PublicIssuer: is.PublicIssuer,
		PinClaims: is.PinClaims, SingleUseSubjectTokens: is.SingleUseSubjectTokens,
		MaxTtlSecs: derefInt(is.MaxTTLSeconds), CaBundleRef: derefStr(is.CABundleRef), UseOutboundProxy: is.UseOutboundProxy,
		JobTokenFallback: fallbackJSON(is.JobTokenFallback), Enabled: is.Enabled, Description: is.Description,
		RemovedFromConfiguration: is.RemovedFromConfiguration,
	}
}

func derefInt(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// loadByID reads one issuer inside a transaction; ok is false when there is none.
func loadByID(ctx context.Context, qs *q.Queries, id string) (Issuer, bool, error) {
	rows, err := qs.CiIssuerByID(ctx, id)
	if err != nil || len(rows) == 0 {
		return Issuer{}, false, err
	}
	return toIssuer(rows[0]), true, nil
}

// record is the wire form of an issuer as the audit events carry it (R-CI-020): the lowerCamelCase
// members of the API, with the version, and no secret in it (ca_bundle_ref is a reference). The key
// summary and the mapping count are not part of the record.
type record struct {
	ID                       string           `json:"id"`
	Name                     string           `json:"name"`
	Kind                     string           `json:"kind"`
	Issuer                   string           `json:"issuer"`
	Description              string           `json:"description"`
	Enabled                  bool             `json:"enabled"`
	DiscoveryURL             *string          `json:"discoveryUrl"`
	JWKSURI                  *string          `json:"jwksUri"`
	AllowedAudiences         []string         `json:"allowedAudiences"`
	ExclusiveAudience        bool             `json:"exclusiveAudience"`
	AllowedAlgorithms        []string         `json:"allowedAlgorithms"`
	AllowedTyp               []string         `json:"allowedTyp"`
	AllowMissingTyp          bool             `json:"allowMissingTyp"`
	ClockSkewSeconds         int              `json:"clockSkewSeconds"`
	RequiredClaims           []string         `json:"requiredClaims"`
	PublicIssuer             bool             `json:"publicIssuer"`
	PinClaims                []string         `json:"pinClaims"`
	SingleUseSubjectTokens   bool             `json:"singleUseSubjectTokens"`
	MaxTTLSeconds            *int64           `json:"maxTtlSeconds"`
	CABundleRef              *string          `json:"caBundleRef"`
	UseOutboundProxy         bool             `json:"useOutboundProxy"`
	JobTokenFallback         JobTokenFallback `json:"jobTokenFallback"`
	Source                   string           `json:"source"`
	RemovedFromConfiguration bool             `json:"removedFromConfiguration"`
	Version                  int64            `json:"version"`
}

func recordOf(is Issuer) *record {
	return &record{
		ID: is.ID, Name: is.Name, Kind: is.Kind, Issuer: is.Issuer, Description: is.Description, Enabled: is.Enabled,
		DiscoveryURL: is.DiscoveryURL, JWKSURI: is.JWKSURI, AllowedAudiences: is.AllowedAudiences,
		ExclusiveAudience: is.ExclusiveAudience, AllowedAlgorithms: is.AllowedAlgorithms, AllowedTyp: is.AllowedTyp,
		AllowMissingTyp: is.AllowMissingTyp, ClockSkewSeconds: is.ClockSkewSeconds, RequiredClaims: is.RequiredClaims,
		PublicIssuer: is.PublicIssuer, PinClaims: is.PinClaims, SingleUseSubjectTokens: is.SingleUseSubjectTokens,
		MaxTTLSeconds: is.MaxTTLSeconds, CABundleRef: is.CABundleRef, UseOutboundProxy: is.UseOutboundProxy,
		JobTokenFallback: is.JobTokenFallback, Source: is.Source, RemovedFromConfiguration: is.RemovedFromConfiguration,
		Version: is.Version,
	}
}

// sameSettings reports whether two issuers have the same settings, ignoring the version, the
// timestamps and the read-only members: `trove migrate` leaves an issuer alone when the configuration
// still declares exactly what is stored.
func sameSettings(a, b Issuer) bool {
	ra, rb := recordOf(a), recordOf(b)
	ra.Version, rb.Version = 0, 0
	ja, _ := json.Marshal(ra)
	jb, _ := json.Marshal(rb)
	return string(ja) == string(jb)
}
