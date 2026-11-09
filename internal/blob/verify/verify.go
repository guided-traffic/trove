// Package verify checks that every stored blob still has its object in the store: the logic of `trove admin storage verify`
// (R-HA-024, R-HA-025, R-STOR-027). It reads the blob table in pages of a fixed size, so its memory does not grow with the
// table, asks the store for each object with one existence check, and reports each blob whose object is missing or of
// another size together with the items that reference it. It changes nothing: the status marks of the scrub (R-STOR-027)
// stay as they are.
package verify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
)

// Defaults of [Options].
const (
	DefaultPageSize    = 1000
	DefaultConcurrency = 16
	DefaultMaxItems    = 20
	headAttempts       = 3
)

// Row is a live blob row.
type Row struct {
	Digest blob.Digest
	Size   int64
}

// Item names one item that references a blob.
type Item struct {
	Project    string `json:"project"`
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Trashed    bool   `json:"trashed,omitempty"`
}

// Source is the database side of a run.
type Source interface {
	// Page returns up to limit live blob rows with a digest after the given one (all of them when first is set), in digest
	// order, restricted to the rows created or changed at or after since when since is not the zero time.
	Page(ctx context.Context, after blob.Digest, first bool, since time.Time, limit int) ([]Row, error)
	// Items returns up to limit items that reference the blob, and the number of all of them.
	Items(ctx context.Context, d blob.Digest, limit int) ([]Item, int64, error)
}

// Objects is the store side of a run: the existence check.
type Objects interface {
	Head(ctx context.Context, key string) (blob.ObjectInfo, error)
}

// Options configure a run.
type Options struct {
	DB      Source
	Objects Objects
	// Prefix is storage.s3.prefix.
	Prefix string
	// Since limits the run to blob rows created or changed at or after it; the zero time checks every blob.
	Since time.Time
	// PageSize is the number of rows read per query; Concurrency the number of simultaneous existence checks; MaxItems the
	// number of items listed per damaged blob.
	PageSize, Concurrency, MaxItems int
	Log                             *slog.Logger
	// Sleep waits between the attempts of a check that failed for a reason other than a missing object; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Finding kinds.
const (
	// KindMissing is a blob row whose object the store does not have.
	KindMissing = "missing"
	// KindSizeMismatch is an object whose size differs from the row's.
	KindSizeMismatch = "size_mismatch"
)

// Finding is one damaged blob with the items that reference it.
type Finding struct {
	Kind       string `json:"kind"`
	SHA256     string `json:"sha256"`
	Key        string `json:"key"`
	Size       int64  `json:"size"`
	ObjectSize int64  `json:"objectSize,omitempty"`
	// Items lists up to Options.MaxItems referencing items; ItemCount is the number of all of them.
	Items     []Item `json:"items"`
	ItemCount int64  `json:"itemCount"`
}

// Unchecked is a blob whose object the store could not be asked about (a timeout, a 5xx, throttling) after the attempts.
type Unchecked struct {
	SHA256 string `json:"sha256"`
	Error  string `json:"error"`
}

// Report is the result of a run.
type Report struct {
	Since time.Time `json:"since,omitzero"`
	// Checked is the number of blobs whose object was asked for.
	Checked int64 `json:"checked"`
	// Missing counts the blobs whose object is missing (the number R-HA-025 step 4 prints); SizeMismatch those of another size.
	Missing      int64       `json:"missing"`
	SizeMismatch int64       `json:"sizeMismatch"`
	Findings     []Finding   `json:"findings"`
	Unchecked    []Unchecked `json:"unchecked"`
	// ItemsAffected is the number of items that reference a damaged blob.
	ItemsAffected int64 `json:"itemsAffected"`
}

// OK reports whether every checked blob has its object and the store answered for each.
func (r *Report) OK() bool { return len(r.Findings) == 0 && len(r.Unchecked) == 0 }

// Run walks the blob table and checks each object. It returns the report so far with the error when the run ends early; a
// permission or configuration error of the store (for example AccessDenied, which a store answers for a missing object when
// the credential may not list the bucket) ends it at once, because every further check would fail the same way.
func Run(ctx context.Context, o Options) (*Report, error) {
	if o.DB == nil || o.Objects == nil {
		return nil, errors.New("verify: a database source and an object store are required")
	}
	if o.PageSize <= 0 {
		o.PageSize = DefaultPageSize
	}
	if o.Concurrency <= 0 {
		o.Concurrency = DefaultConcurrency
	}
	if o.MaxItems <= 0 {
		o.MaxItems = DefaultMaxItems
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Sleep == nil {
		o.Sleep = sleep
	}
	rep := &Report{Since: o.Since, Findings: []Finding{}, Unchecked: []Unchecked{}}
	var after blob.Digest
	first := true
	for {
		rows, err := o.DB.Page(ctx, after, first, o.Since, o.PageSize)
		if err != nil {
			return rep, fmt.Errorf("reading the blob table: %w", err)
		}
		if len(rows) == 0 {
			return rep, nil
		}
		first = false
		after = rows[len(rows)-1].Digest
		results := make([]result, len(rows))
		if err := checkPage(ctx, o, rows, results); err != nil {
			return rep, err
		}
		for i, r := range results {
			rep.Checked++
			switch {
			case r.err != nil:
				rep.Unchecked = append(rep.Unchecked, Unchecked{SHA256: rows[i].Digest.Hex(), Error: r.err.Error()})
			case r.kind != "":
				f, err := finding(ctx, o, rows[i], r)
				if err != nil {
					return rep, fmt.Errorf("listing the items of blob %s: %w", rows[i].Digest.Hex(), err)
				}
				if f.Kind == KindMissing {
					rep.Missing++
				} else {
					rep.SizeMismatch++
				}
				rep.ItemsAffected += f.ItemCount
				rep.Findings = append(rep.Findings, f)
			}
		}
		o.Log.Debug("storage verify progress", "checked", rep.Checked, "missing", rep.Missing)
	}
}

type result struct {
	kind       string
	objectSize int64
	err        error
}

// checkPage asks the store about every row of a page with at most o.Concurrency checks at a time. A store error that retrying
// does not fix ends the page.
func checkPage(ctx context.Context, o Options, rows []Row, out []result) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		fatal error
		sem   = make(chan struct{}, o.Concurrency)
	)
	for i := range rows {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			r, err := check(ctx, o, rows[i])
			if err != nil {
				mu.Lock()
				if fatal == nil {
					fatal = err
					cancel()
				}
				mu.Unlock()
				return
			}
			out[i] = r
		}(i)
	}
	wg.Wait()
	if fatal != nil {
		return fatal
	}
	return ctx.Err()
}

// check is one existence check with its attempts. A missing object and a size mismatch are results; a transient error that
// outlasts the attempts is a result too (the blob is unchecked); a configuration error is returned.
func check(ctx context.Context, o Options, row Row) (result, error) {
	key := blob.Key(o.Prefix, row.Digest)
	var last error
	for attempt := range headAttempts {
		if attempt > 0 {
			if err := o.Sleep(ctx, time.Duration(attempt)*time.Second); err != nil {
				return result{}, err
			}
		}
		info, err := o.Objects.Head(ctx, key)
		switch {
		case errors.Is(err, blob.ErrNotFound):
			return result{kind: KindMissing}, nil
		case err == nil && info.Size >= 0 && info.Size != row.Size:
			return result{kind: KindSizeMismatch, objectSize: info.Size}, nil
		case err == nil:
			return result{}, nil
		case blob.IsConfig(err):
			return result{}, fmt.Errorf("the object store refuses the check of %s: %w", key, err)
		case ctx.Err() != nil:
			return result{}, ctx.Err()
		}
		last = err
	}
	return result{err: last}, nil
}

func finding(ctx context.Context, o Options, row Row, r result) (Finding, error) {
	items, n, err := o.DB.Items(ctx, row.Digest, o.MaxItems)
	if err != nil {
		return Finding{}, err
	}
	if items == nil {
		items = []Item{}
	}
	return Finding{
		Kind: r.kind, SHA256: row.Digest.Hex(), Key: blob.Key(o.Prefix, row.Digest), Size: row.Size, ObjectSize: r.objectSize,
		Items: items, ItemCount: n,
	}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
