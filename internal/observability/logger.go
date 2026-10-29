package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

// Log stream names, the value of the log field (R-OPS-009).
const (
	StreamApp      = "app"
	StreamRequest  = "request"
	StreamUpstream = "upstream"
	StreamAudit    = "audit"
)

// Components returns the registered log components of R-OPS-012. Other
// packages add theirs with Logging.Component.
func Components() []string {
	return []string{"api", "audit", "auth", "authz", "config", "db", "gc", "http", "jobs", "metrics", "npm", "nuget", "proxy", "storage", "tracing", "webhooks"}
}

// LogOption configures NewLogging and NewLogger.
type LogOption func(*logOptions)

type logOptions struct {
	replica, role string
}

// WithIdentity sets the replica and role fields of every record (R-OPS-009).
func WithIdentity(replica, role string) LogOption {
	return func(o *logOptions) { o.replica, o.role = replica, role }
}

// Logging is the process-wide logging state: the output, the ring buffer and
// the level of every component.
type Logging struct {
	cfg   config.Log
	base  slog.Handler
	ring  *Ring
	scrub *Scrubber

	mu       sync.Mutex
	levels   map[string]*slog.LevelVar
	override map[string]slog.Level
	expiry   time.Time
	timer    *time.Timer
	def      *slog.LevelVar
}

// NewLogger returns the application logger (log=app) writing JSON records to w.
func NewLogger(cfg config.Log, w io.Writer, opts ...LogOption) *slog.Logger {
	return NewLogging(cfg, w, opts...).Logger()
}

// NewLogging builds the logging state. Every record goes to w and, after
// redaction, into the ring buffer bounded by cfg.RingBufferRecords records and
// cfg.RingBufferBytes bytes.
func NewLogging(cfg config.Log, w io.Writer, opts ...LogOption) *Logging {
	var o logOptions
	for _, f := range opts {
		f(&o)
	}
	if w == nil {
		w = os.Stdout
	}
	params := cfg.Request.RedactQueryParams
	if params == nil {
		params = DefaultQueryParams()
	}
	l := &Logging{
		cfg:    cfg,
		ring:   NewRing(cfg.RingBufferRecords, int64(cfg.RingBufferBytes)),
		scrub:  NewScrubber(params),
		levels: map[string]*slog.LevelVar{},
		def:    new(slog.LevelVar),
	}
	l.def.Set(parseLevel(cfg.Level))
	hopts := &slog.HandlerOptions{
		AddSource:   cfg.AddSource,
		Level:       slog.LevelDebug, // the component level decides, see compHandler.Enabled
		ReplaceAttr: l.replaceAttr,
	}
	out := io.MultiWriter(w, l.ring)
	if cfg.Format == "text" {
		l.base = slog.NewTextHandler(out, hopts)
	} else {
		l.base = slog.NewJSONHandler(out, hopts)
	}
	if o.replica != "" {
		l.base = l.base.WithAttrs([]slog.Attr{slog.String("replica", o.replica)})
	}
	if o.role != "" {
		l.base = l.base.WithAttrs([]slog.Attr{slog.String("role", o.role)})
	}
	return l
}

// Logger returns the application logger (log=app, no component).
func (l *Logging) Logger() *slog.Logger { return l.Stream(StreamApp) }

// Stream returns a logger for one of the record streams.
func (l *Logging) Stream(name string) *slog.Logger {
	h := &compHandler{core: l, stream: name, level: l.def}
	h.rebuild()
	return slog.New(h)
}

// Component returns the application logger of a registered component. Its level
// follows log.levels and any active override without a restart (R-OPS-012).
func (l *Logging) Component(name string) *slog.Logger {
	return l.Logger().With("component", name)
}

// Ring returns the in-memory record buffer.
func (l *Logging) Ring() *Ring { return l.ring }

// Scrubber returns the scrubber built from log.request.redact_query_params.
func (l *Logging) Scrubber() *Scrubber { return l.scrub }

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// componentLevel returns the level variable of a component, creating it.
func (l *Logging) componentLevel(name string) *slog.LevelVar {
	l.mu.Lock()
	defer l.mu.Unlock()
	if lv, ok := l.levels[name]; ok {
		return lv
	}
	lv := new(slog.LevelVar)
	lv.Set(l.effectiveLocked(name))
	l.levels[name] = lv
	return lv
}

func (l *Logging) effectiveLocked(name string) slog.Level {
	if lvl, ok := l.override[name]; ok {
		return lvl
	}
	if s, ok := l.cfg.Levels[name]; ok {
		return parseLevel(s)
	}
	return parseLevel(l.cfg.Level)
}

func (l *Logging) refreshLocked() {
	for name, lv := range l.levels {
		lv.Set(l.effectiveLocked(name))
	}
}

// Override is an active temporary level override.
type Override struct {
	Levels    map[string]string
	ExpiresAt time.Time
}

// SetOverride lowers or raises component levels until expiry and reverts them
// locally at that time (R-OPS-012). An unknown component or level is an error.
// A new override replaces the active one.
func (l *Logging) SetOverride(levels map[string]string, d time.Duration) (Override, error) {
	known := map[string]bool{}
	for _, c := range Components() {
		known[c] = true
	}
	l.mu.Lock()
	for c := range l.levels {
		known[c] = true
	}
	l.mu.Unlock()
	parsed := make(map[string]slog.Level, len(levels))
	for c, s := range levels {
		if !known[c] {
			return Override{}, fmt.Errorf("unknown log component %q", c)
		}
		switch strings.ToLower(s) {
		case "debug", "info", "warn", "error":
			parsed[c] = parseLevel(s)
		default:
			return Override{}, fmt.Errorf("level %q of component %q must be debug, info, warn or error", s, c)
		}
	}
	if d < time.Minute || (l.cfg.OverrideMaxDuration > 0 && d > l.cfg.OverrideMaxDuration) {
		return Override{}, fmt.Errorf("override duration %s must lie between 1m and %s", d, l.cfg.OverrideMaxDuration)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.timer != nil {
		l.timer.Stop()
	}
	l.override = parsed
	l.expiry = time.Now().Add(d)
	l.timer = time.AfterFunc(d, l.ClearOverride)
	l.refreshLocked()
	return Override{Levels: copyMap(levels), ExpiresAt: l.expiry}, nil
}

// ClearOverride removes the active override.
func (l *Logging) ClearOverride() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	l.override, l.expiry = nil, time.Time{}
	l.refreshLocked()
}

// ActiveOverride returns the active override, if any.
func (l *Logging) ActiveOverride() (Override, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.override == nil {
		return Override{}, false
	}
	out := Override{Levels: map[string]string{}, ExpiresAt: l.expiry}
	for c, lvl := range l.override {
		out.Levels[c] = strings.ToLower(lvl.String())
	}
	return out, true
}

// ConfiguredLevels returns the effective level of every known component.
func (l *Logging) ConfiguredLevels() map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]string{}
	for _, c := range Components() {
		out[c] = strings.ToLower(l.effectiveLocked(c).String())
	}
	for c := range l.levels {
		out[c] = strings.ToLower(l.effectiveLocked(c).String())
	}
	return out
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// replaceAttr applies the format rules of R-OPS-009 and the redaction rules of
// R-OPS-013 to every attribute.
func (l *Logging) replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 {
		switch a.Key {
		case slog.TimeKey:
			if t, ok := a.Value.Any().(time.Time); ok {
				return slog.String(slog.TimeKey, t.UTC().Format("2006-01-02T15:04:05.000Z07:00"))
			}
			return a
		case slog.LevelKey:
			return a
		case slog.MessageKey:
			return slog.String(slog.MessageKey, l.scrub.String(a.Value.String()))
		case slog.SourceKey:
			if src, ok := a.Value.Any().(*slog.Source); ok && src != nil {
				return slog.String("source", src.File+":"+strconv.Itoa(src.Line))
			}
		}
	}
	if SensitiveKey(a.Key) {
		return slog.String(a.Key, RedactedValue)
	}
	v := a.Value
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, l.scrub.String(v.String()))
	case slog.KindAny:
		return slog.Attr{Key: a.Key, Value: l.scrubAny(v.Any())}
	}
	return a
}

// scrubAny applies R-OPS-013 to a value of kind Any, whatever its type: an
// error, a fmt.Stringer and a URL become their scrubbed text; an http.Header
// keeps its names, with the values of sensitive headers redacted and the others
// scrubbed; anything else (maps, slices, structs) is encoded as JSON, decoded
// again, and every string in it is scrubbed and every value under a sensitive
// key redacted. Nothing of kind Any reaches the output unscrubbed.
func (l *Logging) scrubAny(x any) slog.Value {
	if isNil(x) {
		return slog.AnyValue(nil)
	}
	switch t := x.(type) {
	case config.Secret, *config.Secret:
		return slog.StringValue(RedactedValue)
	case *url.URL:
		return slog.StringValue(l.scrub.URL(t))
	case url.URL:
		return slog.StringValue(l.scrub.URL(&t))
	case http.Header:
		return slog.AnyValue(l.scrubHeader(t))
	case error:
		return slog.StringValue(l.scrub.String(t.Error()))
	case fmt.Stringer:
		return slog.StringValue(l.scrub.String(t.String()))
	case []string:
		out := make([]string, len(t))
		for i, s := range t {
			out[i] = l.scrub.String(s)
		}
		return slog.AnyValue(out)
	}
	b, err := json.Marshal(x)
	if err != nil {
		return slog.StringValue(l.scrub.String(fmt.Sprintf("%+v", x)))
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return slog.StringValue(l.scrub.String(string(b)))
	}
	return slog.AnyValue(l.scrubTree(tree))
}

// scrubTree scrubs a decoded JSON value in place.
func (l *Logging) scrubTree(v any) any {
	switch t := v.(type) {
	case string:
		return l.scrub.String(t)
	case []any:
		for i := range t {
			t[i] = l.scrubTree(t[i])
		}
	case map[string]any:
		for k, x := range t {
			if SensitiveKey(k) {
				t[k] = RedactedValue
			} else {
				t[k] = l.scrubTree(x)
			}
		}
	}
	return v
}

// scrubHeader copies h with the values of sensitive headers (Authorization,
// Cookie, X-Api-Key, …: SensitiveKey of the name with "-" read as "_")
// redacted and every other value scrubbed.
func (l *Logging) scrubHeader(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for name, vs := range h {
		if SensitiveKey(strings.ReplaceAll(name, "-", "_")) {
			out[name] = []string{RedactedValue}
			continue
		}
		c := make([]string, len(vs))
		for i, s := range vs {
			c[i] = l.scrub.String(s)
		}
		out[name] = c
	}
	return out
}

// isNil reports a nil interface or a nil pointer, map, slice or function inside
// one, so that a typed nil error is logged as null instead of panicking in Error.
func isNil(x any) bool {
	if x == nil {
		return true
	}
	switch rv := reflect.ValueOf(x); rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return rv.IsNil()
	}
	return false
}

// compHandler wraps the output handler with the per-component level, the log
// stream field and the component field. It keeps the attributes and groups
// added with With/WithGroup so that a later log stream replaces the earlier one
// instead of duplicating the key.
type compHandler struct {
	core      *Logging
	inner     slog.Handler
	stream    string
	component string
	level     *slog.LevelVar
	ops       []handlerOp
}

type handlerOp struct {
	attrs []slog.Attr
	group string
}

func (h *compHandler) Enabled(_ context.Context, lvl slog.Level) bool {
	return lvl >= h.level.Level()
}

func (h *compHandler) Handle(ctx context.Context, r slog.Record) error {
	if !recordHas(r, "trace_id") {
		if id := RequestID(ctx); id != "" {
			r = r.Clone()
			r.AddAttrs(slog.String("trace_id", id))
			if sp := SpanID(ctx); sp != "" {
				r.AddAttrs(slog.String("span_id", sp))
			}
		}
	}
	return h.inner.Handle(ctx, r)
}

// rebuild derives the inner handler from the base: the stream first, then the
// recorded attributes and groups in order.
func (h *compHandler) rebuild() {
	in := h.core.base.WithAttrs([]slog.Attr{slog.String("log", h.stream)})
	for _, op := range h.ops {
		if op.group != "" {
			in = in.WithGroup(op.group)
		} else {
			in = in.WithAttrs(op.attrs)
		}
	}
	h.inner = in
}

func (h *compHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.ops = append([]handlerOp(nil), h.ops...)
	var keep []slog.Attr
	for _, a := range attrs {
		switch a.Key {
		case "log":
			c.stream = a.Value.String()
		case "component":
			c.component = a.Value.String()
			c.level = h.core.componentLevel(c.component)
			keep = append(keep, a)
		default:
			keep = append(keep, a)
		}
	}
	if len(keep) > 0 {
		c.ops = append(c.ops, handlerOp{attrs: keep})
	}
	c.rebuild()
	return &c
}

func (h *compHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.ops = append(append([]handlerOp(nil), h.ops...), handlerOp{group: name})
	c.rebuild()
	return &c
}

func recordHas(r slog.Record, key string) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			found = true
		}
		return !found
	})
	return found
}
