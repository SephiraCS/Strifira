package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"

	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/pkg/response"
	"github.com/ku/ku-backend/pkg/storage"
)

const maxAvatarSize = 5 * 1024 * 1024

var allowedAvatarTypes = map[string]map[string]bool{
	"image/jpeg":    {".jpg": true, ".jpeg": true},
	"image/png":     {".png": true},
	"image/gif":     {".gif": true},
	"image/webp":    {".webp": true},
	"image/avif":    {".avif": true},
	"image/bmp":     {".bmp": true},
	"image/svg+xml": {".svg": true},
}

type AvatarHandler struct {
	store      storage.Backend
	avatarRepo repository.UserAvatarRepo
	userRepo   repository.UserRepo
}

func NewAvatarHandler(store storage.Backend, avatarRepo repository.UserAvatarRepo, userRepo repository.UserRepo) *AvatarHandler {
	return &AvatarHandler{
		store:      store,
		avatarRepo: avatarRepo,
		userRepo:   userRepo,
	}
}

// Upload stores an image file as the authenticated user's avatar and points
// users.avatar_url at the served /api/avatars/<user_id> route. The ?v= query
// is bumped on every replacement so cached <img> entries refetch.
func (h *AvatarHandler) Upload(c echo.Context) error {
	userID := middleware.GetUserID(c)
	c.Request().Body = http.MaxBytesReader(c.Response().Writer, c.Request().Body, maxAvatarSize+1024*1024)

	file, err := c.FormFile("file")
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return response.Error(c, http.StatusRequestEntityTooLarge, "BAD_REQUEST", "File too large (max 5MB)")
		}
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "No file provided")
	}
	if file.Size > maxAvatarSize {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "File too large (max 5MB)")
	}

	src, err := file.Open()
	if err != nil {
		return response.InternalError(c)
	}
	defer src.Close()

	detected, err := mimetype.DetectReader(src)
	if err != nil {
		return response.InternalError(c)
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return response.InternalError(c)
	}

	ext := strings.ToLower(filepath.Ext(file.Filename))
	contentType, ok := allowedAvatarType(detected.String(), ext)
	if !ok {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Unsupported file type; use a PNG, JPEG, GIF, WebP, AVIF, BMP, or SVG image")
	}
	if ext == "" {
		ext = detected.Extension()
	}

	previous, err := h.avatarRepo.GetByUserID(c.Request().Context(), userID)
	if err != nil {
		return response.InternalError(c)
	}

	key := fmt.Sprintf("avatars/%s%s", uuid.New().String(), ext)
	if _, err := h.store.Put(c.Request().Context(), key, src, contentType); err != nil {
		return response.InternalError(c)
	}

	avatar := &domain.UserAvatar{
		ID:          uuid.New(),
		UserID:      userID,
		StorageKey:  key,
		Filename:    filepath.Base(file.Filename),
		ContentType: contentType,
		Size:        file.Size,
	}
	if err := h.avatarRepo.Upsert(c.Request().Context(), avatar); err != nil {
		return response.InternalError(c)
	}

	user, err := h.userRepo.GetByID(c.Request().Context(), userID)
	if err != nil || user == nil {
		return response.NotFound(c, "User")
	}
	avatarURL := avatarURLFor(user.ID, avatar.UpdatedAt)
	user.AvatarURL = &avatarURL
	if err := h.userRepo.Update(c.Request().Context(), user); err != nil {
		return response.InternalError(c)
	}

	if previous != nil && previous.StorageKey != key {
		if err := h.store.Delete(c.Request().Context(), previous.StorageKey); err != nil {
			log.WithFields(log.Fields{"event": "avatar.old_delete_failed", "key": previous.StorageKey}).Warn("failed to delete replaced avatar object")
		}
	}

	return response.Success(c, http.StatusOK, user)
}

// Remove deletes the authenticated user's uploaded avatar and clears
// users.avatar_url. Idempotent: removing with no uploaded avatar still
// succeeds.
func (h *AvatarHandler) Remove(c echo.Context) error {
	userID := middleware.GetUserID(c)

	avatar, err := h.avatarRepo.GetByUserID(c.Request().Context(), userID)
	if err != nil {
		return response.InternalError(c)
	}
	if avatar != nil {
		if err := h.avatarRepo.Delete(c.Request().Context(), userID); err != nil {
			return response.InternalError(c)
		}
		if err := h.store.Delete(c.Request().Context(), avatar.StorageKey); err != nil {
			log.WithFields(log.Fields{"event": "avatar.delete_failed", "key": avatar.StorageKey}).Warn("failed to delete avatar object")
		}
	}

	user, err := h.userRepo.GetByID(c.Request().Context(), userID)
	if err != nil || user == nil {
		return response.NotFound(c, "User")
	}
	user.AvatarURL = nil
	if err := h.userRepo.Update(c.Request().Context(), user); err != nil {
		return response.InternalError(c)
	}

	return response.Success(c, http.StatusOK, user)
}

// Get streams a user's uploaded avatar. It is intentionally public: avatars
// render on public shared views (which carry no session), and users can
// already point avatar_url at arbitrary public URLs.
func (h *AvatarHandler) Get(c echo.Context) error {
	userID, err := uuid.Parse(c.Param("userId"))
	if err != nil {
		return response.NotFound(c, "Avatar")
	}

	avatar, err := h.avatarRepo.GetByUserID(c.Request().Context(), userID)
	if err != nil || avatar == nil {
		return response.NotFound(c, "Avatar")
	}

	rc, err := h.store.Get(c.Request().Context(), avatar.StorageKey)
	if err != nil {
		return response.NotFound(c, "Avatar")
	}
	defer rc.Close()

	// SVGs can embed scripts; restrict the document to a scriptless profile
	// so direct navigation to an avatar cannot execute same-origin code.
	c.Response().Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	c.Response().Header().Set("Cache-Control", "public, max-age=86400")
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	return c.Stream(http.StatusOK, avatar.ContentType, rc)
}

func allowedAvatarType(detectedType, ext string) (string, bool) {
	extensions, ok := allowedAvatarTypes[detectedType]
	if !ok {
		return "", false
	}
	if ext != "" && !extensions[ext] {
		return "", false
	}
	return detectedType, true
}

func avatarURLFor(userID uuid.UUID, version time.Time) string {
	return fmt.Sprintf("/api/avatars/%s?v=%d", userID, version.Unix())
}
