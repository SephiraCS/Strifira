package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/livekit"
	"github.com/ku/ku-backend/internal/realtime"
	"github.com/ku/ku-backend/internal/repository"
)

var (
	ErrScreenSharingDisabled = errors.New("screen sharing is not configured on this instance")
	ErrScreenShareNotFound   = errors.New("screen share not found")
	ErrScreenShareForbidden  = errors.New("only the presenter or a workspace admin can end this share")
)

// recentEndedLimit bounds the history section of the share list.
const recentEndedLimit = 20

// LiveKitGateway is the LiveKit surface the share service depends on.
// A nil gateway leaves screen sharing disabled.
type LiveKitGateway interface {
	ServerURL() string
	PresenterToken(room, identity, name string) (string, error)
	ViewerToken(room, identity, name string) (string, error)
	ViewerCount(ctx context.Context, room, presenterIdentity string) (int, error)
	DeleteRoom(ctx context.Context, room string) error
}

type ScreenShareService struct {
	shareRepo repository.ScreenShareRepo
	userRepo  repository.UserRepo
	hub       *realtime.Hub
	client    LiveKitGateway
	enabled   bool
}

func NewScreenShareService(shareRepo repository.ScreenShareRepo, userRepo repository.UserRepo, hub *realtime.Hub, client LiveKitGateway) *ScreenShareService {
	return &ScreenShareService{
		shareRepo: shareRepo,
		userRepo:  userRepo,
		hub:       hub,
		client:    client,
		enabled:   client != nil,
	}
}

// Create starts a share for the requesting user, or returns their existing
// active share in the workspace (idempotent for reloads and double-clicks).
func (s *ScreenShareService) Create(ctx context.Context, workspaceID, userID uuid.UUID, req dto.CreateScreenShareRequest) (*domain.ScreenShare, string, error) {
	if !s.enabled {
		return nil, "", ErrScreenSharingDisabled
	}

	existing, err := s.shareRepo.GetActiveByPresenter(ctx, workspaceID, userID)
	if err != nil {
		return nil, "", err
	}
	if existing != nil {
		token, err := s.presenterToken(ctx, existing, userID)
		if err != nil {
			return nil, "", err
		}
		return existing, token, nil
	}

	share := &domain.ScreenShare{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		StartedBy:   userID,
		RoomName:    fmt.Sprintf("ku-%s", shareRoomID(workspaceID)),
		Status:      domain.ScreenShareStatusActive,
	}
	if err := s.shareRepo.Create(ctx, share); err != nil {
		return nil, "", err
	}

	token, err := s.presenterToken(ctx, share, userID)
	if err != nil {
		return nil, "", err
	}

	s.broadcast(workspaceID, "share.started", map[string]string{
		"id":         share.ID.String(),
		"started_by": share.StartedBy.String(),
	})

	return share, token, nil
}

// List returns active shares (lazily ending any whose LiveKit room is gone)
// plus a bounded history of ended shares, both decorated with presenter
// info and live viewer counts.
func (s *ScreenShareService) List(ctx context.Context, workspaceID uuid.UUID) (dto.ScreenShareListResponse, error) {
	resp := dto.ScreenShareListResponse{
		Active: []dto.ScreenShareResponse{},
		Recent: []dto.ScreenShareResponse{},
	}
	if !s.enabled {
		return resp, nil
	}

	active, err := s.shareRepo.ListActiveByWorkspace(ctx, workspaceID)
	if err != nil {
		return resp, err
	}

	var ended []*domain.ScreenShare
	for i := range active {
		share := &active[i]
		count, err := s.client.ViewerCount(ctx, share.RoomName, share.StartedBy.String())
		if err != nil {
			if livekit.IsRoomGone(err) {
				// Room vanished (presenter dropped and LiveKit reaped the
				// empty room) — close the share on our side too.
				if err := s.shareRepo.MarkEnded(ctx, share.ID); err != nil {
					return resp, err
				}
				ended = append(ended, share)
				continue
			}
			// LiveKit unreachable: keep the share listed, report zero
			// viewers rather than failing the whole list.
			count = 0
		}
		decorated, err := s.decorate(ctx, *share, count)
		if err != nil {
			return resp, err
		}
		resp.Active = append(resp.Active, decorated)
	}

	recent, err := s.shareRepo.ListRecentEndedByWorkspace(ctx, workspaceID, recentEndedLimit)
	if err != nil {
		return resp, err
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

// Get returns a single share for deep links; nil when unknown.
func (s *ScreenShareService) Get(ctx context.Context, id uuid.UUID) (*domain.ScreenShare, error) {
	return s.shareRepo.GetByID(ctx, id)
}

// JoinToken returns a LiveKit token for joining the share: the presenter
// gets a publish token, everyone else a subscribe-only token.
func (s *ScreenShareService) JoinToken(ctx context.Context, workspaceID, shareID, userID uuid.UUID) (*domain.ScreenShare, string, error) {
	if !s.enabled {
		return nil, "", ErrScreenSharingDisabled
	}
	share, err := s.shareRepo.GetByID(ctx, shareID)
	if err != nil {
		return nil, "", err
	}
	if share == nil || share.WorkspaceID != workspaceID {
		return nil, "", ErrScreenShareNotFound
	}
	if share.Status != domain.ScreenShareStatusActive {
		return nil, "", ErrScreenShareNotFound
	}

	var token string
	if share.StartedBy == userID {
		token, err = s.presenterToken(ctx, share, userID)
	} else {
		token, err = s.viewerToken(ctx, share, userID)
	}
	if err != nil {
		return nil, "", err
	}
	return share, token, nil
}

// End tears down the LiveKit room and closes the share. The presenter may
// end their own share; workspace admins may end any (force).
func (s *ScreenShareService) End(ctx context.Context, shareID, userID uuid.UUID, force bool) (*domain.ScreenShare, error) {
	if !s.enabled {
		return nil, ErrScreenSharingDisabled
	}
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

	if err := s.client.DeleteRoom(ctx, share.RoomName); err != nil {
		return nil, fmt.Errorf("deleting livekit room: %w", err)
	}
	if err := s.shareRepo.MarkEnded(ctx, share.ID); err != nil {
		return nil, err
	}

	s.broadcast(share.WorkspaceID, "share.ended", map[string]string{
		"id": share.ID.String(),
	})

	return share, nil
}

func (s *ScreenShareService) presenterToken(ctx context.Context, share *domain.ScreenShare, userID uuid.UUID) (string, error) {
	identity, name, err := s.identity(ctx, userID)
	if err != nil {
		return "", err
	}
	return s.client.PresenterToken(share.RoomName, identity, name)
}

func (s *ScreenShareService) viewerToken(ctx context.Context, share *domain.ScreenShare, userID uuid.UUID) (string, error) {
	identity, name, err := s.identity(ctx, userID)
	if err != nil {
		return "", err
	}
	return s.client.ViewerToken(share.RoomName, identity, name)
}

func (s *ScreenShareService) identity(ctx context.Context, userID uuid.UUID) (string, string, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return "", "", err
	}
	name := user.DisplayName
	if name == "" {
		name = user.Name
	}
	return user.ID.String(), name, nil
}

func (s *ScreenShareService) decorate(ctx context.Context, share domain.ScreenShare, viewerCount int) (dto.ScreenShareResponse, error) {
	resp := dto.ScreenShareResponse{
		ID:          share.ID.String(),
		WorkspaceID: share.WorkspaceID.String(),
		StartedBy:   share.StartedBy.String(),
		RoomName:    share.RoomName,
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

// shareRoomID derives a short unique room suffix: enough workspace prefix
// to be recognizable in LiveKit tooling plus fresh randomness. The database
// unique constraint on room_name is the real guard.
func shareRoomID(workspaceID uuid.UUID) string {
	return fmt.Sprintf("%s%s", workspaceID.String()[:8], uuid.New().String()[:8])
}
