package authz

import (
	"slices"
	"testing"
)

func TestR_EP_014_ExposedBindsConfigurationReaders(t *testing.T) {
	f := exposureFixture()
	e := newTestEngine(t, f, func(o *Options) { o.Ceiling = exposureCeiling() })
	keys := []string{"files-virtual", "generic-auth", "generic-local", "generic-other", "npm-customers"}
	t.Run("R-EP-014 Q-EP-12 on an endpoint with rules only repositories with an enabled rule, for every subject", func(t *testing.T) {
		for _, s := range []Subject{adminSub, carolSub} {
			got, err := e.Exposed(WithEndpoint(t.Context(), "customers"), s, keys)
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{"files-virtual", "generic-local", "npm-customers"}; !slices.Equal(got, want) {
				t.Fatalf("%s: customers exposes %v, want %v", s.Name, got, want)
			}
		}
	})
	t.Run("R-EP-014 the ceiling never grants: a subject without a read grant still needs its capability", func(t *testing.T) {
		readable, _ := e.Readable(WithEndpoint(t.Context(), "customers"), carolSub, keys)
		exposed, _ := e.Exposed(WithEndpoint(t.Context(), "customers"), carolSub, keys)
		if len(readable) >= len(exposed) {
			t.Fatalf("readable %v, exposed %v: Exposed must apply the ceiling alone", readable, exposed)
		}
	})
	t.Run("R-EP-013 an endpoint in mode all and a request without an endpoint expose everything", func(t *testing.T) {
		for _, ctx := range []struct {
			name string
			got  func() ([]string, error)
		}{
			{"default", func() ([]string, error) { return e.Exposed(WithEndpoint(t.Context(), "default"), carolSub, keys) }},
			{"none", func() ([]string, error) { return e.Exposed(t.Context(), carolSub, keys) }},
		} {
			got, err := ctx.got()
			if err != nil || !slices.Equal(got, keys) {
				t.Fatalf("%s: %v %v", ctx.name, got, err)
			}
		}
	})
}
