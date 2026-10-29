//go:build integration

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// The widget is the test resource that proves the conventions end to end, and
// the worked example behind "How to add a management resource" in
// docs/developer/http-api.md: a keyed collection with list, get, create, update
// (merge patch and PUT, both with If-Match) and delete. Wave 2 resources follow
// its shape: the handlers are thin, the helpers of internal/api/v1 do the
// conventions, and the SQL of a real resource lives in its sqlc queries (this
// test resource runs plain statements because its table exists only in the
// test database).

const widgetSchema = `
CREATE TABLE trove.widgets (
    id         uuid        PRIMARY KEY,
    name       text        NOT NULL UNIQUE,
    color      text,
    labels     jsonb       NOT NULL DEFAULT '{}',
    revision   bigint      NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX widgets_created ON trove.widgets (created_at, id);
GRANT ALL ON trove.widgets TO PUBLIC;`

// Widget is the representation: the same type serves GET, POST, PUT and the base
// of a PATCH, so a body obtained by GET can be sent back (R-API-003).
type Widget struct {
	Name      string            `json:"name"`
	Color     *string           `json:"color"`
	Labels    map[string]string `json:"labels"`
	Revision  int64             `json:"revision"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// reservedWidgetNames are the literal siblings of {name} (R-API-004).
func reservedWidgetNames() []string { return []string{"batch-create"} }

func widgetName() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`) }

type widgetAPI struct {
	db   *postgres.DB
	idem *v1.Idempotency
	cfg  config.API
	// hold, when set, blocks a create whose name starts with "hold-" inside the
	// transaction, after the idempotency row is claimed (the in-progress test).
	hold chan struct{}
	// entered receives once per held create.
	entered chan struct{}
	// failNext makes the next create fail with an internal error after it claimed
	// the idempotency key (the rollback test).
	failNext bool
}

func newWidgetAPI(db *postgres.DB, idem *v1.Idempotency, cfg config.API) *widgetAPI {
	return &widgetAPI{db: db, idem: idem, cfg: cfg}
}

func (a *widgetAPI) register(r *httpserver.Router) {
	r.HandleFunc("GET /api/v1/widgets", a.list)
	r.HandleFunc("POST /api/v1/widgets", a.create)
	r.HandleFunc("GET /api/v1/widgets/{name}", a.get)
	r.HandleFunc("PATCH /api/v1/widgets/{name}", a.patch)
	r.HandleFunc("PUT /api/v1/widgets/{name}", a.put)
	r.HandleFunc("DELETE /api/v1/widgets/{name}", a.delete)
}

const widgetColumns = "name, color, labels, revision, created_at, updated_at"

func scanWidget(row pgx.Row) (Widget, error) {
	var w Widget
	var labels []byte
	if err := row.Scan(&w.Name, &w.Color, &labels, &w.Revision, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return Widget{}, err
	}
	w.CreatedAt, w.UpdatedAt = w.CreatedAt.UTC(), w.UpdatedAt.UTC()
	if err := json.Unmarshal(labels, &w.Labels); err != nil {
		return Widget{}, err
	}
	if w.Labels == nil {
		w.Labels = map[string]string{}
	}
	return w, nil
}

func (a *widgetAPI) read(ctx context.Context, name string) (w Widget, found bool, err error) {
	err = a.db.InPgxTx(ctx, func(tx pgx.Tx) error {
		var e error
		w, e = scanWidget(tx.QueryRow(ctx, "SELECT "+widgetColumns+" FROM widgets WHERE name = $1", name))
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		found = e == nil
		return e
	})
	return w, found, err
}

// list is the cursor-paginated collection (R-API-010, R-API-011): one keyset
// query per page, the cursor carries the sort key of the last item.
func (a *widgetAPI) list(w http.ResponseWriter, r *http.Request) {
	lq, err := v1.ParseList(r, v1.ListSpec{
		Filters: []string{"color", "namePrefix"}, Sorts: []string{"name", "createdAt"}, DefaultSort: "name",
	}, a.cfg)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	col := "name"
	if lq.Sort == "createdAt" {
		col = "created_at"
	}
	dir, cmp := "ASC", ">"
	if lq.Desc {
		dir, cmp = "DESC", "<"
	}
	var args []any
	where := []string{"true"}
	if v := lq.Filter["color"]; len(v) > 0 {
		args = append(args, v)
		where = append(where, fmt.Sprintf("color = ANY($%d)", len(args)))
	}
	if v := lq.First("namePrefix"); v != "" {
		args = append(args, v+"%")
		where = append(where, fmt.Sprintf("name LIKE $%d", len(args)))
	}
	if len(lq.After) == 2 {
		args = append(args, lq.After[0], lq.After[1])
		where = append(where, fmt.Sprintf("(%s, id) %s ($%d::%s, $%d::uuid)", col, cmp, len(args)-1, map[string]string{"name": "text", "created_at": "timestamptz"}[col], len(args)))
	}
	args = append(args, lq.FetchLimit())
	sql := fmt.Sprintf("SELECT %s, id::text FROM widgets WHERE %s ORDER BY %s %s, id %s LIMIT $%d",
		widgetColumns, strings.Join(where, " AND "), col, dir, dir, len(args))
	type row struct {
		w  Widget
		id string
	}
	var rows []row
	err = a.db.InPgxTx(r.Context(), func(tx pgx.Tx) error {
		rows = rows[:0]
		rs, err := tx.Query(r.Context(), sql, args...)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var w Widget
			var labels []byte
			var id string
			if err := rs.Scan(&w.Name, &w.Color, &labels, &w.Revision, &w.CreatedAt, &w.UpdatedAt, &id); err != nil {
				return err
			}
			w.CreatedAt, w.UpdatedAt = w.CreatedAt.UTC(), w.UpdatedAt.UTC()
			_ = json.Unmarshal(labels, &w.Labels)
			if w.Labels == nil {
				w.Labels = map[string]string{}
			}
			rows = append(rows, row{w, id})
		}
		return rs.Err()
	})
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "widgets.list"))
		return
	}
	page := v1.NewPage(lq, rows, func(x row) []string {
		if col == "name" {
			return []string{x.w.Name, x.id}
		}
		return []string{x.w.CreatedAt.Format(time.RFC3339Nano), x.id}
	})
	v1.WritePage(w, r, v1.MapPage(page, func(x row) Widget { return x.w }))
}

func (a *widgetAPI) get(w http.ResponseWriter, r *http.Request) {
	wd, found, err := a.read(r.Context(), r.PathValue("name"))
	switch {
	case err != nil:
		httpserver.Problem(w, r, errs.Wrap(err, "widgets.get"))
	case !found:
		httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "No widget has this name."))
	default:
		tag := v1.ETag(wd.Revision, "")
		if v1.NotModified(w, r, tag) {
			return
		}
		w.Header().Set("ETag", tag)
		writeJSON(w, http.StatusOK, wd)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// create decodes strictly, validates, and creates inside the idempotent
// operation: the row and the stored outcome commit together (R-API-014).
func (a *widgetAPI) create(w http.ResponseWriter, r *http.Request) {
	body, err := v1.ReadBody(w, r, v1.MediaJSON, int64(a.cfg.MaxRequestBodyBytes))
	var in Widget
	if err == nil {
		err = v1.StrictUnmarshal(body, &in)
	}
	if err == nil {
		err = validateWidget(in)
	}
	if err == nil {
		err = v1.CheckReservedName("/name", in.Name, reservedWidgetNames()...)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	err = a.idem.Do(w, r, body, v1.IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (v1.Outcome, error) {
		ptx, _ := postgres.Unwrap(tx)
		if strings.HasPrefix(in.Name, "hold-") && a.hold != nil {
			a.entered <- struct{}{}
			<-a.hold
		}
		if a.failNext {
			a.failNext = false
			return v1.Outcome{}, errors.New("injected failure")
		}
		labels, _ := json.Marshal(in.Labels)
		wd, err := scanWidget(ptx.QueryRow(ctx, `INSERT INTO widgets (id, name, color, labels) VALUES (gen_random_uuid(), $1, $2, $3)
			ON CONFLICT (name) DO NOTHING RETURNING `+widgetColumns, in.Name, in.Color, labels))
		if errors.Is(err, pgx.ErrNoRows) {
			return v1.Outcome{}, errs.E(errs.Conflict, errs.CodeAlreadyExists, "A widget with this name exists already.")
		}
		if err != nil {
			return v1.Outcome{}, err
		}
		return v1.Outcome{
			Status: http.StatusCreated, Body: wd,
			Header: map[string]string{"Location": httpserver.InfoFrom(ctx).BaseURL + "/api/v1/widgets/" + wd.Name, "ETag": v1.ETag(wd.Revision, "")},
		}, nil
	}})
	if err != nil {
		httpserver.Problem(w, r, err)
	}
}

func validateWidget(in Widget) error {
	if !widgetName().MatchString(in.Name) {
		return errs.E(errs.Invalid, errs.CodeValidation, "1 field is invalid.", errs.WithFields(
			errs.FieldError{Pointer: "/name", Code: "pattern", Detail: "must match " + widgetName().String()}))
	}
	return nil
}

// patch is the merge patch of R-API-013 with If-Match (R-API-012): 428 without
// the header, the patch applied to the current representation, immutable members
// checked, and one UPDATE that carries the revision condition.
func (a *widgetAPI) patch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	pre, err := v1.ParseIfMatch(r)
	var raw []byte
	if err == nil {
		err = pre.Require()
	}
	if err == nil {
		raw, err = v1.ReadMergePatch(w, r, int64(a.cfg.MaxRequestBodyBytes))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	a.update(w, r, name, pre, func(cur Widget) (Widget, error) {
		next, err := v1.ApplyMergePatch(cur, raw)
		if err != nil {
			return Widget{}, err
		}
		return next, v1.CheckImmutable(cur, next, "name")
	})
}

func (a *widgetAPI) put(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	pre, err := v1.ParseIfMatch(r)
	var in Widget
	if err == nil {
		err = pre.Require()
	}
	if err == nil {
		err = v1.DecodeJSON(w, r, &in, int64(a.cfg.MaxRequestBodyBytes))
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	a.update(w, r, name, pre, func(cur Widget) (Widget, error) {
		// Read-only members (revision, createdAt, updatedAt) of the body are ignored.
		in.Revision, in.CreatedAt, in.UpdatedAt = cur.Revision, cur.CreatedAt, cur.UpdatedAt
		return in, v1.CheckImmutable(cur, in, "name")
	})
}

// update is the shared write: load, compute the new state, UPDATE ... WHERE
// revision, and tell a stale tag (412) from a missing resource (404).
func (a *widgetAPI) update(w http.ResponseWriter, r *http.Request, name string, pre v1.Precondition, next func(Widget) (Widget, error)) {
	ctx := r.Context()
	cur, found, err := a.read(ctx, name)
	if err == nil && !found {
		err = errs.E(errs.NotFound, errs.CodeNotFound, "No widget has this name.")
	}
	if err == nil && !pre.Matches(cur.Revision, "") {
		err = v1.Failed(cur.Revision, "")
	}
	var nw Widget
	if err == nil {
		nw, err = next(cur)
	}
	if err == nil {
		err = validateWidget(nw)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	labels, _ := json.Marshal(nw.Labels)
	rev, byRevision := pre.Revision("")
	var out Widget
	var updated bool
	err = a.db.InPgxTx(ctx, func(tx pgx.Tx) error {
		var arg any // NULL for If-Match: *, the revision otherwise
		if byRevision {
			arg = rev
		}
		o, e := scanWidget(tx.QueryRow(ctx, `UPDATE widgets SET color = $2, labels = $3, revision = revision + 1, updated_at = now()
			WHERE name = $1 AND ($4::bigint IS NULL OR revision = $4) RETURNING `+widgetColumns, name, nw.Color, labels, arg))
		if errors.Is(e, pgx.ErrNoRows) {
			updated = false
			return nil
		}
		out, updated = o, e == nil
		return e
	})
	if err == nil && !updated {
		// The row changed between the read and the UPDATE: report the revision it has now.
		latest, ok, rerr := a.read(ctx, name)
		switch {
		case rerr != nil:
			err = rerr
		case !ok:
			err = errs.E(errs.NotFound, errs.CodeNotFound, "No widget has this name.")
		default:
			err = v1.Failed(latest.Revision, "")
		}
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "widgets.update"))
		return
	}
	w.Header().Set("ETag", v1.ETag(out.Revision, ""))
	writeJSON(w, http.StatusOK, out)
}

func (a *widgetAPI) delete(w http.ResponseWriter, r *http.Request) {
	pre, err := v1.ParseIfMatch(r)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	rev, byRevision := pre.Revision("")
	var arg any
	if byRevision {
		arg = rev
	}
	var n int64
	err = a.db.InPgxTx(r.Context(), func(tx pgx.Tx) error {
		tag, e := tx.Exec(r.Context(), "DELETE FROM widgets WHERE name = $1 AND ($2::bigint IS NULL OR revision = $2)", r.PathValue("name"), arg)
		n = tag.RowsAffected()
		return e
	})
	if err == nil && n == 0 {
		cur, found, rerr := a.read(r.Context(), r.PathValue("name"))
		switch {
		case rerr != nil:
			err = rerr
		case !found:
			err = errs.E(errs.NotFound, errs.CodeNotFound, "No widget has this name.")
		default:
			err = v1.Failed(cur.Revision, "")
		}
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "widgets.delete"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
