package tlsfiles

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// Metric families of the TLS material. Their labels are bounded by the
// configuration: kind is one of the Kind constants, listener is client, ops or
// empty, name is the configured file path of the piece.
const (
	// FamilyExpiry is the notAfter of the served certificate of a pair, or the
	// earliest notAfter of the certificates of a bundle, as a Unix timestamp.
	FamilyExpiry = "trove_tls_certificate_expiry_timestamp_seconds"
	// FamilyReloads counts content changes that were loaded and swapped in after
	// the first load.
	FamilyReloads = "trove_tls_reloads_total"
	// FamilyReloadErrors counts content that did not load; the previous content of
	// the piece stays in service. One distinct content counts once.
	FamilyReloadErrors = "trove_tls_reload_errors_total"
)

// Kinds of material: the value of the kind label. A listener's material also
// carries the listener label.
const (
	KindListener    = "listener"
	KindDatabase    = "database"
	KindValkey      = "valkey"
	KindObjectStore = "objectstore"
	KindOutbound    = "outbound"
)

// Metrics are the three families, registered once per process and shared by
// every Reloader. A nil *Metrics records nothing.
type Metrics struct {
	expiry  *prometheus.GaugeVec
	reloads *prometheus.CounterVec
	errors  *prometheus.CounterVec
}

func labelNames() []string { return []string{"kind", "listener", "name"} }

// NewMetrics registers the families on reg. A registry that already holds them
// (a second call in the same process) gives back the registered collectors.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		expiry: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: FamilyExpiry,
			Help: "Expiry (notAfter) of the served TLS certificate of a pair, or the earliest of a CA bundle, as a Unix timestamp.",
		}, labelNames()),
		reloads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: FamilyReloads,
			Help: "Changed TLS material that was loaded and is served from the next handshake or connection on.",
		}, labelNames()),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: FamilyReloadErrors,
			Help: "TLS material that did not load; the previous material stays in service. Counted once per distinct content.",
		}, labelNames()),
	}
	var err error
	if m.expiry, err = register(reg, m.expiry); err != nil {
		return nil, err
	}
	if m.reloads, err = register(reg, m.reloads); err != nil {
		return nil, err
	}
	if m.errors, err = register(reg, m.errors); err != nil {
		return nil, err
	}
	return m, nil
}

func register[C prometheus.Collector](reg prometheus.Registerer, c C) (C, error) {
	err := reg.Register(c)
	var already prometheus.AlreadyRegisteredError
	switch {
	case err == nil:
		return c, nil
	case errors.As(err, &already):
		if existing, ok := already.ExistingCollector.(C); ok {
			return existing, nil
		}
	}
	return c, err
}

// series are the label values of one piece.
type series struct{ kind, listener, name string }

func (m *Metrics) init(s series) {
	if m == nil {
		return
	}
	m.reloads.WithLabelValues(s.kind, s.listener, s.name)
	m.errors.WithLabelValues(s.kind, s.listener, s.name)
}

func (m *Metrics) reloaded(s series) {
	if m != nil {
		m.reloads.WithLabelValues(s.kind, s.listener, s.name).Inc()
	}
}

func (m *Metrics) failed(s series) {
	if m != nil {
		m.errors.WithLabelValues(s.kind, s.listener, s.name).Inc()
	}
}

func (m *Metrics) setExpiry(s series, unix float64) {
	if m != nil {
		m.expiry.WithLabelValues(s.kind, s.listener, s.name).Set(unix)
	}
}
