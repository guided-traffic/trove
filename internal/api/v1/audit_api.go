package v1

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
)

// The audit operations of R-OPS-017: query, read one event and export. The
// store reads PostgreSQL in short keyset pages and holds no transaction between
// them; the permission is audit:read, declared in the document and enforced by
// the authorization step before any handler here runs.

// auditFilters are the filter parameters shared by the query and the export.
func auditFilters() []string {
	return []string{"action", "category", "outcome", "actor", "repository", "resourceType", "resourceId", "traceId"}
}

// exportBatch is the number of events one export query reads.
const exportBatch = 1000

func (s *Server) auditStore() (*audit.Store, error) {
	if s.o.AuditStore == nil {
		return nil, unavailable("The audit trail")
	}
	return s.o.AuditStore, nil
}

type auditActorView struct {
	Type       string  `json:"type"`
	ID         *string `json:"id"`
	Name       *string `json:"name"`
	TokenID    *string `json:"tokenId"`
	AuthMethod *string `json:"authMethod"`
}

type auditSourceView struct {
	IP        *string `json:"ip"`
	UserAgent *string `json:"userAgent"`
}

type auditResourceView struct {
	Type *string `json:"type"`
	ID   *string `json:"id"`
}

// auditEventView is the representation of R-OPS-017: every member is present,
// "no value" is null.
type auditEventView struct {
	ID         string            `json:"id"`
	OccurredAt time.Time         `json:"occurredAt"`
	Action     string            `json:"action"`
	Category   string            `json:"category"`
	Outcome    string            `json:"outcome"`
	Actor      auditActorView    `json:"actor"`
	Source     auditSourceView   `json:"source"`
	TraceID    *string           `json:"traceId"`
	Replica    *string           `json:"replica"`
	Endpoint   *string           `json:"endpoint"`
	Resource   auditResourceView `json:"resource"`
	Repository *string           `json:"repository"`
	Path       *string           `json:"path"`
	Reason     *string           `json:"reason"`
	Details    json.RawMessage   `json:"details"`
}

func nz(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toAuditEvent(e audit.Entry) auditEventView {
	details := e.Details
	if len(bytes.TrimSpace(details)) == 0 {
		details = json.RawMessage(`{}`)
	}
	return auditEventView{
		ID: e.ID, OccurredAt: e.OccurredAt.UTC(), Action: e.Action, Category: e.Category, Outcome: string(e.Outcome),
		Actor:    auditActorView{Type: e.Actor.Kind, ID: nz(e.Actor.ID), Name: nz(e.Actor.Name), TokenID: nz(e.Actor.TokenID), AuthMethod: nz(e.Actor.AuthMethod)},
		Source:   auditSourceView{IP: nz(e.Source.ClientAddr), UserAgent: nz(e.Source.UserAgent)},
		TraceID:  nz(e.Source.TraceID),
		Replica:  nz(e.Replica),
		Endpoint: nz(e.Endpoint),
		Resource: auditResourceView{Type: nz(e.Resource.Kind), ID: nz(e.Resource.ID)},
		// Repository, Path and Reason are columns of their own.
		Repository: nz(e.Repository), Path: nz(e.Path), Reason: nz(e.Reason),
		Details: details,
	}
}

// onceEach returns the single value of every named query parameter, or 400 when a
// parameter that takes one value is repeated.
func onceEach(vals url.Values, names ...string) (map[string]string, error) {
	out := map[string]string{}
	for _, n := range names {
		v := vals[n]
		if len(v) > 1 {
			return nil, errs.E(errs.Invalid, errs.CodeInvalid, fmt.Sprintf("The query parameter %q is given once.", n))
		}
		if len(v) == 1 {
			out[n] = v[0]
		}
	}
	return out, nil
}

// auditFilter builds the store filter from the parsed query parameters and
// validates the closed sets.
func auditFilter(p map[string]string) (audit.Filter, error) {
	f := audit.Filter{
		Action: p["action"], Category: p["category"], Outcome: audit.Outcome(p["outcome"]), Actor: p["actor"],
		Repository: p["repository"], ResourceType: p["resourceType"], ResourceID: p["resourceId"], TraceID: p["traceId"],
	}
	if f.Category != "" {
		known := false
		for _, c := range audit.Categories() {
			known = known || string(c) == f.Category
		}
		if !known {
			return f, errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter \"category\" has an unknown value: "+f.Category+".")
		}
	}
	switch f.Outcome {
	case "", audit.Success, audit.Failure, audit.Denied:
	default:
		return f, errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter \"outcome\" must be success, failure or denied.")
	}
	for _, t := range []struct {
		name string
		dst  *time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		v, ok := p[t.name]
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return f, errs.E(errs.Invalid, errs.CodeInvalidAuditRange, "The query parameter \""+t.name+"\" must be an RFC 3339 time.")
		}
		*t.dst = at.UTC()
	}
	return f, nil
}

// ListAuditEvents implements GET /api/v1/audit/events (R-OPS-017): newest
// first over (occurredAt, id). The page size follows the audit limits, not the
// general ones: default 100, at most 1000.
func (s *Server) ListAuditEvents(w http.ResponseWriter, r *http.Request, _ gen.ListAuditEventsParams) {
	store, err := s.auditStore()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	cfg := s.o.API.Config
	cfg.PageSizeDefault, cfg.PageSizeMax = audit.DefaultLimit, audit.MaxLimit
	lq, err := ParseList(r, ListSpec{Filters: append([]string{"from", "to"}, auditFilters()...)}, cfg)
	var params map[string]string
	if err == nil {
		params, err = onceEach(r.URL.Query(), append([]string{"from", "to"}, auditFilters()...)...)
	}
	var filter audit.Filter
	if err == nil {
		filter, err = auditFilter(params)
	}
	var after *audit.Cursor
	if err == nil && lq.After != nil {
		after, err = auditCursor(lq.After)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	res, err := store.Query(r.Context(), filter, audit.Page{Limit: lq.Limit, After: after})
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	page := Page[auditEventView]{Items: make([]auditEventView, 0, len(res.Items))}
	for _, e := range res.Items {
		page.Items = append(page.Items, toAuditEvent(e))
	}
	if res.Next != nil {
		c := lq.EncodeCursor([]string{res.Next.OccurredAt.UTC().Format(time.RFC3339Nano), res.Next.ID})
		page.NextCursor = &c
	}
	WritePage(w, r, page)
}

// auditCursor reads the keyset position out of a cursor's key.
func auditCursor(key []string) (*audit.Cursor, error) {
	if len(key) != 2 {
		return nil, invalidCursor()
	}
	at, err := time.Parse(time.RFC3339Nano, key[0])
	if err != nil {
		return nil, invalidCursor()
	}
	return &audit.Cursor{OccurredAt: at, ID: key[1]}, nil
}

// GetAuditEvent implements GET /api/v1/audit/events/{id}.
func (s *Server) GetAuditEvent(w http.ResponseWriter, r *http.Request, id string) {
	store, err := s.auditStore()
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	e, err := store.Get(r.Context(), id)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, toAuditEvent(e))
}

// ExportAuditEvents implements GET /api/v1/audit/events/export (R-OPS-017).
//
// The event audit.exported is written in a transaction of its own before the
// first byte, so an export that cannot be recorded does not run. The events are
// then read in keyset batches, oldest first; every batch is one short query on a
// main-pool connection that is back in the pool before the batch is written to
// the client, so a slow reader holds no connection and no transaction and
// partition maintenance never waits behind an export (R-OPS-015). The answer is
// chunked: an export that is cut, by a drain or a failing batch, ends without
// the terminating chunk, which the client sees as incomplete.
func (s *Server) ExportAuditEvents(w http.ResponseWriter, r *http.Request, _ gen.ExportAuditEventsParams) {
	store, err := s.auditStore()
	if err == nil && s.o.API.Store == nil {
		err = unavailable("The audit trail")
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	names := append([]string{"from", "to", "format"}, auditFilters()...)
	if err := onlyParams(r.URL.Query(), names); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	params, err := onceEach(r.URL.Query(), names...)
	var filter audit.Filter
	if err == nil {
		filter, err = auditFilter(params)
	}
	if err == nil {
		err = audit.CheckExportRange(filter.From, filter.To)
	}
	format := "jsonl"
	if err == nil && params["format"] != "" {
		if format = params["format"]; format != "jsonl" && format != "csv" {
			err = errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter \"format\" must be jsonl or csv.")
		}
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if err := s.recordExport(r, filter, params, format); err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "audit.export"))
		return
	}

	var enc exportEncoder = jsonlEncoder{}
	ctype := "application/x-ndjson"
	if format == "csv" {
		enc, ctype = &csvEncoder{}, "text/csv"
	}
	w.Header().Set("Content-Type", ctype+"; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="audit-%s.%s"`,
		filter.From.UTC().Format("20060102T150405Z"), format))
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	var buf bytes.Buffer
	var after *audit.Cursor
	for first := true; ; first = false {
		res, err := store.Query(r.Context(), filter, audit.Page{Limit: exportBatch, After: after, Oldest: true})
		if err != nil {
			httpserver.Logger(r.Context()).Error("audit export: a batch failed, the export is cut", "error", err)
			panic(http.ErrAbortHandler)
		}
		buf.Reset()
		if first {
			enc.header(&buf)
		}
		for _, e := range res.Items {
			if err := enc.event(&buf, toAuditEvent(e)); err != nil {
				httpserver.Logger(r.Context()).Error("audit export: an event could not be encoded, the export is cut", "error", err)
				panic(http.ErrAbortHandler)
			}
		}
		if buf.Len() > 0 {
			if _, err := w.Write(buf.Bytes()); err != nil {
				return // the client went away
			}
			_ = rc.Flush()
		}
		if res.Next == nil {
			return
		}
		after = res.Next
	}
}

// onlyParams refuses a query parameter the operation does not declare.
func onlyParams(vals url.Values, allowed []string) error {
	names := make([]string, 0, len(vals))
	for k := range vals {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if !slices.Contains(allowed, k) {
			return errs.E(errs.Invalid, errs.CodeInvalid, fmt.Sprintf("The query parameter %q is not known to this endpoint.", k))
		}
	}
	return nil
}

// recordExport writes audit.exported in a transaction of its own and returns
// when it committed.
func (s *Server) recordExport(r *http.Request, f audit.Filter, params map[string]string, format string) error {
	filters := map[string]string{}
	for _, n := range auditFilters() {
		if v := params[n]; v != "" {
			filters[n] = v
		}
	}
	details, err := json.Marshal(map[string]any{
		"from": f.From.UTC().Format(time.RFC3339Nano), "to": f.To.UTC().Format(time.RFC3339Nano),
		"filters": filters, "format": format,
	})
	if err != nil {
		return err
	}
	ev := audit.Event{
		Type: audit.AuditExported, Actor: auditActor(r.Context()), Outcome: audit.Success,
		Source: auditSource(r), Details: details,
	}
	return s.o.API.Store.InTx(r.Context(), func(tx meta.Tx) error {
		return s.recorder().RecordTx(r.Context(), tx, ev)
	})
}

// exportEncoder writes the events of an export in one format.
type exportEncoder interface {
	header(*bytes.Buffer)
	event(*bytes.Buffer, auditEventView) error
}

type jsonlEncoder struct{}

func (jsonlEncoder) header(*bytes.Buffer) {}

func (jsonlEncoder) event(b *bytes.Buffer, e auditEventView) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b.Write(line)
	b.WriteByte('\n')
	return nil
}

// csvColumns are the columns of the CSV export, in order.
func csvColumns() []string {
	return []string{
		"id", "occurredAt", "action", "category", "outcome", "actorType", "actorId", "actorName", "tokenId", "authMethod",
		"sourceIp", "userAgent", "traceId", "replica", "endpoint", "resourceType", "resourceId", "repository", "path", "reason", "details",
	}
}

type csvEncoder struct{}

func (*csvEncoder) header(b *bytes.Buffer) {
	w := csv.NewWriter(b)
	_ = w.Write(csvColumns())
	w.Flush()
}

func (*csvEncoder) event(b *bytes.Buffer, e auditEventView) error {
	s := func(p *string) string {
		if p == nil {
			return ""
		}
		return csvSafe(*p)
	}
	w := csv.NewWriter(b)
	err := w.Write([]string{
		e.ID, e.OccurredAt.Format(time.RFC3339Nano), csvSafe(e.Action), e.Category, e.Outcome,
		csvSafe(e.Actor.Type), s(e.Actor.ID), s(e.Actor.Name), s(e.Actor.TokenID), s(e.Actor.AuthMethod),
		s(e.Source.IP), s(e.Source.UserAgent), s(e.TraceID), s(e.Replica), s(e.Endpoint),
		s(e.Resource.Type), s(e.Resource.ID), s(e.Repository), s(e.Path), s(e.Reason), csvSafe(string(e.Details)),
	})
	w.Flush()
	if err != nil {
		return err
	}
	return w.Error()
}

// csvSafe defuses a cell a spreadsheet would read as a formula: a value that
// starts with =, +, - or @ (also after a tab or carriage return) gets a leading
// apostrophe. The values come from clients (user agent, path), so an exported
// file is not a vector against whoever opens it.
func csvSafe(v string) string {
	if v == "" {
		return v
	}
	if strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}
