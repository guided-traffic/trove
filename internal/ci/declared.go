package ci

import "github.com/guided-traffic/trove/internal/config"

// DeclaredFromConfig converts an entry of ci.issuers (R-CI-001, Q-CI-16) to the input the issuer
// service applies: the same fields, the keys left out stay nil and take the defaults of R-CI-001 when
// [Issuers.ApplyDeclared] stores the issuer.
func DeclaredFromConfig(c config.CIIssuer) DeclaredIssuer {
	return DeclaredIssuer{
		Name: c.Name, Kind: c.Kind, Issuer: c.Issuer, Description: c.Description, Enabled: c.Enabled,
		DiscoveryURL: c.DiscoveryURL, JWKSURI: c.JWKSURI, AllowedAudiences: c.AllowedAudiences,
		ExclusiveAudience: c.ExclusiveAudience, AllowedAlgorithms: c.AllowedAlgorithms, AllowedTyp: c.AllowedTyp,
		AllowMissingTyp: c.AllowMissingTyp, ClockSkewSeconds: c.ClockSkewSeconds, RequiredClaims: c.RequiredClaims,
		PublicIssuer: c.PublicIssuer, PinClaims: c.PinClaims, SingleUseSubjectTokens: c.SingleUseSubjectTokens,
		MaxTTLSeconds: c.MaxTTLSeconds, CABundleRef: c.CABundleRef, UseOutboundProxy: c.UseOutboundProxy,
	}
}

// DeclaredFromConfigList converts ci.issuers.
func DeclaredFromConfigList(list []config.CIIssuer) []DeclaredIssuer {
	out := make([]DeclaredIssuer, 0, len(list))
	for _, c := range list {
		out = append(out, DeclaredFromConfig(c))
	}
	return out
}
