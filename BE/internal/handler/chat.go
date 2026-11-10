package handler

import (
	"errors"
	"net/http"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/middleware"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/internal/service"
	"github.com/SephiraCS/strifira-backend/pkg/response"
	"github.com/SephiraCS/strifira-backend/pkg/validate"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type ChatHandler struct {
	chatSvc *service.ChatService
}

func NewChatHandler(chatSvc *service.ChatService) *ChatHandler {
	return &ChatHandler{chatSvc: chatSvc}
}

// validationDetails formats validator errors the way every handler reports
// them back to the client.
func validationDetails(err error) []dto.ErrorDetail {
	details := make([]dto.ErrorDetail, 0)
	for _, e := range validate.FormatErrors(err) {
		details = append(details, dto.ErrorDetail{Field: e["field"], Message: e["message"]})
	}
	return details
}

func (h *ChatHandler) ListChannels(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)

	channels, err := h.chatSvc.ListChannels(c.Request().Context(), ws.ID, userID)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}

	list := make([]dto.ChatChannelResponse, 0, len(channels))
	for _, channel := range channels {
		list = append(list, chatChannelResponse(channel))
	}
	return response.Success(c, http.StatusOK, dto.ChatChannelListResponse{Channels: list})
}

func (h *ChatHandler) CreateChannel(c echo.Context) error {
	var req dto.CreateChatChannelRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, validationDetails(err))
	}

	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)

	channel, err := h.chatSvc.CreateChannel(c.Request().Context(), ws.ID, userID, req)
	if err != nil {
		if errors.Is(err, service.ErrChatReservedName) {
			return response.Error(c, http.StatusUnprocessableEntity, "CHANNEL_NAME_RESERVED", "This channel name is reserved")
		}
		if errors.Is(err, repository.ErrChatChannelExists) {
			return response.Error(c, http.StatusConflict, "CHANNEL_EXISTS", "A channel with this name already exists")
		}
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusCreated, chatChannelResponse(*channel))
}

func (h *ChatHandler) EnsureDM(c echo.Context) error {
	var req dto.CreateChatDMRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, validationDetails(err))
	}

	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	peerID, err := uuid.Parse(req.UserID)
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid user ID")
	}

	channel, err := h.chatSvc.EnsureDM(c.Request().Context(), ws.ID, userID, peerID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrChatSelfDM):
			return response.Error(c, http.StatusUnprocessableEntity, "DM_SELF", "A direct message to yourself is not supported")
		case errors.Is(err, service.ErrChatUserNotMember):
			return response.NotFound(c, "Member")
		}
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusOK, chatChannelResponse(*channel))
}

func (h *ChatHandler) ListMessages(c echo.Context) error {
	channelID, err := uuid.Parse(c.Param("channelId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid channel ID")
	}

	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)

	messages, hasMore, err := h.chatSvc.ListMessages(c.Request().Context(), ws.ID, userID, channelID, c.QueryParam("before"))
	if err != nil {
		if errors.Is(err, service.ErrChatChannelForbidden) {
			return response.NotFound(c, "Channel")
		}
		return response.InternalErrorErr(c, err)
	}

	list := make([]dto.ChatMessageResponse, 0, len(messages))
	for _, message := range messages {
		list = append(list, chatMessageResponse(message))
	}
	return response.Success(c, http.StatusOK, dto.ChatMessageListResponse{Messages: list, HasMore: hasMore})
}

func (h *ChatHandler) CreateMessage(c echo.Context) error {
	channelID, err := uuid.Parse(c.Param("channelId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid channel ID")
	}
	var req dto.CreateChatMessageRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, validationDetails(err))
	}

	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)

	message, err := h.chatSvc.SendMessage(c.Request().Context(), ws.ID, userID, channelID, req)
	if err != nil {
		if errors.Is(err, service.ErrChatChannelForbidden) {
			return response.NotFound(c, "Channel")
		}
		if errors.Is(err, service.ErrChatEmptyMessage) {
			return response.Error(c, http.StatusUnprocessableEntity, "EMPTY_MESSAGE", "Message body cannot be empty")
		}
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusCreated, chatMessageResponse(*message))
}

func (h *ChatHandler) UpdateMessage(c echo.Context) error {
	messageID, err := uuid.Parse(c.Param("messageId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid message ID")
	}
	var req dto.UpdateChatMessageRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, validationDetails(err))
	}

	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)

	message, err := h.chatSvc.EditMessage(c.Request().Context(), ws.ID, userID, messageID, req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrChatMessageForbidden):
			return response.Forbidden(c)
		case errors.Is(err, service.ErrChatChannelForbidden), errors.Is(err, service.ErrChatMessageDeleted):
			return response.NotFound(c, "Message")
		case errors.Is(err, service.ErrChatEmptyMessage):
			return response.Error(c, http.StatusUnprocessableEntity, "EMPTY_MESSAGE", "Message body cannot be empty")
		}
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusOK, chatMessageResponse(*message))
}

func (h *ChatHandler) DeleteMessage(c echo.Context) error {
	messageID, err := uuid.Parse(c.Param("messageId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid message ID")
	}

	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)

	if err := h.chatSvc.DeleteMessage(c.Request().Context(), ws.ID, userID, messageID); err != nil {
		switch {
		case errors.Is(err, service.ErrChatMessageForbidden):
			return response.Forbidden(c)
		case errors.Is(err, service.ErrChatChannelForbidden), errors.Is(err, service.ErrChatMessageDeleted):
			return response.NotFound(c, "Message")
		}
		return response.InternalErrorErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// ListVoiceRooms returns the workspace's live voice rooms the caller can
// see. Membership is hub state (ephemeral); a room the reader cannot see is
// indistinguishable from an empty one.
func (h *ChatHandler) ListVoiceRooms(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)

	rooms, err := h.chatSvc.VoiceRooms(c.Request().Context(), ws.ID, userID)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusOK, dto.VoiceRoomListResponse{Rooms: rooms})
}

func chatChannelResponse(channel domain.ChatChannel) dto.ChatChannelResponse {
	var dmPeer *dto.ChatUser
	if channel.DMPeer != nil {
		dmPeer = &dto.ChatUser{
			ID:          channel.DMPeer.ID.String(),
			Name:        channel.DMPeer.Name,
			DisplayName: channel.DMPeer.DisplayName,
			Email:       channel.DMPeer.Email,
			AvatarURL:   channel.DMPeer.AvatarURL,
		}
	}
	return dto.ChatChannelResponse{
		ID:          channel.ID.String(),
		WorkspaceID: channel.WorkspaceID.String(),
		Type:        channel.Type,
		Name:        channel.Name,
		Topic:       channel.Topic,
		DMPeer:      dmPeer,
		CreatedBy:   channel.CreatedBy.String(),
		CreatedAt:   channel.CreatedAt,
		UpdatedAt:   channel.UpdatedAt,
		LastPostAt:  channel.LastPostAt,
	}
}

func chatMessageResponse(message domain.ChatMessage) dto.ChatMessageResponse {
	var author *dto.ChatUser
	if message.Author != nil {
		author = &dto.ChatUser{
			ID:          message.Author.ID.String(),
			Name:        message.Author.Name,
			DisplayName: message.Author.DisplayName,
			Email:       message.Author.Email,
			AvatarURL:   message.Author.AvatarURL,
		}
	}
	return dto.ChatMessageResponse{
		ID:        message.ID.String(),
		ChannelID: message.ChannelID.String(),
		UserID:    message.UserID.String(),
		Author:    author,
		Body:      message.Body,
		CreatedAt: message.CreatedAt,
		EditedAt:  message.EditedAt,
		DeletedAt: message.DeletedAt,
	}
}
