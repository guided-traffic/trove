package fixup

import (
	"context"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/items"
)

// The item store is the storage delete path a purger empties a cache through (R-BAK-008).
var _ CacheEraser = (*items.Store)(nil)

func TestR_BAK_008_TheFormatsRegisterTheirPurgers(t *testing.T) {
	got := map[string]CachePurger{}
	for _, p := range Purgers() {
		got[p.Name] = p
	}
	for _, name := range []string{"npm", "nuget"} {
		p, ok := got[name]
		if !ok {
			t.Errorf("no purger %q (registered: %v)", name, Purgers())
			continue
		}
		// A restore that composed no storage delete path must not purge around it.
		err := p.Purge(context.Background(), nil, Env{})
		if err == nil || !strings.Contains(err.Error(), "Env.Items") {
			t.Errorf("%s: purge without Env.Items = %v", name, err)
		}
	}
}
