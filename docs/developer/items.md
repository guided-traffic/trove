# The item store

The format-neutral half of the files transfer: [internal/items](../../internal/items/) stores, reads and removes the items of a
repository on the blob store and the `items` tables. It makes no decision and imports no authorization. Three kinds of caller
use it: the generic layer ([files.md](files.md)) for `/files/` and the items operations, the pull-through proxy for the artifacts of
a remote fill (`Origin: fill`), and the format host for a format's publish (`Host.Ingest()` and `Host.Items()`,
[R-ARCH-009](../spec/architecture.md)). Requirements: [R-STOR-005, R-STOR-007, R-STOR-012, R-STOR-013, R-STOR-015 to
R-STOR-018](../spec/storage.md), [R-ARCH-009, R-ARCH-014, R-ARCH-019](../spec/architecture.md). Decisions:
[Q-ARCH-206, Q-ARCH-207](../spec/architecture.md#decisions), [Q-STOR-143 to Q-STOR-146](../spec/storage.md#decisions).

## What has to be in your head first

- **The store never asks who you are.** `items` does not import `internal/authz` (the architecture test `moduleBans` fails the build of
  that edge). Whoever calls has decided, and hands the store the checks that must run inside its transactions as callbacks
  (`CommitSpec.Extra`, `DeleteSpec.Check`, `DeleteSpec.Extra`).
- **One commit, one order.** `Store.Commit` runs, in one PostgreSQL transaction that also holds the blob row's lock: the ancestor
  folders and the item row, then `CommitSpec.Extra`, then `Hooks.OnCommit`, then the audit row. Any step that fails rolls the
  others back, so no item is left without the committer's rows and no row without its item.
- **A fill is not an upload.** `OriginFill` stores `upstream_url`, tells the hook it is a fill, and writes no `artifact.deployed`
  (Q-STOR-146). `OriginUpload` writes it.
- **The overwrite question is settled outside.** `CommitSpec.MayOverwrite` is decided by the committer before the call, never by
  a decision inside the transaction (Q-STOR-144, R-ARCH-019). A fill sets it.
- **State between requests is the write-only statistics buffer.** Everything else asks PostgreSQL ([R-ARCH-032](../spec/architecture.md)).

## Files

| File | Responsibility |
|---|---|
| [items.go](../../internal/items/items.go) | `Store`, `Options`, `New`, `RepoRef` / `RepoOf`, the path helpers |
| [commit.go](../../internal/items/commit.go) | `Spool`, `CommitSpec`, `Commit`, the link protocol (`linkFile`, `ensureParents`, `lockAncestors`), `CreateFolder`, `ErrOverwriteDenied`, `PathConflict`, `CreateOnlyFailed` |
| [hooks.go](../../internal/items/hooks.go) | `Origin`, `Ref`, `CommitEvent`, `RemovedEvent` (each removed item names the package version it belonged to), `Hooks`, `NoHooks`; `internal/formathost` implements `Hooks` over the format registry ([formats.md](formats.md)) |
| [read.go](../../internal/items/read.go) | `Live` (PostgreSQL only), `ContentError`, `Open` (the verifying reader or a range), `DownloadStats` |
| [children.go](../../internal/items/children.go) | `Children`: keyset batches, the cursor, the scan limit; visibility is a callback |
| [delete.go](../../internal/items/delete.go) | `Delete` (at once or by the job), `RunDeleteJob`, `DeleteJobKind` / `DeleteJobSpec`, `DeleteArgs` |
| [stats.go](../../internal/items/stats.go) | `Stats` (the in-memory download counter and its flush), `Served` |
| [item.go](../../internal/items/item.go) | `Item`, `Root`, `ETag`, the kinds |

The statements are in [queries/files.sql](../../internal/meta/postgres/queries/files.sql); the package writes no SQL.

## The API

```go
// Build the store once per replica (internal/app/wire_files.go).
func New(o Options) (*Store, error)
type Options struct {
	Runner meta.Runner; Blobs Blobs; Recorder audit.Recorder; Jobs JobEnqueuer
	Hooks Hooks            // the formats' storage hooks; nil runs none
	Storage config.Storage; RecordDownloads bool
	Activity ActivitySink; Stats *Stats; Log *slog.Logger
}

// Spool reads a body into the replica-local spool and hashes it in one pass. The caller closes the result.
func (s *Store) Spool(ctx context.Context, body io.Reader, o IngestOptions) (*Spooled, error)

// Commit stores a spooled body as the file at a path. Not decided here: the committer decided before.
func (s *Store) Commit(ctx context.Context, spec CommitSpec) (CommitResult, error)
type CommitSpec struct {
	Repo      repository.Repository
	Path      repository.Cleaned   // a file path; the root and a folder are refused
	Spooled   *Spooled
	MediaType string
	Declared  Expected             // digests the committer announced, stored as the declared digests
	Origin    Origin               // OriginUpload | OriginFill
	UpstreamURL string             // OriginFill; user info is removed
	CreateOnly, MayOverwrite bool
	Actor audit.Actor; Source audit.Source; Endpoint string
	Payload any                    // handed to Hooks.OnCommit unchanged
	Extra func(ctx context.Context, tx meta.Tx, it Ref) error // in the transaction, after the row, before the hook
	BeginCommit func()             // the server may now finish the request past the shutdown deadline
}
type CommitResult struct { Created, Overwritten bool; Item Item } // Item is read in the commit transaction, so a delete or overwrite right after it cannot empty it

// The rest of the store.
func (s *Store) CreateFolder(ctx context.Context, spec FolderSpec) (FolderResult, error)
func (s *Store) Live(ctx context.Context, repo RepoRef, path string) (Item, bool, error)
func (s *Store) Open(ctx context.Context, it Item, rng *Range) (io.ReadCloser, error)
func (s *Store) Children(ctx context.Context, repo RepoRef, cq ChildrenQuery, visible Visibility) (ChildrenPage, error)
func (s *Store) Delete(ctx context.Context, spec DeleteSpec) (DeleteResult, error)
func (s *Store) RunDeleteJob(ctx context.Context, run *jobs.Run) error
func (s *Store) RemoveIn(ctx context.Context, tx meta.Tx, spec RemoveSpec) error
func (s *Store) DownloadStats(ctx context.Context, itemID int64) (DownloadStats, error)
func (s *Store) Served(ctx context.Context, e ServedEvent)

// Hooks are the formats' storage hooks, injected by the composition root. They run in the transaction of the change.
type Hooks interface {
	OnCommit(ctx context.Context, tx meta.Tx, e CommitEvent) error
	OnItemsRemoved(ctx context.Context, tx meta.Tx, e RemovedEvent) error
}
```

### The three committers

| Committer | `Origin` | `Extra` | `MayOverwrite` | Audit row |
|---|---|---|---|---|
| The generic layer, a `PUT` on `/files/` ([files/deploy.go](../../internal/files/deploy.go)) | `OriginUpload` | `Access.RecordAllowed`: the audit row of the allowed write (R-AUTHZ-029) | decided by `PathsAllowed` for `overwrite` | `artifact.deployed` |
| A format's publish, through `Host.Ingest().Commit` (the format host, with the format contract) | `OriginUpload` | the writer's package rows | decided by the host from the request's index | `artifact.deployed` |
| The proxy's artifact fill ([R-REPO-025](../spec/repositories-and-proxy.md)) | `OriginFill` | the cache-entry upsert, which can name the new item's id | `true` | none; the proxy raises its own event |

A proxy fill, in code:

```go
sp, err := store.Spool(ctx, upstreamBody, items.IngestOptions{ContentLength: -1, Expect: expected})
if err != nil { return err }
defer sp.Close()
_, err = store.Commit(ctx, items.CommitSpec{
	Repo: repo, Path: cleaned, Spooled: sp, MediaType: mt,
	Origin: items.OriginFill, UpstreamURL: finalURL, MayOverwrite: true,
	Extra: func(ctx context.Context, tx meta.Tx, it items.Ref) error {
		return upsertCacheEntry(ctx, tx, it.ID, entry) // rolled back with the item
	},
})
```

### What a failing step leaves

| Failing step | Item row | Ancestor folders | `Extra` rows | Hook rows | Audit row |
|---|---|---|---|---|---|
| the link step (path conflict, create-only, overwrite denied) | none | none | none | none | none |
| `Extra` | rolled back | rolled back | rolled back | not run | not written |
| `Hooks.OnCommit` | rolled back | rolled back | rolled back | rolled back | not written |
| the audit insert | rolled back | rolled back | rolled back | rolled back | not written |

The blob is already in the object store when the transaction starts; a rolled-back commit leaves it unreferenced and the garbage
collection finds it ([blob-store.md](blob-store.md)). An identical commit (same content at the path) changes no row: `Extra` runs, the
hook and the audit row do not.

### Removal

`Store.Delete` counts the subtree. At most `storage.delete.max_sync_items` items are checked (`DeleteSpec.Check`, in batches of 1,000
paths, on the rows that are about to be removed), `DeleteSpec.Extra` runs with the paths of the files among those rows (the generic
layer records the allowed write and asks the format's `CheckRemoval`, the ownership of their packages, Q-AUTHZ-161), the files go by id,
`Hooks.OnItemsRemoved` is told per statement of at most 5,000 files, one `artifact.deleted` per file is written, then the folders
deepest first. A larger subtree is checked in full before the job `items-delete` is queued (the highest id the check saw is
`DeleteArgs.MaxID`, Q-STOR-143), `Extra` runs in the transaction that queues it with the files the check saw, and the job
removes files in batches of `storage.delete.batch_size`, each batch in its own transaction with its own hook call, then the folders.
The job decides nothing. A hook that fails stops the batch, rolls it back, and the job is retried.

`Store.RemoveIn` is the same removal inside a transaction of the caller, for a format's route that removes the files of a version together with its own rows
(`PackageWriter.DeleteItems`, [formats.md](formats.md), [npm.md](npm.md)): it lists the files of the given paths (a folder path takes its subtree), removes them by id, tells
`Hooks.OnItemsRemoved` in the caller's transaction, writes the `artifact.deleted` events and removes the implicit folders that stay empty. It checks nothing and queues no job: the caller
has decided every path and bounds their number.

## Tests

| Tier | Where | Proves |
|---|---|---|
| unit | [items_test.go](../../internal/items/items_test.go), [buffers_test.go](../../internal/items/buffers_test.go) | path helpers and subtree bounds, `RepoOf`, the origin names, no credentials in the upstream URL, the statistics buffer keeps what it could not write |
| architecture | [archtest_test.go](../../internal/archtest/archtest_test.go) `TestModuleBans` | `items` imports no `authz`; `files` imports no `blob` and no `meta/postgres`; `rawfiles` and `edge` import no `api/v1` |
| integration | [commit_test.go](../../test/integration/files/commit_test.go) | `TestR_ARCH_009_ItemStoreRunsTheStorageHooksInItsTransaction` (both origins, the order `Extra` → hook → audit, `Extra` sees the new item id, rollback leaves no item, no folder, no `Extra` row, no event; identical commit; overwrite gate; create-only), `TestR_ARCH_008_DeleteRunsOnItemsRemoved` (the hook is told in the synchronous delete and in every batch of the job; a failing hook leaves the files) |

## Extending

1. A new committer: build a `CommitSpec` with its own `Origin` only if a hook must tell it apart (add the constant in
   [hooks.go](../../internal/items/hooks.go) and its name in `String`); decide before the call; put its own writes in `Extra`.
2. A new step between the hook and the audit row: only in `linkFile` ([commit.go](../../internal/items/commit.go)), with a test in
   [commit_test.go](../../test/integration/files/commit_test.go) that asserts the order.
3. A new statement: add it to `queries/files.sql` and run `make generate`; the package writes no SQL.
