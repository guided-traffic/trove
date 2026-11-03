// Package metrics defines every metric of the trove Bastion in one place (R-BAS-040, INV-16): the names, the
// label values and the collectors. The packages of the Bastion reference the fields of [Metrics] and add no
// metric of their own; a new one is a new line here and a new row in R-BAS-040. No label carries an address, a
// token, a path or a repository key (R-BAS-040).
//
// The request metrics of R-OPS-008 (duration, in flight, bytes by handler) and trove_valkey_available and
// trove_ratelimit_fallback are the shared ones of internal/observability and internal/cluster, not defined here.
package metrics

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// The names of R-BAS-040.
const (
	RequestsTotal            = "trove_bastion_requests_total"
	BytesServedTotal         = "trove_bastion_bytes_served_total"
	MainRequestsTotal        = "trove_bastion_main_requests_total"
	MainRequestDuration      = "trove_bastion_main_request_duration_seconds"
	MainInFlight             = "trove_bastion_main_in_flight"
	MainBudget               = "trove_bastion_main_budget"
	MainBudgetExhaustedTotal = "trove_bastion_main_budget_exhausted_total"
	MainReachable            = "trove_bastion_main_reachable"
	MainUnreachableSince     = "trove_bastion_main_unreachable_since_timestamp_seconds"
	BaseURLMismatch          = "trove_bastion_base_url_mismatch"
	InterfaceMismatch        = "trove_bastion_interface_mismatch"
	InterfaceVersion         = "trove_bastion_interface_version"
	GracePeriodSeconds       = "trove_bastion_grace_period_seconds"
	AnswerPeriodSeconds      = "trove_bastion_answer_period_seconds"
	Answers                  = "trove_bastion_answers"
	AnswersTotal             = "trove_bastion_answers_total"
	FeedLagSeconds           = "trove_bastion_feed_lag_seconds"
	FeedEntriesAppliedTotal  = "trove_bastion_feed_entries_applied_total"
	FeedResetsTotal          = "trove_bastion_feed_resets_total"
	TokenRemainingSeconds    = "trove_bastion_token_remaining_seconds"   //nolint:gosec // G101: a metric name, not a credential
	TokenWarnBeforeSeconds   = "trove_bastion_token_warn_before_seconds" //nolint:gosec // G101: a metric name, not a credential
	MainTokenRefused         = "trove_bastion_main_token_refused"        //nolint:gosec // G101: a metric name, not a credential
	TokenReloadsTotal        = "trove_bastion_token_reloads_total"       //nolint:gosec // G101: a metric name, not a credential
	CacheBytes               = "trove_bastion_cache_bytes"
	CacheCapacityBytes       = "trove_bastion_cache_capacity_bytes"
	CacheEvictionsTotal      = "trove_bastion_cache_evictions_total"
	CacheCorruptTotal        = "trove_bastion_cache_corrupt_total"
	CacheErrorsTotal         = "trove_bastion_cache_errors_total"
	RefusedTotal             = "trove_bastion_refused_total"
	BlocksTotal              = "trove_bastion_blocks_total"
	BlockedAddresses         = "trove_bastion_blocked_addresses"
	AuditEventsTotal         = "trove_bastion_audit_events_total"
	AuditBufferEvents        = "trove_bastion_audit_buffer_events"
	AuditEventsDroppedTotal  = "trove_bastion_audit_events_dropped_total"
)

// Label values. The sets are closed: a value outside them is a defect, never a new series.
const (
	// class of trove_bastion_requests_total: the classes of the marking (iface.Class) and refused.
	ClassArtifact = "artifact"
	ClassShared   = "shared"
	ClassPrivate  = "private"
	ClassAbsent   = "absent"
	ClassPass     = "pass"
	ClassRefused  = "refused"

	// result of trove_bastion_requests_total.
	ResultHit     = "hit"
	ResultChecked = "checked"
	ResultMiss    = "miss"
	ResultRefused = "refused"
	ResultError   = "error"

	// source of trove_bastion_bytes_served_total.
	SourceCache = "cache"
	SourceMain  = "main"

	// kind of trove_bastion_main_requests_total and of the main request duration.
	KindForward     = "forward"
	KindConditional = "conditional"
	KindCheck       = "check"
	KindProbe       = "probe"
	KindFeed        = "feed"
	KindHandshake   = "handshake"
	KindDownloads   = "downloads"

	// outcome of trove_bastion_main_requests_total: the status class of the answer, or how the request failed.
	Outcome2xx     = "2xx"
	Outcome3xx     = "3xx"
	Outcome4xx     = "4xx"
	Outcome5xx     = "5xx"
	OutcomeError   = "error"
	OutcomeTimeout = "timeout"
	OutcomeBusy    = "busy"

	// class of the budget gauges.
	BudgetAuthenticated = "authenticated"
	BudgetAnonymous     = "anonymous"

	// side of trove_bastion_interface_version.
	SideBastion = "bastion"
	SideMain    = "main"

	// kind of trove_bastion_answers and trove_bastion_answers_total.
	AnswerCredential = "credential"
	AnswerShape      = "shape"
	AnswerURL        = "url"

	// result of trove_bastion_answers_total.
	AnswerHit          = "hit"
	AnswerMiss         = "miss"
	AnswerExpired      = "expired"
	AnswerDropped      = "dropped"
	AnswerBadSignature = "bad_signature"

	// outcome of trove_bastion_token_reloads_total.
	ReloadLoaded  = "loaded"
	ReloadRefused = "refused"

	// area of trove_bastion_cache_bytes and trove_bastion_cache_capacity_bytes.
	AreaArtifacts = "artifacts"
	AreaDocuments = "documents"

	// reason of trove_bastion_cache_evictions_total.
	EvictSize    = "size"
	EvictStale   = "stale"
	EvictMissing = "missing"

	// reason of trove_bastion_refused_total.
	RefusedBlocked     = "blocked"
	RefusedRateLimited = "rate_limited"
	RefusedMissLimited = "miss_limited"
	RefusedAuthTimeout = "auth_timeout"
	RefusedInvalid     = "invalid_token"
	RefusedBusy        = "busy"
	RefusedUnreachable = "unreachable"
	RefusedNotServed   = "not_served"
	RefusedReadOnly    = "read_only"

	// reason of trove_bastion_blocks_total.
	BlockRefusals = "refusals"

	// outcome of trove_bastion_audit_events_total.
	AuditSent    = "sent"
	AuditSpooled = "spooled"
	AuditDropped = "dropped"
)

// Cache operations of the op label of trove_bastion_cache_errors_total.
const (
	OpGet    = "get"
	OpPut    = "put"
	OpDelete = "delete"
	OpList   = "list"
	OpSpool  = "spool"
)

// Metrics holds the collectors. Every package of the Bastion that counts takes the *Metrics and uses a field;
// a nil *Metrics is not valid, tests pass one made by [New] with a nil registerer.
type Metrics struct {
	// Requests counts the requests the read flow ended by class and result; the hit rate of the cache is
	// result="hit" over all (R-BAS-040).
	Requests *prometheus.CounterVec
	// BytesServed counts the response bytes by source (cache, main).
	BytesServed *prometheus.CounterVec
	// MainRequests counts the requests to the main installation by kind and outcome (the HTTP status class, or
	// error, timeout, refused); MainDuration times them by kind.
	MainRequests *prometheus.CounterVec
	MainDuration *prometheus.HistogramVec
	// MainInFlight and MainBudget are the slots in use and the slots the replica holds, by class (R-BAS-025);
	// BudgetExhausted counts the requests that got no slot within the queue timeout.
	MainInFlight    *prometheus.GaugeVec
	MainBudget      *prometheus.GaugeVec
	BudgetExhausted *prometheus.CounterVec
	// MainReachable is 1 or 0; MainUnreachableSince the Unix time unreachability began, 0 while reachable
	// (R-BAS-027). BaseURLMismatch and InterfaceMismatch are 1 while the condition holds (R-BAS-010, R-BAS-028).
	MainReachable        prometheus.Gauge
	MainUnreachableSince prometheus.Gauge
	BaseURLMismatch      prometheus.Gauge
	InterfaceMismatch    prometheus.Gauge
	// InterfaceVersion is the two versions by side (R-BAS-028).
	InterfaceVersion *prometheus.GaugeVec
	// GracePeriod and AnswerPeriod export the configured seconds, which the rule TroveBastionGraceExhausted
	// reads (R-BAS-041).
	GracePeriod  prometheus.Gauge
	AnswerPeriod prometheus.Gauge
	// Answers is the number of answers held by kind; AnswersTotal counts lookups by kind and result (R-BAS-020).
	Answers      *prometheus.GaugeVec
	AnswersTotal *prometheus.CounterVec
	// FeedLag is the age of the last successful poll; FeedApplied counts entries by feed kind; FeedResets
	// counts resets (R-BAS-026).
	FeedLag     prometheus.Gauge
	FeedApplied *prometheus.CounterVec
	FeedResets  prometheus.Counter
	// TokenRemaining is the remaining lifetime of the token in use, TokenWarnBefore the configured warning
	// threshold, MainTokenRefused is 1 while the main installation refuses the token (R-BAS-006).
	TokenRemaining   prometheus.Gauge
	TokenWarnBefore  prometheus.Gauge
	MainTokenRefused prometheus.Gauge
	TokenReloads     *prometheus.CounterVec
	// CacheBytes and CacheCapacity are the bytes held and the limit by area; CacheEvictions counts evictions by
	// reason; CacheCorrupt counts verifications that failed; CacheErrors counts backend errors by op (R-BAS-030
	// to R-BAS-032).
	CacheBytes     *prometheus.GaugeVec
	CacheCapacity  *prometheus.GaugeVec
	CacheEvictions *prometheus.CounterVec
	CacheCorrupt   prometheus.Counter
	CacheErrors    *prometheus.CounterVec
	// Refused counts the Bastion's own refusals by reason (R-BAS-029); Blocks counts blocks set; Blocked is the
	// number of addresses blocked (R-BAS-037).
	Refused *prometheus.CounterVec
	Blocks  *prometheus.CounterVec
	Blocked prometheus.Gauge
	// AuditEvents counts download events by outcome, AuditBuffer is the buffer's fill, AuditDropped counts the
	// events a full buffer lost (R-BAS-039).
	AuditEvents  *prometheus.CounterVec
	AuditBuffer  prometheus.Gauge
	AuditDropped prometheus.Counter
}

// New creates the collectors and registers them on reg; a nil reg creates them unregistered. The series with
// a closed label set are created at zero, so every metric of the table is scrapeable from the start.
func New(reg prometheus.Registerer) (*Metrics, error) {
	counter := func(name, help string, labels ...string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	}
	gauge := func(name, help string, labels ...string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
	}
	m := &Metrics{
		Requests:     counter(RequestsTotal, "Requests of the read flow by class (artifact, shared, private, absent, pass, refused) and result (hit, checked, miss, refused, error).", "class", "result"),
		BytesServed:  counter(BytesServedTotal, "Response bytes by source (cache, main).", "source"),
		MainRequests: counter(MainRequestsTotal, "Requests to the main installation by kind and outcome.", "kind", "outcome"),
		MainDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: MainRequestDuration, Help: "Duration of requests to the main installation by kind.",
			Buckets: []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 10, 30},
		}, []string{"kind"}),
		MainInFlight:         gauge(MainInFlight, "Requests to the main installation in flight by class (authenticated, anonymous).", "class"),
		MainBudget:           gauge(MainBudget, "Slots of the concurrency budget towards the main installation held by this replica, by class.", "class"),
		BudgetExhausted:      counter(MainBudgetExhaustedTotal, "Requests that got no slot of the budget within the queue timeout, by class.", "class"),
		MainReachable:        prometheus.NewGauge(prometheus.GaugeOpts{Name: MainReachable, Help: "1 while this replica treats the main installation as reachable (R-BAS-027)."}),
		MainUnreachableSince: prometheus.NewGauge(prometheus.GaugeOpts{Name: MainUnreachableSince, Help: "Unix time at which the main installation became unreachable for this replica; 0 while reachable."}),
		BaseURLMismatch:      prometheus.NewGauge(prometheus.GaugeOpts{Name: BaseURLMismatch, Help: "1 while the main installation does not honour the Bastion's base URL (R-BAS-010)."}),
		InterfaceMismatch:    prometheus.NewGauge(prometheus.GaugeOpts{Name: InterfaceMismatch, Help: "1 while the interface versions of the Bastion and the main installation differ (R-BAS-028)."}),
		InterfaceVersion:     gauge(InterfaceVersion, "The interface version by side (bastion, main).", "side"),
		GracePeriod:          prometheus.NewGauge(prometheus.GaugeOpts{Name: GracePeriodSeconds, Help: "bastion.answers.grace_period in seconds."}),
		AnswerPeriod:         prometheus.NewGauge(prometheus.GaugeOpts{Name: AnswerPeriodSeconds, Help: "bastion.answers.period in seconds."}),
		Answers:              gauge(Answers, "Answers of the main installation held by this replica, by kind (credential, shape, url).", "kind"),
		AnswersTotal:         counter(AnswersTotal, "Answer lookups by kind and result (hit, miss, expired, dropped, bad_signature).", "kind", "result"),
		FeedLag:              prometheus.NewGauge(prometheus.GaugeOpts{Name: FeedLagSeconds, Help: "Age of the last successful feed poll in seconds (R-BAS-026)."}),
		FeedApplied:          counter(FeedEntriesAppliedTotal, "Feed entries applied by kind (token, principal, grants, settings, content).", "kind"),
		FeedResets:           prometheus.NewCounter(prometheus.CounterOpts{Name: FeedResetsTotal, Help: "Feed resets (R-BAS-026)."}),
		TokenRemaining:       prometheus.NewGauge(prometheus.GaugeOpts{Name: TokenRemainingSeconds, Help: "Remaining lifetime of the Bastion token in use in seconds (R-BAS-006)."}),
		TokenWarnBefore:      prometheus.NewGauge(prometheus.GaugeOpts{Name: TokenWarnBeforeSeconds, Help: "bastion.token.warn_before in seconds."}),
		MainTokenRefused:     prometheus.NewGauge(prometheus.GaugeOpts{Name: MainTokenRefused, Help: "1 while the main installation refuses the Bastion token (R-BAS-006)."}),
		TokenReloads:         counter(TokenReloadsTotal, "Reads of the token file by outcome (loaded, refused).", "outcome"),
		CacheBytes:           gauge(CacheBytes, "Bytes held by the cache by area (artifacts, documents).", "area"),
		CacheCapacity:        gauge(CacheCapacityBytes, "Configured size limit of the cache by area.", "area"),
		CacheEvictions:       counter(CacheEvictionsTotal, "Evictions by reason (size, stale, missing).", "reason"),
		CacheCorrupt:         prometheus.NewCounter(prometheus.CounterOpts{Name: CacheCorruptTotal, Help: "Cached objects whose verification failed (R-BAS-032)."}),
		CacheErrors:          counter(CacheErrorsTotal, "Errors of the cache backend by operation.", "op"),
		Refused:              counter(RefusedTotal, "Requests the Bastion refused itself, by reason (R-BAS-029).", "reason"),
		Blocks:               counter(BlocksTotal, "Blocks set on addresses, by reason.", "reason"),
		Blocked:              prometheus.NewGauge(prometheus.GaugeOpts{Name: BlockedAddresses, Help: "Addresses blocked now."}),
		AuditEvents:          counter(AuditEventsTotal, "Download events by outcome (sent, spooled, dropped) (R-BAS-039).", "outcome"),
		AuditBuffer:          prometheus.NewGauge(prometheus.GaugeOpts{Name: AuditBufferEvents, Help: "Download events buffered in memory."}),
		AuditDropped:         prometheus.NewCounter(prometheus.CounterOpts{Name: AuditEventsDroppedTotal, Help: "Download events lost to a full buffer."}),
	}
	m.seed()
	if reg == nil {
		return m, nil
	}
	for _, c := range m.collectors() {
		if err := reg.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				return nil, errors.New("bastion metrics are already registered on this registerer")
			}
			return nil, err
		}
	}
	return m, nil
}

// seed creates the series of the closed label sets at zero.
func (m *Metrics) seed() {
	for _, c := range []string{ClassArtifact, ClassShared, ClassPrivate, ClassAbsent, ClassPass, ClassRefused} {
		for _, r := range []string{ResultHit, ResultChecked, ResultMiss, ResultRefused, ResultError} {
			m.Requests.WithLabelValues(c, r)
		}
	}
	for _, s := range []string{SourceCache, SourceMain} {
		m.BytesServed.WithLabelValues(s)
	}
	for _, k := range []string{KindForward, KindConditional, KindCheck, KindProbe, KindFeed, KindHandshake, KindDownloads} {
		m.MainDuration.WithLabelValues(k)
		for _, o := range []string{Outcome2xx, Outcome3xx, Outcome4xx, Outcome5xx, OutcomeError, OutcomeTimeout, OutcomeBusy} {
			m.MainRequests.WithLabelValues(k, o)
		}
	}
	for _, c := range []string{BudgetAuthenticated, BudgetAnonymous} {
		m.MainInFlight.WithLabelValues(c)
		m.MainBudget.WithLabelValues(c)
		m.BudgetExhausted.WithLabelValues(c)
	}
	for _, s := range []string{SideBastion, SideMain} {
		m.InterfaceVersion.WithLabelValues(s)
	}
	for _, k := range []string{AnswerCredential, AnswerShape, AnswerURL} {
		m.Answers.WithLabelValues(k)
		for _, r := range []string{AnswerHit, AnswerMiss, AnswerExpired, AnswerDropped, AnswerBadSignature} {
			m.AnswersTotal.WithLabelValues(k, r)
		}
	}
	for _, k := range []string{"token", "principal", "grants", "settings", "content"} {
		m.FeedApplied.WithLabelValues(k)
	}
	for _, o := range []string{ReloadLoaded, ReloadRefused} {
		m.TokenReloads.WithLabelValues(o)
	}
	for _, a := range []string{AreaArtifacts, AreaDocuments} {
		m.CacheBytes.WithLabelValues(a)
		m.CacheCapacity.WithLabelValues(a)
	}
	for _, r := range []string{EvictSize, EvictStale, EvictMissing} {
		m.CacheEvictions.WithLabelValues(r)
	}
	for _, o := range []string{OpGet, OpPut, OpDelete, OpList, OpSpool} {
		m.CacheErrors.WithLabelValues(o)
	}
	for _, r := range []string{RefusedBlocked, RefusedRateLimited, RefusedMissLimited, RefusedAuthTimeout, RefusedInvalid, RefusedBusy, RefusedUnreachable, RefusedNotServed, RefusedReadOnly} {
		m.Refused.WithLabelValues(r)
	}
	m.Blocks.WithLabelValues(BlockRefusals)
	for _, o := range []string{AuditSent, AuditSpooled, AuditDropped} {
		m.AuditEvents.WithLabelValues(o)
	}
}

func (m *Metrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{
		m.Requests, m.BytesServed, m.MainRequests, m.MainDuration, m.MainInFlight, m.MainBudget, m.BudgetExhausted,
		m.MainReachable, m.MainUnreachableSince, m.BaseURLMismatch, m.InterfaceMismatch, m.InterfaceVersion,
		m.GracePeriod, m.AnswerPeriod, m.Answers, m.AnswersTotal, m.FeedLag, m.FeedApplied, m.FeedResets,
		m.TokenRemaining, m.TokenWarnBefore, m.MainTokenRefused, m.TokenReloads,
		m.CacheBytes, m.CacheCapacity, m.CacheEvictions, m.CacheCorrupt, m.CacheErrors,
		m.Refused, m.Blocks, m.Blocked, m.AuditEvents, m.AuditBuffer, m.AuditDropped,
	}
}

// Names lists the metric names the table of R-BAS-040 holds, for the test that the table and the code agree.
func Names() []string {
	return []string{
		RequestsTotal, BytesServedTotal, MainRequestsTotal, MainRequestDuration, MainInFlight, MainBudget,
		MainBudgetExhaustedTotal, MainReachable, MainUnreachableSince, BaseURLMismatch, InterfaceMismatch,
		InterfaceVersion, GracePeriodSeconds, AnswerPeriodSeconds, Answers, AnswersTotal, FeedLagSeconds,
		FeedEntriesAppliedTotal, FeedResetsTotal, TokenRemainingSeconds, TokenWarnBeforeSeconds, MainTokenRefused,
		TokenReloadsTotal, CacheBytes, CacheCapacityBytes, CacheEvictionsTotal, CacheCorruptTotal, CacheErrorsTotal,
		RefusedTotal, BlocksTotal, BlockedAddresses, AuditEventsTotal, AuditBufferEvents, AuditEventsDroppedTotal,
	}
}
