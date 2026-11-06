package app

import (
	"context"
	"fmt"

	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/webui"
	"github.com/guided-traffic/trove/internal/webui/shared"
)

// AreaUIAssets is the UI's shared asset store (R-HA-020, Q-HA-120): a replica of a role that serves the UI publishes the
// hashed files of its embedded build before it turns ready, keeps them renewed while it runs, and serves the hashed files
// of the other release of a rolling upgrade from it.
const AreaUIAssets Area = "ui-assets"

// uiAssetsArea is the shared asset store the UI handler asks for a hashed file its build lacks.
type uiAssetsArea struct{ store *shared.Store }

// wireUIAssets builds the store over the database and the blob store and publishes the build (Q-HA-123): the replica is not
// ready while it waits for the publish, so a page that gets its index.html from it finds every file the index names on the
// replicas of the other release too. A store that cannot take the build within shared.PublishWait does not keep the
// replica from serving: it starts, says so, and the keeper tries again every minute.
func (a *App) wireUIAssets(ctx context.Context, in infra) (uiAssetsArea, error) {
	if err := requireInputs(AreaUIAssets, given("database", in.db), given("blob store", in.blobs)); err != nil {
		return uiAssetsArea{}, err
	}
	tree, err := webui.Tree(a.o.UIAssets)
	if err != nil {
		return uiAssetsArea{}, fmt.Errorf("%s: the embedded UI: %w", AreaUIAssets, err)
	}
	files, err := webui.HashedFiles(tree, uiLocales())
	if err != nil {
		return uiAssetsArea{}, fmt.Errorf("%s: %w", AreaUIAssets, err)
	}
	log := a.log.With("component", "ui-assets")
	bi := buildinfo.Get()
	store := shared.New(in.db, in.blobs, bi.Version+" "+bi.Commit, log)
	keeper := shared.NewKeeper(store, tree, files, log)
	if err := keeper.PublishAtStart(ctx, shared.PublishWait); err != nil {
		if ctx.Err() != nil {
			return uiAssetsArea{}, ctx.Err()
		}
		log.Error("the UI's hashed files are not in the shared asset store: the replica serves without them, and a page of "+
			"another release that asks it for one of them fails until the keeper has published them", "error", err, "files", len(files))
	}
	if err := a.startComponent(ctx, keeper); err != nil {
		return uiAssetsArea{}, err
	}
	return uiAssetsArea{store: store}, nil
}

// uiShared is the shared asset store of the UI handler: nil on a replica without the area (the role packages builds no
// UI, the shell profile has no infrastructure), so that the handler answers a missing hashed file 404 there.
func (a *App) uiShared() webui.Shared {
	s := part[uiAssetsArea](a.parts).store
	if s == nil {
		return nil // not the nil *shared.Store, which as a webui.Shared would not be nil
	}
	return s
}
