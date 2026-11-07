package items

import (
	"testing"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/servefacts"
)

// R-ARCH-027: the facts of a lookup are stamped with the key only when the statement says it evaluated that kind. A kind the
// statement has no branch for would come back all false, and facts that claim to be read for the key would let a check serve a
// tombstoned version: they are left out and the check reads the rows itself.
func TestR_ARCH_027_FactsOfAKindTheStatementDidNotEvaluateAreNotStamped(t *testing.T) {
	key := servefacts.Key{Kind: servefacts.NPMLocal, Package: "p", Version: "1.0.0"}
	future := servefacts.Key{Kind: "future_kind", Package: "p", Version: "1"}
	for _, c := range []struct {
		name      string
		key       servefacts.Key
		evaluated string
		want      bool
	}{
		{"the kind that was asked", key, "npm_local", true},
		{"a kind without a branch", future, "", false},
		{"another kind than asked", key, "nuget_local", false},
		{"no key", servefacts.Key{}, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			it := itemFrom(qr("r"), c.key, q.GetLiveItemRow{Kind: KindFolder, FactKind: c.evaluated, FactTombstoned: true})
			if got := it.Facts.For(c.key); got != c.want {
				t.Errorf("Facts.For(key) = %v, want %v (facts %+v)", got, c.want, it.Facts)
			}
		})
	}
}
