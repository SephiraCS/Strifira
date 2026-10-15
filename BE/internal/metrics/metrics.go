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

// GitHub webhook delivery outcomes recorded by GitHubWebhooks.
const (
	WebhookResultProcessed        = "processed"
	WebhookResultInvalidSignature = "invalid_signature"
	WebhookResultBadRequest       = "bad_request"
	WebhookResultError            = "error"
)

var (
	registry = prometheus.NewRegistry()

	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "strifira_http_requests_total",
		Help: "HTTP requests handled by the API server, by route template and status.",
	}, []string{"method", "route", "status"})

	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "strifira_http_request_duration_seconds",
		Help:    "HTTP request latency by route template.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})

	// GitHubWebhooks counts GitHub webhook deliveries by event and result.
	GitHubWebhooks = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "strifira_github_webhook_deliveries_total",
		Help: "GitHub webhook deliveries by event type and processing result.",
	}, []string{"event", "result"})

	// WebhookEventsDropped counts outgoing webhook work that was dropped:
	// whole events refused at Dispatch (queue_full, shutdown) and
	// subscriber deliveries a fan-out could not reach before its deadline
	// (fanout_deadline) or before a timed-out shutdown (shutdown).
	WebhookEventsDropped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "strifira_webhook_events_dropped_total",
		Help: "Outgoing webhook events or subscriber deliveries dropped, by reason.",
	}, []string{"reason"})
)

func init() {
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		httpRequests,
		httpDuration,
		GitHubWebhooks,
		WebhookEventsDropped,
	)
}

// RecordGitHubWebhook increments the webhook outcome counter. The event
// header is client-controlled (it is recorded even for unsigned deliveries),
// so only a fixed set of GitHub event names becomes a label; everything else
// is bucketed as "other" and a missing header as "unknown".
func RecordGitHubWebhook(event, result string) {
	GitHubWebhooks.WithLabelValues(webhookEventLabel(event), result).Inc()
}

// githubEvents are the GitHub webhook event names kept as metric labels:
// the ones HandleWebhookEvent processes plus the other common repository
// events GitHub sends.
var githubEvents = map[string]struct{}{
	"push": {}, "create": {}, "delete": {}, "fork": {}, "release": {},
	"repository": {}, "status": {}, "ping": {}, "branch_protection_rule": {},
	"check_run": {}, "check_suite": {}, "workflow_run": {}, "workflow_job": {},
	"issues": {}, "issue_comment": {}, "issue_assignment": {}, "issue_label": {}, "issue_milestone": {},
	"pull_request": {}, "pull_request_comment": {}, "pull_request_review": {},
	"pull_request_review_comment": {}, "pull_request_review_thread": {},
	"pull_request_assignment": {}, "pull_request_label": {}, "pull_request_milestone": {},
	"pull_request_target": {}, "label": {}, "milestone": {}, "member": {}, "team": {},
}

func webhookEventLabel(event string) string {
	if event == "" {
		return "unknown"
	}
	if _, ok := githubEvents[event]; ok {
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
