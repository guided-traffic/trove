package config

import (
	"slices"
	"strings"
	"testing"
)

func TestR_EP_010_CredentialRuleConfiguration(t *testing.T) {
	t.Run("R-EP-010 an empty list of token types stops start-up instead of accepting every type", func(t *testing.T) {
		_, err := loadEndpoints(t, customersYAML+"      auth: {token_types: []}\n")
		if err == nil || !strings.Contains(err.Error(), "server.endpoints.customers.auth.token_types is empty") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-EP-010 Q-EP-10 the defaults: SATs bound to the endpoint, no anonymous; the default endpoint unchanged", func(t *testing.T) {
		l, err := loadEndpoints(t, customersYAML)
		if err != nil {
			t.Fatal(err)
		}
		eps := l.Config.Server.ResolveEndpoints()
		def, cust := eps[0], eps[1]
		if !slices.Equal(cust.TokenTypes, []string{"sat"}) || !cust.BoundTokensOnly || cust.Anonymous || cust.Exposure != ExposureRules || cust.RateLimitClass != "external" {
			t.Fatalf("customers: %+v", cust)
		}
		if !slices.Equal(def.TokenTypes, []string{"pat", "sat", "cit"}) || def.BoundTokensOnly || !def.Anonymous || def.Exposure != ExposureAll || def.RateLimitClass != "" {
			t.Fatalf("default: %+v", def)
		}
	})
}
