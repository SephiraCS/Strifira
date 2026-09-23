package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/realtime"
	"github.com/ku/ku-backend/internal/repository"
)

var (
	ErrScreenShareNotFound  = errors.New("screen share not found")
	ErrScreenShareForbidden = errors.New("only the presenter or a workspace admin can end this share")
)

// recentEndedLimit bounds the history section of the share list.
const recentEndedLimit = 20

// noSourceGraceSeconds is how long a share may stay active without a live
// presenter connection. The grace is measured from the LAST presenter
// contact (a presence announce; persisted in last_presenter_seen_at), so it
// covers presenter reloads, brief signaling drops, and backend restarts —
// not just the share's first 45 seconds of life.
const noSourceGraceSeconds = 45

type ScreenShareService struct {
	shareRepo repository.ScreenShareRepo
	userRepo  repository.UserRepo
	hub       *realtime.Hub
}

func NewScreenShareService(shareRepo repository.ScreenShareRepo, userRepo repository.UserRepo, hub *realtime.Hub) *ScreenShareService {
	return &ScreenShareService{
		shareRepo: shareRepo,
		userRepo:  userRepo,
		hub:       hub,
	}
}

// Create starts a share for the requesting user, or returns their existing
// active share in the workspace (idempotent for reloads and double-clicks).
// Media is peer-to-peer WebRTC; the share record only anchors presence and
// lifecycle — no server-side room or token exists.
func (s *ScreenShareService) Create(ctx context.Context, workspaceID, userID uuid.UUID, req dto.CreateScreenShareRequest) (*domain.ScreenShare, error) {
	existing, err := s.shareRepo.GetActiveByPresenter(ctx, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	share := &domain.ScreenShare{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		StartedBy:   userID,
		Status:      domain.ScreenShareStatusActive,
	}
	if err := s.shareRepo.Create(ctx, share); err != nil {
		return nil, err
	}

	s.broadcast(workspaceID, "share.started", map[string]string{
		"id":         share.ID.String(),
		"started_by": share.StartedBy.String(),
	})

	return share, nil
}

// List returns active shares — lazily ending any whose presenter has been
// offline past the reload grace period (no source) — plus a bounded history
// of ended shares, both decorated with presenter info and live viewer
// counts from the signaling hub.
func (s *ScreenShareService) List(ctx context.Context, workspaceID uuid.UUID) (dto.ScreenShareListResponse, error) {
	resp := dto.ScreenShareListResponse{
		Active: []dto.ScreenShareResponse{},
		Recent: []dto.ScreenShareResponse{},
	}

	active, err := s.shareRepo.ListActiveByWorkspace(ctx, workspaceID)
	if err != nil {
		return resp, err
	}

	var ended []*domain.ScreenShare
	for i := range active {
		share := &active[i]
		viewers, presenterOnline := s.hub.ShareState(share.ID)
		if !presenterOnline && s.presenterUnreachableFor(*share) > noSourceGraceSeconds*time.Second {
			// The presenter has been unreachable for longer than a reload
			// or reconnect takes; the share has no source and must not
			// linger on the wall. Drop the membership state and close the
			// share.
			s.hub.ClearShare(share.ID)
			if err := s.shareRepo.MarkEnded(ctx, share.ID); err != nil {
				return resp, err
			}
			ended = append(ended, share)
			// Parked viewers (and the presenter's own tile) learn the share
			// is over instead of waiting for their next list load.
			s.broadcast(share.WorkspaceID, "share.ended", map[string]string{
				"id": share.ID.String(),
			})
			continue
		}
		decorated, err := s.decorate(ctx, *share, viewers)
		if err != nil {
			return resp, err
		}
		resp.Active = append(resp.Active, decorated)
	}

	recent, err := s.shareRepo.ListRecentEndedByWorkspace(ctx, workspaceID, recentEndedLimit)
	if err != nil {
		return resp, err
	}
	for _, share := range ended {
		decorated, err := s.decorate(ctx, *share, 0)
		if err != nil {
			return resp, err
		}
		resp.Recent = append([]dto.ScreenShareResponse{decorated}, resp.Recent...)
	}
	for _, share := range recent {
		decorated, err := s.decorate(ctx, share, 0)
		if err != nil {
			return resp, err
		}
		resp.Recent = append(resp.Recent, decorated)
	}

	return resp, nil
}

// TouchPresenterSeen records presenter contact for the share (wired to the
// hub's OnPresenterSeen callback) so the GC grace survives restarts.
func (s *ScreenShareService) TouchPresenterSeen(ctx context.Context, shareID uuid.UUID) {
	if share, err := s.shareRepo.GetByID(ctx, shareID); err == nil && share != nil && share.Status == domain.ScreenShareStatusActive {
		_ = s.shareRepo.TouchPresenterSeen(ctx, shareID)
	}
}

// presenterUnreachableFor measures how long the share's presenter has been
// without contact: the freshest of the persisted timestamp, the hub's
// in-memory sighting, and the hub process start (a restart grants every
// live share a fresh grace window to be re-announced).
func (s *ScreenShareService) presenterUnreachableFor(share domain.ScreenShare) time.Duration {
	lastSeen := share.LastPresenterSeenAt
	if t, ok := s.hub.PresenterSeenAt(share.ID); ok && t.After(lastSeen) {
		lastSeen = t
	}
	if start := s.hub.StartedAt(); start.After(lastSeen) {
		lastSeen = start
	}
	return time.Since(lastSeen)
}

// Get returns a single share for deep links; nil when unknown.
func (s *ScreenShareService) Get(ctx context.Context, id uuid.UUID) (*domain.ScreenShare, error) {
	return s.shareRepo.GetByID(ctx, id)
}

// End closes the share. The presenter may end their own share; workspace
// admins may end any (force). Viewers learn through the share.ended event;
// peer connections collapse on their own once signaling stops.
func (s *ScreenShareService) End(ctx context.Context, shareID, userID uuid.UUID, force bool) (*domain.ScreenShare, error) {
	share, err := s.shareRepo.GetByID(ctx, shareID)
	if err != nil {
		return nil, err
	}
	if share == nil {
		return nil, ErrScreenShareNotFound
	}
	if share.Status != domain.ScreenShareStatusActive {
		return share, nil
	}
	if share.StartedBy != userID && !force {
		return nil, ErrScreenShareForbidden
	}

	s.hub.ClearShare(share.ID)
	if err := s.shareRepo.MarkEnded(ctx, share.ID); err != nil {
		return nil, err
	}

	s.broadcast(share.WorkspaceID, "share.ended", map[string]string{
		"id": share.ID.String(),
	})

	return share, nil
}

func (s *ScreenShareService) decorate(ctx context.Context, share domain.ScreenShare, viewerCount int) (dto.ScreenShareResponse, error) {
	resp := dto.ScreenShareResponse{
		ID:          share.ID.String(),
		WorkspaceID: share.WorkspaceID.String(),
		StartedBy:   share.StartedBy.String(),
		Status:      share.Status,
		ViewerCount: viewerCount,
		CreatedAt:   share.CreatedAt,
		EndedAt:     share.EndedAt,
	}
	if user, err := s.userRepo.GetByID(ctx, share.StartedBy); err == nil && user != nil {
		resp.StartedByUser = &dto.ScreenShareUser{
			ID:          user.ID.String(),
			Name:        user.Name,
			DisplayName: user.DisplayName,
			AvatarURL:   user.AvatarURL,
		}
	}
	return resp, nil
}

func (s *ScreenShareService) broadcast(workspaceID uuid.UUID, eventType string, payload map[string]string) {
	if s.hub == nil {
		return
	}
	s.hub.Broadcast(workspaceID, realtime.Event{
		Type:    eventType,
		Payload: payload,
	})
}
