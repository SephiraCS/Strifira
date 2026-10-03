package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/realtime"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/pkg/crypto"
	"github.com/ku/ku-backend/pkg/validate"
	"github.com/lib/pq"
	log "github.com/sirupsen/logrus"
)

type WebhookService struct {
	webhookRepo   *repository.WebhookRepository
	encryptionKey []byte
}

func NewWebhookService(webhookRepo *repository.WebhookRepository, jwtSecret string) *WebhookService {
	return &WebhookService{
		webhookRepo:   webhookRepo,
		encryptionKey: crypto.DeriveKey(jwtSecret + ":webhook"),
	}
}

func (s *WebhookService) Create(ctx context.Context, workspaceID uuid.UUID, req dto.CreateWebhookRequest) (*domain.Webhook, error) {
	if err := validate.ValidateWebhookURL(req.URL); err != nil {
		return nil, fmt.Errorf("invalid webhook URL: %w", err)
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
			return nil, fmt.Errorf("invalid webhook URL: %w", err)
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

// webhookDeliveryTimeout bounds one dispatch (listing subscribers plus every
// delivery). Dispatch runs detached from the triggering request.
const webhookDeliveryTimeout = 15 * time.Second

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

// Dispatch sends an event payload to all active webhooks subscribed to the event.
// Payloads are signed with HMAC-SHA256 in the X-Webhook-Signature header.
// It returns immediately: delivery happens in the background on a context
// detached from ctx's cancellation (a finished HTTP request must not abort
// it) and bounded by webhookDeliveryTimeout.
func (s *WebhookService) Dispatch(ctx context.Context, workspaceID uuid.UUID, event string, payload interface{}) {
	body, err := json.Marshal(map[string]interface{}{
		"event":     event,
		"payload":   payload,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		log.WithError(err).Warn("failed to marshal webhook payload")
		return
	}
	detached := context.WithoutCancel(ctx)
	go s.deliver(detached, workspaceID, event, body)
}

func (s *WebhookService) deliver(parent context.Context, workspaceID uuid.UUID, event string, body []byte) {
	ctx, cancel := context.WithTimeout(parent, webhookDeliveryTimeout)
	defer cancel()

	webhooks, err := s.webhookRepo.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		log.WithError(err).Warn("failed to list webhooks for dispatch")
		return
	}

	var wg sync.WaitGroup
	for _, wh := range webhooks {
		if !wh.IsActive || !slices.Contains([]string(wh.Events), event) {
			continue
		}
		wg.Add(1)
		go func(w domain.Webhook) {
			defer wg.Done()
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

			resp, err := webhookHTTPClient.Do(req)
			if err != nil {
				log.WithError(err).WithField("webhook_id", w.ID).Warn("webhook delivery failed")
				return
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				log.WithFields(log.Fields{"webhook_id": w.ID, "status": resp.StatusCode}).Warn("webhook delivery rejected")
			}
		}(wh)
	}
	wg.Wait()
}

var webhookHTTPClient = &http.Client{Timeout: 10 * time.Second}
