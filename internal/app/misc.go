package app

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/buildinfo"
	"github.com/guided-traffic/trove/internal/observability"
)

type httpHandler = http.Handler

// uiDefaultLocale and uiLocales are fixed until a ui configuration section
// exists (R-UI-001 names ui.locales and ui.default_locale).
const uiDefaultLocale = "en"

func uiLocales() []string { return []string{"en", "de"} }

func buildVersion() string { return buildinfo.Get().Version }

func observabilityRequestID(ctx context.Context) string { return observability.RequestID(ctx) }

// metricsHandler is GET /metrics; the Metrics handler applies metrics.enabled and
// metrics.auth itself.
func (a *App) metricsHandler() http.Handler { return a.tele.metrics.Handler() }
