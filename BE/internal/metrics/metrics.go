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

// Reasons recorded by WebhookEventsDropped.
const (
	WebhookDropQueueFull      = "queue_full"
	WebhookDropShutdown       = "shutdown"
	WebhookDropFanoutDeadline = "fanout_deadline"
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

	// WebhookEventsDropped counts outgoing webhook work that was dropped:
	// whole events refused at Dispatch (queue_full, shutdown) and
	// subscriber deliveries a fan-out could not reach before its deadline
	// (fanout_deadline) or before a timed-out shutdown (shutdown).
	WebhookEventsDropped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ku_webhook_events_dropped_total",
		Help: "Outgoing webhook events or subscriber deliveries dropped, by reason.",
	}, []string{"reason"})
)

func init() {
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		httpRequests,
		httpDuration,
		ForgejoWebhooks,
		WebhookEventsDropped,
	)
}

// RecordForgejoWebhook increments the webhook outcome counter. The event
// header is client-controlled (it is recorded even for unsigned deliveries),
// so only a fixed set of Forgejo event names becomes a label; everything else
// is bucketed as "other" and a missing header as "unknown".
func RecordForgejoWebhook(event, result string) {
	ForgejoWebhooks.WithLabelValues(webhookEventLabel(event), result).Inc()
}

// forgejoEvents are the Forgejo webhook event names kept as metric labels:
// the ones HandleWebhookEvent processes plus the other common repository
// events Forgejo sends.
var forgejoEvents = map[string]struct{}{
	"push": {}, "create": {}, "delete": {}, "fork": {}, "release": {},
	"repository": {}, "wiki": {}, "package": {}, "status": {},
	"issues": {}, "issue_assign": {}, "issue_label": {}, "issue_milestone": {}, "issue_comment": {},
	"pull_request": {}, "pull_request_assign": {}, "pull_request_label": {}, "pull_request_milestone": {},
	"pull_request_comment": {}, "pull_request_review_approved": {}, "pull_request_review_rejected": {},
	"pull_request_review_comment": {}, "pull_request_sync": {}, "pull_request_review_request": {},
	"action_run_success": {}, "action_run_failure": {}, "action_run_recover": {},
}

func webhookEventLabel(event string) string {
	if event == "" {
		return "unknown"
	}
	if _, ok := forgejoEvents[event]; ok {
		return event
	}
	return "other"
}

// httpMethods are the request methods kept as metric labels; any other
// (client-chosen) method token is recorded as "other".
var httpMethods = map[string]struct{}{
	http.MethodGet: {}, http.MethodHead: {}, http.MethodPost: {}, http.MethodPut: {},
	http.MethodPatch: {}, http.MethodDelete: {}, http.MethodOptions: {},
}

func methodLabel(method string) string {
	if _, ok := httpMethods[method]; ok {
		return method
	}
	return "other"
}

// Middleware records request count and latency labelled by the matched route
// template (c.Path()), never the raw URL, so identifiers do not explode label
// cardinality. Unmatched requests share the "unmatched" route label and
// unknown methods the "other" method label. Register it outside Recovery so
// recovered panics are observed as the 500 Recovery writes.
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
			method := methodLabel(c.Request().Method)
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
