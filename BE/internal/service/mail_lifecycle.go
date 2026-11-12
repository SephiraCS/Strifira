package service

import (
	"context"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/google/uuid"
)

type mailAccountOperations struct {
	removing bool
	cancels  map[*context.CancelFunc]struct{}
}

func (s *MailService) withAccountOperation(ctx context.Context, workspaceID, id uuid.UUID, fn func(context.Context, *domain.MailAccount) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.accountOpsMu.Lock()
	if s.accountOps == nil {
		s.accountOps = make(map[uuid.UUID]*mailAccountOperations)
	}
	ops := s.accountOps[id]
	if ops == nil {
		ops = &mailAccountOperations{cancels: make(map[*context.CancelFunc]struct{})}
		s.accountOps[id] = ops
	}
	if ops.removing {
		s.accountOpsMu.Unlock()
		return repository.ErrMailAccountNotFound
	}
	ops.cancels[&cancel] = struct{}{}
	s.accountOpsMu.Unlock()
	defer func() {
		s.accountOpsMu.Lock()
		defer s.accountOpsMu.Unlock()
		delete(ops.cancels, &cancel)
		if len(ops.cancels) == 0 && !ops.removing {
			delete(s.accountOps, id)
		}
	}()
	return s.accountRepo.WithAccountLock(ctx, workspaceID, id, false, func(account *domain.MailAccount) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn(ctx, account)
	})
}

func (s *MailService) syncAccount(ctx context.Context, account domain.MailAccount) error {
	return s.withAccountOperation(ctx, account.WorkspaceID, account.ID, func(ctx context.Context, current *domain.MailAccount) error {
		return s.syncLinkedAccount(ctx, *current)
	})
}

func (s *MailService) DeleteAccount(ctx context.Context, workspaceID, id uuid.UUID) error {
	// Check workspace ownership before cancelling any local operation.
	if _, err := s.accountRepo.GetByID(ctx, workspaceID, id); err != nil {
		return err
	}
	s.accountOpsMu.Lock()
	if s.accountOps == nil {
		s.accountOps = make(map[uuid.UUID]*mailAccountOperations)
	}
	ops := s.accountOps[id]
	if ops == nil {
		ops = &mailAccountOperations{cancels: make(map[*context.CancelFunc]struct{})}
		s.accountOps[id] = ops
	}
	if ops.removing {
		s.accountOpsMu.Unlock()
		return repository.ErrMailAccountConflict
	}
	ops.removing = true
	for cancel := range ops.cancels {
		(*cancel)()
	}
	s.accountOpsMu.Unlock()
	defer func() {
		s.accountOpsMu.Lock()
		defer s.accountOpsMu.Unlock()
		ops.removing = false
		if len(ops.cancels) == 0 {
			delete(s.accountOps, id)
		}
	}()
	err := s.accountRepo.WithAccountLock(ctx, workspaceID, id, true, func(account *domain.MailAccount) error {
		if account.AuthMethod == "oauth2" {
			return s.disconnectOAuth(ctx, account)
		}
		return s.accountRepo.Delete(ctx, workspaceID, id)
	})
	if err == nil {
		s.hub.Broadcast(workspaceID, realtime.Event{
			Type:    "mail.updated",
			Payload: map[string]any{"account_id": id.String(), "deleted": true},
		})
	}
	return err
}
