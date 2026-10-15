package handler

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/middleware"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/pkg/response"
	"github.com/SephiraCS/strifira-backend/pkg/storage"
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
	store         storage.Backend
	avatarRepo    repository.UserAvatarRepo
	userRepo      repository.UserRepo
	wsAvatarRepo  repository.WorkspaceAvatarRepo
	workspaceRepo repository.WorkspaceRepo
}

func NewAvatarHandler(
	store storage.Backend,
	avatarRepo repository.UserAvatarRepo,
	userRepo repository.UserRepo,
	wsAvatarRepo repository.WorkspaceAvatarRepo,
	workspaceRepo repository.WorkspaceRepo,
) *AvatarHandler {
	return &AvatarHandler{
		store:         store,
		avatarRepo:    avatarRepo,
		userRepo:      userRepo,
		wsAvatarRepo:  wsAvatarRepo,
		workspaceRepo: workspaceRepo,
	}
}

// Upload stores an image file as the authenticated user's avatar and points
// users.avatar_url at the served /api/avatars/<user_id> route. The ?v= query
// is bumped on every replacement so cached <img> entries refetch.
func (h *AvatarHandler) Upload(c echo.Context) error {
	userID := middleware.GetUserID(c)

	upload, err := readAvatarUpload(c)
	if err != nil {
		return respondAvatarUploadError(c, err)
	}
	defer upload.file.Close()

	previous, err := h.avatarRepo.GetByUserID(c.Request().Context(), userID)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}

	key := fmt.Sprintf("avatars/%s.%s", uuid.New().String(), strings.TrimPrefix(extOf(upload.filename, upload.contentType), "."))
	if err := h.storePut(c, key, upload.file, upload.contentType); err != nil {
		return response.InternalErrorErr(c, err)
	}

	avatar := &domain.UserAvatar{
		ID:          uuid.New(),
		UserID:      userID,
		StorageKey:  key,
		Filename:    filepath.Base(upload.filename),
		ContentType: upload.contentType,
		Size:        upload.size,
	}
	if err := h.avatarRepo.Upsert(c.Request().Context(), avatar); err != nil {
		return response.InternalErrorErr(c, err)
	}

	user, err := h.userRepo.GetByID(c.Request().Context(), userID)
	if err != nil || user == nil {
		return response.NotFound(c, "User")
	}
	avatarURL := avatarURLFor(user.ID, avatar.UpdatedAt)
	user.AvatarURL = &avatarURL
	if err := h.userRepo.Update(c.Request().Context(), user); err != nil {
		return response.InternalErrorErr(c, err)
	}

	h.deletePreviousObject(c, previous != nil, func() string { return previous.StorageKey }, key)
	return response.Success(c, http.StatusOK, user)
}

// Remove deletes the authenticated user's uploaded avatar and clears
// users.avatar_url. Idempotent: removing with no uploaded avatar still
// succeeds.
func (h *AvatarHandler) Remove(c echo.Context) error {
	userID := middleware.GetUserID(c)

	avatar, err := h.avatarRepo.GetByUserID(c.Request().Context(), userID)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	if avatar != nil {
		if err := h.avatarRepo.Delete(c.Request().Context(), userID); err != nil {
			return response.InternalErrorErr(c, err)
		}
		h.deleteStorageObject(c, avatar.StorageKey)
	}

	user, err := h.userRepo.GetByID(c.Request().Context(), userID)
	if err != nil || user == nil {
		return response.NotFound(c, "User")
	}
	user.AvatarURL = nil
	if err := h.userRepo.Update(c.Request().Context(), user); err != nil {
		return response.InternalErrorErr(c, err)
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
	return h.streamAvatar(c, avatar.StorageKey, avatar.ContentType)
}

// UploadWorkspace stores an image file as the workspace logo and points
// workspaces.logo_url at the served /api/workspace-avatars/<id> route.
// Owner-only (enforced by route middleware, like PATCH workspace).
func (h *AvatarHandler) UploadWorkspace(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)

	upload, err := readAvatarUpload(c)
	if err != nil {
		return respondAvatarUploadError(c, err)
	}
	defer upload.file.Close()

	previous, err := h.wsAvatarRepo.GetByWorkspaceID(c.Request().Context(), ws.ID)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}

	key := fmt.Sprintf("workspace-avatars/%s.%s", uuid.New().String(), strings.TrimPrefix(extOf(upload.filename, upload.contentType), "."))
	if err := h.storePut(c, key, upload.file, upload.contentType); err != nil {
		return response.InternalErrorErr(c, err)
	}

	avatar := &domain.WorkspaceAvatar{
		ID:          uuid.New(),
		WorkspaceID: ws.ID,
		StorageKey:  key,
		Filename:    filepath.Base(upload.filename),
		ContentType: upload.contentType,
		Size:        upload.size,
	}
	if err := h.wsAvatarRepo.Upsert(c.Request().Context(), avatar); err != nil {
		return response.InternalErrorErr(c, err)
	}

	logoURL := workspaceLogoURLFor(ws.ID, avatar.UpdatedAt)
	ws.LogoURL = &logoURL
	if err := h.workspaceRepo.Update(c.Request().Context(), ws); err != nil {
		return response.InternalErrorErr(c, err)
	}

	h.deletePreviousObject(c, previous != nil, func() string { return previous.StorageKey }, key)
	return response.Success(c, http.StatusOK, ws)
}

// RemoveWorkspace deletes the workspace's uploaded logo and clears
// workspaces.logo_url. Idempotent.
func (h *AvatarHandler) RemoveWorkspace(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)

	avatar, err := h.wsAvatarRepo.GetByWorkspaceID(c.Request().Context(), ws.ID)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	if avatar != nil {
		if err := h.wsAvatarRepo.Delete(c.Request().Context(), ws.ID); err != nil {
			return response.InternalErrorErr(c, err)
		}
		h.deleteStorageObject(c, avatar.StorageKey)
	}

	ws.LogoURL = nil
	if err := h.workspaceRepo.Update(c.Request().Context(), ws); err != nil {
		return response.InternalErrorErr(c, err)
	}

	return response.Success(c, http.StatusOK, ws)
}

// GetWorkspace streams a workspace's uploaded logo (public, mirroring user
// avatars, so logos render wherever the workspace is named).
func (h *AvatarHandler) GetWorkspace(c echo.Context) error {
	workspaceID, err := uuid.Parse(c.Param("workspaceId"))
	if err != nil {
		return response.NotFound(c, "Avatar")
	}

	avatar, err := h.wsAvatarRepo.GetByWorkspaceID(c.Request().Context(), workspaceID)
	if err != nil || avatar == nil {
		return response.NotFound(c, "Avatar")
	}
	return h.streamAvatar(c, avatar.StorageKey, avatar.ContentType)
}

func (h *AvatarHandler) storePut(c echo.Context, key string, file multipart.File, contentType string) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err := h.store.Put(c.Request().Context(), key, file, contentType)
	return err
}

func (h *AvatarHandler) deletePreviousObject(c echo.Context, hasPrevious bool, previousKey func() string, newKey string) {
	if !hasPrevious || previousKey() == newKey {
		return
	}
	h.deleteStorageObject(c, previousKey())
}

func (h *AvatarHandler) deleteStorageObject(c echo.Context, key string) {
	if err := h.store.Delete(c.Request().Context(), key); err != nil {
		log.WithFields(log.Fields{"event": "avatar.delete_failed", "key": key}).Warn("failed to delete avatar object")
	}
}

func (h *AvatarHandler) streamAvatar(c echo.Context, storageKey, contentType string) error {
	rc, err := h.store.Get(c.Request().Context(), storageKey)
	if err != nil {
		return response.NotFound(c, "Avatar")
	}
	defer rc.Close()

	// SVGs can embed scripts; restrict the document to a scriptless profile
	// so direct navigation to an avatar cannot execute same-origin code.
	c.Response().Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	c.Response().Header().Set("Cache-Control", "public, max-age=86400")
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	return c.Stream(http.StatusOK, contentType, rc)
}

// avatarUpload is the validated result of readAvatarUpload.
type avatarUpload struct {
	file        multipart.File
	filename    string
	contentType string
	size        int64
}

// avatarValidationError marks a rejected upload (bad request semantics);
// respondAvatarUploadError turns it into the JSON error response.
type avatarValidationError struct {
	status  int
	message string
}

func (e *avatarValidationError) Error() string { return e.message }

func respondAvatarUploadError(c echo.Context, err error) error {
	var ve *avatarValidationError
	if errors.As(err, &ve) {
		return response.Error(c, ve.status, "BAD_REQUEST", ve.message)
	}
	return response.InternalErrorErr(c, err)
}

// readAvatarUpload performs the shared request-side validation for avatar
// uploads: body size cap, form file presence, magic-byte MIME detection and
// the image allowlist. On success the caller owns closing upload.file.
func readAvatarUpload(c echo.Context) (avatarUpload, error) {
	c.Request().Body = http.MaxBytesReader(c.Response().Writer, c.Request().Body, maxAvatarSize+1024*1024)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return avatarUpload{}, &avatarValidationError{status: http.StatusRequestEntityTooLarge, message: "File too large (max 5MB)"}
		}
		return avatarUpload{}, &avatarValidationError{status: http.StatusBadRequest, message: "No file provided"}
	}
	if fileHeader.Size > maxAvatarSize {
		return avatarUpload{}, &avatarValidationError{status: http.StatusBadRequest, message: "File too large (max 5MB)"}
	}

	file, err := fileHeader.Open()
	if err != nil {
		return avatarUpload{}, err
	}

	detected, err := mimetype.DetectReader(file)
	if err != nil {
		file.Close()
		return avatarUpload{}, err
	}

	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
	contentType, ok := allowedAvatarType(detected.String(), ext)
	if !ok {
		file.Close()
		return avatarUpload{}, &avatarValidationError{status: http.StatusBadRequest, message: "Unsupported file type; use a PNG, JPEG, GIF, WebP, AVIF, BMP, or SVG image"}
	}
	return avatarUpload{file: file, filename: fileHeader.Filename, contentType: contentType, size: fileHeader.Size}, nil
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

// extOf returns the filename extension when it is present, falling back to
// the detected type's canonical extension.
func extOf(filename, contentType string) string {
	if ext := strings.ToLower(filepath.Ext(filename)); ext != "" {
		return ext
	}
	return mimetype.Lookup(contentType).Extension()
}

func avatarURLFor(userID uuid.UUID, version time.Time) string {
	return fmt.Sprintf("/api/avatars/%s?v=%d", userID, version.Unix())
}

func workspaceLogoURLFor(workspaceID uuid.UUID, version time.Time) string {
	return fmt.Sprintf("/api/workspace-avatars/%s?v=%d", workspaceID, version.Unix())
}
