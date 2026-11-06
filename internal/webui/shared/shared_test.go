package shared

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/webui"
)

// The store's statements run against PostgreSQL and the object store in test/integration/uiassets; these are the parts that
// decide before either is asked.

func TestAssetAsksOnlyForHashedNames(t *testing.T) {
	t.Run("R-HA-020 a name that is not of the hashed pattern is never looked up", func(t *testing.T) {
		s := New(nil, nil, "test", nil) // a lookup would dereference the nil database
		for _, p := range []string{"index.html", "favicon.ico", "../main-ABCD1234.js", "main.js", "a/b/c/main-ABCD1234.js"} {
			if _, err := s.Asset(context.Background(), "en", p); !errors.Is(err, webui.ErrNotShared) {
				t.Errorf("Asset(%q) = %v, want ErrNotShared", p, err)
			}
		}
	})
}

func TestKeeperLifecycle(t *testing.T) {
	t.Run("R-HA-020 a keeper that never started stops at once, and one that started stops its loop", func(t *testing.T) {
		k := NewKeeper(New(nil, nil, "test", nil), nil, nil, nil)
		if k.Name() != "ui-assets" {
			t.Errorf("name %q", k.Name())
		}
		if err := k.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := k.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := k.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}
