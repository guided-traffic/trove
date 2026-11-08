package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/qname"
)

func TestCreateHooksRunForTheirFormatOnly(t *testing.T) {
	resetCreateHooks()
	t.Cleanup(resetCreateHooks)
	var got []string
	RegisterCreateHook("deb", func(_ context.Context, _ meta.Tx, repo CreatedRepo, class string) error {
		got = append(got, repo.Name.String()+" "+class)
		return nil
	})
	repo := CreatedRepo{ID: 7, Name: qname.Repo{Project: "web", Key: "deb-local"}, Format: "deb"}
	if err := runCreateHooks(context.Background(), nil, repo, "local"); err != nil {
		t.Fatal(err)
	}
	other := CreatedRepo{ID: 8, Name: qname.Repo{Project: "web", Key: "npm-local"}, Format: "npm"}
	if err := runCreateHooks(context.Background(), nil, other, "local"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "web/deb-local local" {
		t.Fatalf("hook calls = %v", got)
	}
}

func TestCreateHookErrorIsReturned(t *testing.T) {
	resetCreateHooks()
	t.Cleanup(resetCreateHooks)
	boom := errors.New("boom")
	RegisterCreateHook("deb", func(context.Context, meta.Tx, CreatedRepo, string) error { return boom })
	err := runCreateHooks(context.Background(), nil, CreatedRepo{Format: "deb"}, "virtual")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the hook's error so the creation rolls back", err)
	}
}

func TestCreateHookRegisteredTwicePanics(t *testing.T) {
	resetCreateHooks()
	t.Cleanup(resetCreateHooks)
	fn := func(context.Context, meta.Tx, CreatedRepo, string) error { return nil }
	RegisterCreateHook("deb", fn)
	defer func() {
		if recover() == nil {
			t.Fatal("a second registration did not panic")
		}
	}()
	RegisterCreateHook("deb", fn)
}
