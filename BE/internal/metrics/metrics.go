// Package metrics exposes the API server's Prometheus metrics. The /metrics
// endpoint is meant for in-cluster scraping only: the production reverse
// proxies forward /api/*, /health, /ready and /uploads/* to the backend and
// nothing else, so /metrics is never routed from the public internet.
package metrics

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Forgejo webhook delivery outcomes recorded by ForgejoWebhooks.
const (
	WebhookResultProcessed        = "processed"
	WebhookResultInvalidSignature = "invalid_signature"
	WebhookResultBadRequest       = "bad_request"
	WebhookResultError            = "error"
)

var (
	registry = prometheus.NewRegistry()

	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ku_http_requests_total",
		Help: "HTTP requests handled by the API server, by route template and status.",
	}, []string{"method", "route", "status"})

	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ku_http_request_duration_seconds",
		Help:    "HTTP request latency by route template.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})

	// ForgejoWebhooks counts Forgejo webhook deliveries by event and result.
	ForgejoWebhooks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ku_forgejo_webhook_deliveries_total",
		Help: "Forgejo webhook deliveries by event type and processing result.",
	}, []string{"event", "result"})
)

func init() {
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		httpRequests,
		httpDuration,
		ForgejoWebhooks,
	)
}

// RecordForgejoWebhook increments the webhook outcome counter. Unknown or
// empty event names are bucketed to keep label cardinality bounded.
func RecordForgejoWebhook(event, result string) {
	ForgejoWebhooks.WithLabelValues(webhookEventLabel(event), result).Inc()
}

func webhookEventLabel(event string) string {
	if event == "" {
		return "unknown"
	}
	if len(event) > 64 {
		return "other"
	}
	for _, r := range event {
		if (r < 'a' || r > 'z') && r != '_' {
			return "other"
		}
	}
	return event
}

// Middleware records request count and latency labelled by the matched route
// template (c.Path()), never the raw URL, so identifiers do not explode label
// cardinality. Unmatched requests share the "unmatched" route label.
func Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			route := c.Path()
			if route == "" || route == "/*" {
				route = "unmatched"
			}
			if route == "/metrics" {
				return err
			}
			status := c.Response().Status
			if err != nil && !c.Response().Committed {
				status = http.StatusInternalServerError
				var httpErr *echo.HTTPError
				if errors.As(err, &httpErr) {
					status = httpErr.Code
				}
			}
			method := c.Request().Method
			httpRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
			httpDuration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
			return err
		}
	}
}

// Handler serves the registry in the Prometheus exposition format.
func Handler() echo.HandlerFunc {
	return echo.WrapHandler(promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
}
