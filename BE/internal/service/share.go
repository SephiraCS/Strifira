package service

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/google/uuid"
)

var (
	ErrScreenShareNotFound      = errors.New("screen share not found")
	ErrScreenShareForbidden     = errors.New("only the presenter or a workspace admin can end this share")
	ErrScreenShareAlreadyActive = repository.ErrScreenShareAlreadyActive
)

// recentEndedLimit bounds the history section of the share list.
const recentEndedLimit = 20

// noSourceGraceSeconds is how long a share may stay active without a live
// presenter connection. The grace is measured from the LAST presenter
// contact (a presence announce; persisted in last_presenter_seen_at), so it
// covers presenter reloads, brief signaling drops, and backend restarts —
// not just the share's first 45 seconds of life.
const noSourceGraceSeconds = 45

// presenterClaimGraceSeconds is how long Create refuses to retire a previous
// source-less share of the same presenter. It only needs to span one
// workspace-socket reconnect cycle (the client's backoff ceiling is 15s):
// within it a live presenter elsewhere re-announces and the claim fails
// cleanly instead of stealing a working capture, while a presenter whose
// previous share died uncleanly (crash, reload, failed Stop) can start
// again after one cycle instead of waiting out the full GC grace above.
const presenterClaimGraceSeconds = 15

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

// Create starts a share for the capturing tab. An existing share belongs to
// its original tab and cannot be claimed by a second capture.
// Media is peer-to-peer WebRTC; the private token binds presence and Stop to
// the original capturing tab. Expired sources are retired before a new start.
func (s *ScreenShareService) Create(ctx context.Context, workspaceID, userID uuid.UUID, req dto.CreateScreenShareRequest) (*domain.ScreenShare, error) {
	existing, err := s.shareRepo.GetActiveByPresenter(ctx, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if err := s.retirePreviousCapture(ctx, existing); err != nil {
			return nil, err
		}
	}

	share := &domain.ScreenShare{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		StartedBy:      userID,
		Status:         domain.ScreenShareStatusActive,
		PresenterToken: uuid.New(),
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

func (s *ScreenShareService) retirePreviousCapture(ctx context.Context, previous *domain.ScreenShare) error {
	retired := false
	err := s.hub.WithShareLock(previous.ID, func() error {
		current, err := s.shareRepo.GetByID(ctx, previous.ID)
		if err != nil {
			return err
		}
		if current == nil || current.Status != domain.ScreenShareStatusActive {
			return nil
		}
		_, online := s.hub.ShareState(current.ID)
		if online || s.presenterUnreachableFor(*current) <= presenterClaimGraceSeconds*time.Second {
			return ErrScreenShareAlreadyActive
		}
		if err := s.shareRepo.MarkEnded(ctx, current.ID); err != nil {
			return err
		}
		s.hub.ClearShare(current.ID)
		retired = true
		return nil
	})
	if retired {
		s.broadcast(previous.WorkspaceID, "share.ended", map[string]string{"id": previous.ID.String()})
	}
	return err
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

	for i := range active {
		share := &active[i]
		var viewers int
		removed := false
		gcEnded := false
		if err := s.hub.WithShareLock(share.ID, func() error {
			// The list query precedes the lifecycle lock. An explicit End may
			// already have committed, so reconcile the row before treating it
			// as active or deciding whether to collect its presenter.
			current, err := s.shareRepo.GetByID(ctx, share.ID)
			if err != nil {
				return err
			}
			if current == nil || current.WorkspaceID != workspaceID || current.Status != domain.ScreenShareStatusActive {
				removed = true
				return nil
			}
			share = current
			var presenterOnline bool
			viewers, presenterOnline = s.hub.ShareState(share.ID)
			if presenterOnline || s.presenterUnreachableFor(*share) <= noSourceGraceSeconds*time.Second {
				return nil
			}
			if err := s.shareRepo.MarkEnded(ctx, share.ID); err != nil {
				return err
			}
			s.hub.ClearShare(share.ID)
			removed = true
			gcEnded = true
			return nil
		}); err != nil {
			return resp, err
		}
		if removed {
			if gcEnded {
				s.broadcast(share.WorkspaceID, "share.ended", map[string]string{"id": share.ID.String()})
			}
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
	endedIDs := make(map[string]bool, len(recent))
	for _, share := range recent {
		decorated, err := s.decorate(ctx, share, 0)
		if err != nil {
			return resp, err
		}
		resp.Recent = append(resp.Recent, decorated)
		endedIDs[decorated.ID] = true
	}
	// End may commit after an active row's lock is released and before the
	// recent query. The later ended snapshot wins within this response.
	resp.Active = slices.DeleteFunc(resp.Active, func(share dto.ScreenShareResponse) bool {
		return endedIDs[share.ID]
	})

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
func (s *ScreenShareService) End(ctx context.Context, workspaceID, shareID, userID, presenterToken uuid.UUID, force bool) (*domain.ScreenShare, error) {
	var share *domain.ScreenShare
	changed := false
	err := s.hub.WithShareLock(shareID, func() error {
		var err error
		share, err = s.shareRepo.GetByID(ctx, shareID)
		if err != nil {
			return err
		}
		if share == nil || share.WorkspaceID != workspaceID {
			return ErrScreenShareNotFound
		}
		if !force && (share.StartedBy != userID || presenterToken == uuid.Nil || share.PresenterToken != presenterToken) {
			return ErrScreenShareForbidden
		}
		if share.Status != domain.ScreenShareStatusActive {
			return nil
		}
		if err := s.shareRepo.MarkEnded(ctx, share.ID); err != nil {
			return err
		}
		s.hub.ClearShare(share.ID)
		changed = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if changed {
		s.broadcast(share.WorkspaceID, "share.ended", map[string]string{"id": share.ID.String()})
	}
	return share, nil
}

// AuthorizePresence binds signaling membership to an active share in this
// workspace. Only the capturing tab holds the token needed to present.
func (s *ScreenShareService) AuthorizePresence(ctx context.Context, workspaceID, userID, shareID uuid.UUID, role, token string) bool {
	share, err := s.shareRepo.GetByID(ctx, shareID)
	if err != nil || share == nil || share.WorkspaceID != workspaceID || share.Status != domain.ScreenShareStatusActive {
		return false
	}
	if role == "viewer" {
		return true
	}
	presenterToken, err := uuid.Parse(token)
	return role == "presenter" && err == nil && presenterToken != uuid.Nil && share.StartedBy == userID && share.PresenterToken == presenterToken
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
