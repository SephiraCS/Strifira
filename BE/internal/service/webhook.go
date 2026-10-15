package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/metrics"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/pkg/crypto"
	"github.com/SephiraCS/strifira-backend/pkg/validate"
	"github.com/google/uuid"
	"github.com/lib/pq"
	log "github.com/sirupsen/logrus"
)

type WebhookService struct {
	webhookRepo   *repository.WebhookRepository
	encryptionKey []byte
	httpClient    *http.Client
	pool          webhookPool
}

// ErrInvalidWebhookURL marks a webhook URL rejected by validation; its
// message is safe to show to the caller.
var ErrInvalidWebhookURL = errors.New("invalid webhook URL")

// invalidWebhookURLError keeps the validation detail while matching
// ErrInvalidWebhookURL with errors.Is.
type invalidWebhookURLError struct{ cause error }

func (e invalidWebhookURLError) Error() string        { return "invalid webhook URL: " + e.cause.Error() }
func (e invalidWebhookURLError) Is(target error) bool { return target == ErrInvalidWebhookURL }

func NewWebhookService(webhookRepo *repository.WebhookRepository, jwtSecret string) *WebhookService {
	return &WebhookService{
		webhookRepo:   webhookRepo,
		encryptionKey: crypto.DeriveKey(jwtSecret + ":webhook"),
		httpClient:    newWebhookHTTPClient(publicWebhookIP),
	}
}

// WithAllowedAddresses replaces the delivery-time destination check. The
// production default (publicWebhookIP) refuses every address that
// validate.IsBlockedIP blocks; tests use this to reach loopback receivers.
func (s *WebhookService) WithAllowedAddresses(allowed func(net.IP) bool) *WebhookService {
	s.httpClient = newWebhookHTTPClient(allowed)
	return s
}

func publicWebhookIP(ip net.IP) bool { return !validate.IsBlockedIP(ip) }

// errWebhookDestinationBlocked is returned when a delivery would connect to
// an address outgoing webhooks may not reach.
var errWebhookDestinationBlocked = errors.New("webhook destination address is not allowed")

// newWebhookHTTPClient builds the delivery client. URLs are validated when
// saved, but DNS can change afterwards and a receiver could redirect, so:
// redirects are never followed, no environment proxy is used, and the
// resolved address of every connection is checked against allowed right
// before connecting (defeating DNS rebinding).
func newWebhookHTTPClient(allowed func(net.IP) bool) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return errWebhookDestinationBlocked
			}
			ip := net.ParseIP(host)
			if ip == nil || !allowed(ip) {
				return errWebhookDestinationBlocked
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (s *WebhookService) Create(ctx context.Context, workspaceID uuid.UUID, req dto.CreateWebhookRequest) (*domain.Webhook, error) {
	if err := validate.ValidateWebhookURL(req.URL); err != nil {
		return nil, invalidWebhookURLError{err}
	}

	encSecret, err := crypto.Encrypt(req.Secret, s.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt webhook secret: %w", err)
	}

	w := &domain.Webhook{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		URL:         req.URL,
		Secret:      encSecret,
		Events:      pq.StringArray(req.Events),
		IsActive:    true,
	}
	if err := s.webhookRepo.Create(ctx, w); err != nil {
		return nil, err
	}
	return w, nil
}

func (s *WebhookService) List(ctx context.Context, workspaceID uuid.UUID) ([]domain.Webhook, error) {
	return s.webhookRepo.ListByWorkspace(ctx, workspaceID)
}

func (s *WebhookService) Update(ctx context.Context, workspaceID, id uuid.UUID, req dto.UpdateWebhookRequest) (*domain.Webhook, error) {
	w, err := s.webhookRepo.GetByID(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, ErrWebhookNotFound
	}
	if req.URL != nil {
		if err := validate.ValidateWebhookURL(*req.URL); err != nil {
			return nil, invalidWebhookURLError{err}
		}
		w.URL = *req.URL
	}
	if req.Events != nil {
		w.Events = pq.StringArray(req.Events)
	}
	if req.IsActive != nil {
		w.IsActive = *req.IsActive
	}
	if err := s.webhookRepo.Update(ctx, w); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrWebhookNotFound
		}
		return nil, err
	}
	return w, nil
}

func (s *WebhookService) Delete(ctx context.Context, workspaceID, id uuid.UUID) error {
	deleted, err := s.webhookRepo.Delete(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrWebhookNotFound
	}
	return nil
}

// webhookEvents are the realtime events that outgoing webhooks may subscribe
// to (the set offered by the webhook settings UI).
var webhookEvents = map[string]bool{
	"issue.created":   true,
	"issue.updated":   true,
	"issue.deleted":   true,
	"issue.triaged":   true,
	"comment.created": true,
	"cycle.completed": true,
	"project.updated": true,
}

// webhookDeliveryTimeout bounds one job (listing the subscribers of an
// event, or one delivery). Jobs run detached from the triggering request.
const webhookDeliveryTimeout = 15 * time.Second

// Default delivery pool: a fixed number of workers drain a bounded queue so
// webhook traffic (slow receivers, bursts of broadcasts) cannot grow
// goroutines, sockets or database queries without limit.
const (
	defaultWebhookWorkers   = 8
	defaultWebhookQueueSize = 1024
)

// webhookJob is either a dispatch (find the subscribers of event in a
// workspace) or, when target is set, one delivery to that webhook.
type webhookJob struct {
	workspaceID uuid.UUID
	event       string
	body        []byte
	target      *domain.Webhook
}

// webhookPool is the bounded delivery queue and its workers.
type webhookPool struct {
	workers    int
	size       int
	jobTimeout time.Duration

	start   sync.Once
	mu      sync.RWMutex // guards closed and sends on queue
	closed  bool
	queue   chan webhookJob
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
	stopped chan struct{}
}

// WithDeliveryPool sizes the delivery worker pool and queue. It must be
// called before the first Dispatch.
func (s *WebhookService) WithDeliveryPool(workers, queueSize int) *WebhookService {
	s.pool.workers, s.pool.size = max(workers, 1), max(queueSize, 1)
	return s
}

func (s *WebhookService) startPool() {
	p := &s.pool
	p.start.Do(func() {
		if p.workers == 0 {
			p.workers, p.size = defaultWebhookWorkers, defaultWebhookQueueSize
		}
		p.queue = make(chan webhookJob, p.size)
		p.ctx, p.cancel = context.WithCancel(context.Background())
		p.stopped = make(chan struct{})
		for range p.workers {
			p.wg.Add(1)
			go s.worker()
		}
		go func() { p.wg.Wait(); close(p.stopped) }()
	})
}

func (s *WebhookService) closing() bool {
	s.pool.mu.RLock()
	defer s.pool.mu.RUnlock()
	return s.pool.closed
}

// enqueue offers job to the queue without blocking. It reports false when
// the queue is full or the pool is shutting down.
func (s *WebhookService) enqueue(job webhookJob) bool {
	p := &s.pool
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return false
	}
	select {
	case p.queue <- job:
		return true
	default:
		return false
	}
}

func (s *WebhookService) worker() {
	defer s.pool.wg.Done()
	for job := range s.pool.queue {
		// After a shutdown timeout the backlog is discarded, not run, and
		// counted: a dispatch job is one event, a target job one delivery.
		if s.pool.ctx.Err() != nil {
			metrics.WebhookEventsDropped.WithLabelValues(metrics.WebhookDropShutdown).Inc()
			continue
		}
		s.run(job)
	}
}

func (s *WebhookService) run(job webhookJob) {
	// A panicking delivery must not take a worker (or the process) down.
	defer func() {
		if r := recover(); r != nil {
			log.WithField("panic", fmt.Sprintf("%v", r)).Error("webhook job panicked")
		}
	}()
	// One deadline per job, covering the whole subscriber fan-out including
	// any inline deliveries, so a worker always returns to the shared queue
	// within jobTimeout.
	ctx, cancel := context.WithTimeout(s.pool.ctx, s.jobTimeout())
	defer cancel()
	if job.target != nil {
		s.deliverOne(ctx, *job.target, job.event, job.body)
		return
	}
	webhooks, err := s.webhookRepo.ListByWorkspace(ctx, job.workspaceID)
	if err != nil {
		log.WithError(err).Warn("failed to list webhooks for dispatch")
		return
	}
	targets := make([]domain.Webhook, 0, len(webhooks))
	for _, wh := range webhooks {
		if wh.IsActive && slices.Contains([]string(wh.Events), job.event) {
			targets = append(targets, wh)
		}
	}
	for i, wh := range targets {
		if ctx.Err() != nil {
			// Shutdown timed out or the job used up its deadline: the rest
			// of the fan-out is dropped and accounted for.
			reason := metrics.WebhookDropFanoutDeadline
			if s.pool.ctx.Err() != nil {
				reason = metrics.WebhookDropShutdown
			}
			undelivered := len(targets) - i
			metrics.WebhookEventsDropped.WithLabelValues(reason).Add(float64(undelivered))
			log.WithFields(log.Fields{
				"workspace_id": job.workspaceID, "event": job.event,
				"undelivered": undelivered, "reason": reason,
			}).Warn("webhook fan-out stopped before reaching every subscriber")
			return
		}
		delivery := webhookJob{workspaceID: job.workspaceID, event: job.event, body: job.body, target: &wh}
		if !s.enqueue(delivery) {
			// Fan-out overflow (or shutdown) is delivered by this worker
			// itself, within the job's deadline.
			s.deliverOne(ctx, wh, job.event, job.body)
		}
	}
}

func (s *WebhookService) jobTimeout() time.Duration {
	if s.pool.jobTimeout > 0 {
		return s.pool.jobTimeout
	}
	return webhookDeliveryTimeout
}

// WithJobTimeout overrides the per-job deadline (default 15s). It must be
// called before the first Dispatch.
func (s *WebhookService) WithJobTimeout(d time.Duration) *WebhookService {
	s.pool.jobTimeout = d
	return s
}

// Close stops accepting events and waits, until ctx ends, for queued and
// in-flight jobs to finish. When ctx ends first (or has already ended),
// in-flight deliveries are aborted and every job still queued, including the
// rest of a subscriber fan-out, is discarded without being run, so Close
// returns promptly after the deadline.
func (s *WebhookService) Close(ctx context.Context) error {
	p := &s.pool
	// A pool that never started must not start after Close.
	p.start.Do(func() {})
	p.mu.Lock()
	started := p.queue != nil
	if !p.closed {
		p.closed = true
		if started {
			close(p.queue)
		}
	}
	p.mu.Unlock()
	if !started {
		return nil
	}
	if ctx.Err() == nil {
		select {
		case <-p.stopped:
			return nil
		case <-ctx.Done():
		}
	}
	// Deadline reached: abort in-flight deliveries; workers discard the
	// remaining queue without running it.
	p.cancel()
	<-p.stopped
	return ctx.Err()
}

// HubListener returns a realtime.Hub broadcast hook that forwards every
// webhook-eligible workspace event to the subscribed outgoing webhooks.
func (s *WebhookService) HubListener() func(uuid.UUID, realtime.Event) {
	return func(workspaceID uuid.UUID, event realtime.Event) {
		if !webhookEvents[event.Type] {
			return
		}
		s.Dispatch(context.Background(), workspaceID, event.Type, event.Payload)
	}
}

// Dispatch queues an event for every active webhook of the workspace
// subscribed to it. Payloads are signed with HMAC-SHA256 in the
// X-Webhook-Signature header. It never blocks: delivery happens on the
// bounded worker pool, independent of ctx (a finished HTTP request must not
// abort it). When the queue is full the event is dropped, logged and
// counted in strifira_webhook_events_dropped_total{reason="queue_full"}.
func (s *WebhookService) Dispatch(_ context.Context, workspaceID uuid.UUID, event string, payload interface{}) {
	body, err := json.Marshal(map[string]interface{}{
		"event":     event,
		"payload":   payload,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		log.WithError(err).Warn("failed to marshal webhook payload")
		return
	}
	s.startPool()
	if !s.enqueue(webhookJob{workspaceID: workspaceID, event: event, body: body}) {
		reason := metrics.WebhookDropQueueFull
		if s.closing() {
			reason = metrics.WebhookDropShutdown
		}
		metrics.WebhookEventsDropped.WithLabelValues(reason).Inc()
		log.WithFields(log.Fields{"workspace_id": workspaceID, "event": event}).Warn("webhook queue full or closed; event dropped")
	}
}

func (s *WebhookService) deliverOne(ctx context.Context, w domain.Webhook, event string, body []byte) {
	secret, err := crypto.Decrypt(w.Secret, s.encryptionKey)
	if err != nil {
		log.WithError(err).WithField("webhook_id", w.ID).Warn("failed to decrypt webhook secret")
		return
	}

	signature := crypto.HMACSha256(body, secret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		log.WithError(err).WithField("webhook_id", w.ID).Warn("failed to create webhook request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", "sha256="+signature)
	req.Header.Set("X-Webhook-Event", event)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		log.WithError(err).WithField("webhook_id", w.ID).Warn("webhook delivery failed")
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.WithFields(log.Fields{"webhook_id": w.ID, "status": resp.StatusCode}).Warn("webhook delivery rejected")
	}
}
