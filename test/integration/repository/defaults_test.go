//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_REPO_063_TheDefaultsAreWhatABareCreateStores: for every class of a format with presets and of generic, the document of
// the defaults equals, member for member, the document a create of only key, class and format stores and reads back, without the
// key; the suggestion is the first free key and is nil once ten are taken.
func TestR_REPO_063_TheDefaultsAreWhatABareCreateStores(t *testing.T) {
	e := newStackEnv(t)
	rep := e.newReplica("defaults", stackOpts{})
	ctx := context.Background()
	asMap := func(t *testing.T, d repository.Document) map[string]any {
		t.Helper()
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		delete(m, "key")
		return m
	}
	for _, c := range []struct {
		format string
		class  repository.Class
	}{{"generic", repository.Local}, {"npm", repository.Local}, {"oci", repository.Remote}, {"deb", repository.Remote}, {"npm", repository.Virtual}} {
		t.Run(fmt.Sprintf("R-REPO-063 %s %s", c.format, c.class), func(t *testing.T) {
			def, err := rep.svc.Defaults(ctx, caller, "it", c.format, c.class)
			if err != nil {
				t.Fatal(err)
			}
			want := c.format + "-" + string(c.class)
			if def.SuggestedKey == nil || *def.SuggestedKey != want {
				t.Fatalf("suggested key = %v, want %s", def.SuggestedKey, want)
			}
			st, err := rep.svc.Create(ctx, caller, "it", []byte(fmt.Sprintf(`{"key":%q,"class":%q,"format":%q}`, want, c.class, c.format)))
			if err != nil {
				t.Fatal(err)
			}
			got, err := rep.svc.Get(ctx, st.Repository.Name)
			if err != nil {
				t.Fatal(err)
			}
			if a, b := asMap(t, def.Document), asMap(t, got.Document); !reflect.DeepEqual(a, b) {
				t.Fatalf("defaults\n%v\nstored\n%v", a, b)
			}
			next, err := rep.svc.Defaults(ctx, caller, "it", c.format, c.class)
			if err != nil {
				t.Fatal(err)
			}
			if next.SuggestedKey == nil || *next.SuggestedKey != want+"-2" {
				t.Fatalf("after the create the suggestion is %v", next.SuggestedKey)
			}
		})
	}
	t.Run("R-REPO-063 no suggestion once the ten keys are taken", func(t *testing.T) {
		for _, k := range []string{
			"generic-local-2", "generic-local-3", "generic-local-4", "generic-local-5", "generic-local-6",
			"generic-local-7", "generic-local-8", "generic-local-9",
		} {
			if _, err := rep.svc.Create(ctx, caller, "it", []byte(fmt.Sprintf(`{"key":%q,"class":"local","format":"generic"}`, k))); err != nil {
				t.Fatal(err)
			}
		}
		def, err := rep.svc.Defaults(ctx, caller, "it", "generic", repository.Local)
		if err != nil {
			t.Fatal(err)
		}
		if def.SuggestedKey != nil {
			t.Fatalf("suggested key = %s", *def.SuggestedKey)
		}
	})
}
