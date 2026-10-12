package handler

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"

	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/pkg/assettoken"
	"github.com/ku/ku-backend/pkg/response"
	"github.com/ku/ku-backend/pkg/storage"
)

const maxUploadSize = 10 * 1024 * 1024

var allowedUploadExtensions = map[string]map[string]bool{
	"image/jpeg":    {".jpg": true, ".jpeg": true},
	"image/png":     {".png": true},
	"image/gif":     {".gif": true},
	"image/webp":    {".webp": true},
	"image/svg+xml": {".svg": true},

	"application/pdf":  {".pdf": true},
	"text/plain":       {".txt": true, ".log": true},
	"text/csv":         {".csv": true},
	"text/markdown":    {".md": true, ".markdown": true},
	"application/json": {".json": true},
	"text/rtf":         {".rtf": true},

	"application/msword": {".doc": true},
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": {".docx": true},
	"application/vnd.ms-excel": {".xls": true},
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         {".xlsx": true},
	"application/vnd.ms-powerpoint":                                             {".ppt": true},
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": {".pptx": true},

	"application/zip":             {".zip": true},
	"application/gzip":            {".gz": true, ".gzip": true},
	"application/x-tar":           {".tar": true},
	"application/x-7z-compressed": {".7z": true},
	"application/vnd.rar":         {".rar": true},
}

type UploadHandler struct {
	store           storage.Backend
	assetRepo       repository.AssetRepo
	issueRepo       repository.IssueRepo
	tokenSecret     string
	fileShareSecret string
}

func NewUploadHandler(store storage.Backend, assetRepo repository.AssetRepo, issueRepo repository.IssueRepo, jwtSecret string) *UploadHandler {
	return &UploadHandler{
		store:           store,
		assetRepo:       assetRepo,
		issueRepo:       issueRepo,
		tokenSecret:     jwtSecret + ":prompt-assets",
		fileShareSecret: jwtSecret + ":file-share",
	}
}

func (h *UploadHandler) Upload(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	c.Request().Body = http.MaxBytesReader(c.Response().Writer, c.Request().Body, maxUploadSize+1024*1024)

	file, err := c.FormFile("file")
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return response.Error(c, http.StatusRequestEntityTooLarge, "BAD_REQUEST", "File too large (max 10MB)")
		}
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "No file provided")
	}

	// Validate file size (max 10MB)
	if file.Size > maxUploadSize {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "File too large (max 10MB)")
	}

	src, err := file.Open()
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	defer src.Close()

	detected, err := mimetype.DetectReader(src)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return response.InternalErrorErr(c, err)
	}

	ext := strings.ToLower(filepath.Ext(file.Filename))
	contentType, ok := allowedUploadType(detected.String(), ext)
	if !ok {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Unsupported file type")
	}

	// Generate unique filename
	if ext == "" {
		ext = detected.Extension()
	}
	key := fmt.Sprintf("%s%s", uuid.New().String(), ext)

	if _, err := h.store.Put(c.Request().Context(), key, src, contentType); err != nil {
		return response.InternalErrorErr(c, err)
	}

	asset := &domain.Asset{
		ID:          uuid.New(),
		WorkspaceID: ws.ID,
		StorageKey:  key,
		Filename:    filepath.Base(file.Filename),
		ContentType: contentType,
		Size:        file.Size,
		UploadedBy:  userID,
	}
	if err := h.assetRepo.Create(c.Request().Context(), asset); err != nil {
		return response.InternalErrorErr(c, err)
	}

	assetURL := fmt.Sprintf("/api/workspaces/%s/assets/%s", ws.Slug, asset.ID)

	return response.Success(c, http.StatusOK, map[string]string{
		"url":          assetURL,
		"asset_id":     asset.ID.String(),
		"filename":     asset.Filename,
		"content_type": asset.ContentType,
	})
}

func (h *UploadHandler) GetAsset(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	assetID, err := uuid.Parse(c.Param("assetId"))
	if err != nil {
		return response.NotFound(c, "Asset")
	}

	asset, err := h.assetRepo.GetByID(c.Request().Context(), assetID)
	if err != nil || asset == nil || asset.WorkspaceID != ws.ID {
		return response.NotFound(c, "Asset")
	}

	c.Response().Header().Set("Cache-Control", "private, max-age=300")
	h.setAssetDisposition(c, asset)
	return h.streamAsset(c, asset)
}

// ListAssets serves the workspace file browser (uploads of every kind).
func (h *UploadHandler) ListAssets(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	page, _ := strconv.Atoi(c.QueryParam("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(c.QueryParam("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = 30
	}
	search := strings.TrimSpace(c.QueryParam("search"))

	ctx := c.Request().Context()
	assets, err := h.assetRepo.ListByWorkspace(ctx, ws.ID, search, perPage, (page-1)*perPage)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	total, err := h.assetRepo.CountByWorkspace(ctx, ws.ID, search)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}

	type assetEntry struct {
		ID           string    `json:"id"`
		Filename     string    `json:"filename"`
		ContentType  string    `json:"content_type"`
		Size         int64     `json:"size"`
		UploadedBy   string    `json:"uploaded_by"`
		UploaderName string    `json:"uploader_name"`
		CreatedAt    time.Time `json:"created_at"`
		URL          string    `json:"url"`
	}
	entries := make([]assetEntry, len(assets))
	for i, a := range assets {
		entries[i] = assetEntry{
			ID:           a.ID.String(),
			Filename:     a.Filename,
			ContentType:  a.ContentType,
			Size:         a.Size,
			UploadedBy:   a.UploadedBy.String(),
			UploaderName: a.UploaderName,
			CreatedAt:    a.CreatedAt,
			URL:          fmt.Sprintf("/api/workspaces/%s/assets/%s", ws.Slug, a.ID),
		}
	}
	return response.Success(c, http.StatusOK, map[string]any{
		"items":    entries,
		"total":    total,
		"page":     page,
		"per_page": perPage,
	})
}

// DeleteAsset removes an upload. Uploaders may remove their own files;
// anyone with issue:delete (admin/owner) may remove any.
func (h *UploadHandler) DeleteAsset(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	assetID, err := uuid.Parse(c.Param("assetId"))
	if err != nil {
		return response.NotFound(c, "Asset")
	}
	ctx := c.Request().Context()
	asset, err := h.assetRepo.GetByID(ctx, assetID)
	if err != nil || asset == nil || asset.WorkspaceID != ws.ID {
		return response.NotFound(c, "Asset")
	}
	if asset.UploadedBy != userID && !domain.HasPermission(c.Get("workspace_role").(string), domain.PermIssueDelete) {
		return response.Forbidden(c)
	}
	if _, err := h.assetRepo.Delete(ctx, ws.ID, assetID); err != nil {
		return response.InternalErrorErr(c, err)
	}
	if err := h.store.Delete(ctx, asset.StorageKey); err != nil {
		log.WithError(err).WithField("storage_key", asset.StorageKey).Warn("asset row deleted but storage object remains")
	}
	return response.Success(c, http.StatusOK, map[string]bool{"ok": true})
}

var fileShareTTLs = map[string]time.Duration{
	"1h":   time.Hour,
	"24h":  24 * time.Hour,
	"7d":   7 * 24 * time.Hour,
	"30d":  30 * 24 * time.Hour,
	"365d": 365 * 24 * time.Hour,
}

// ShareAsset signs a public link for one file with the requested lifetime.
func (h *UploadHandler) ShareAsset(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	assetID, err := uuid.Parse(c.Param("assetId"))
	if err != nil {
		return response.NotFound(c, "Asset")
	}
	var req struct {
		TTL string `json:"ttl"`
	}
	if err := c.Bind(&req); err != nil && c.Request().Body != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	ttl, ok := fileShareTTLs[req.TTL]
	if !ok {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Unsupported ttl (use 1h, 24h, 7d, 30d or 365d)")
	}

	asset, err := h.assetRepo.GetByID(c.Request().Context(), assetID)
	if err != nil || asset == nil || asset.WorkspaceID != ws.ID {
		return response.NotFound(c, "Asset")
	}
	token, expiresAt, err := assettoken.GenerateFileShare(h.fileShareSecret, asset.ID, ws.ID, ttl)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusOK, map[string]string{
		"url":        "/api/public/files/" + token,
		"expires_at": expiresAt.Format(time.RFC3339),
	})
}

// PublicFile streams a shared file for holders of a valid share token.
func (h *UploadHandler) PublicFile(c echo.Context) error {
	assetID, workspaceID, err := assettoken.ValidateFileShare(c.Param("token"), h.fileShareSecret)
	if err != nil {
		return response.NotFound(c, "File")
	}
	asset, err := h.assetRepo.GetByID(c.Request().Context(), assetID)
	if err != nil || asset == nil || asset.WorkspaceID != workspaceID {
		return response.NotFound(c, "File")
	}
	c.Response().Header().Set("Cache-Control", "private, max-age=3600")
	h.setAssetDisposition(c, asset)
	return h.streamAsset(c, asset)
}

func (h *UploadHandler) PublicAsset(c echo.Context) error {
	claims, err := assettoken.Validate(c.Param("token"), h.tokenSecret)
	if err != nil {
		return response.NotFound(c, "Asset")
	}

	asset, err := h.assetRepo.GetByID(c.Request().Context(), claims.AssetID)
	if err != nil || asset == nil || asset.WorkspaceID != claims.WorkspaceID {
		return response.NotFound(c, "Asset")
	}

	c.Response().Header().Set("Cache-Control", "private, max-age=3600")
	h.setAssetDisposition(c, asset)
	return h.streamAsset(c, asset)
}

func allowedUploadType(detectedType, ext string) (string, bool) {
	if detectedType == "text/plain" {
		switch ext {
		case ".csv":
			detectedType = "text/csv"
		case ".md", ".markdown":
			detectedType = "text/markdown"
		}
	}
	extensions, ok := allowedUploadExtensions[detectedType]
	if !ok {
		return "", false
	}
	if ext != "" && !extensions[ext] {
		return "", false
	}
	return detectedType, true
}

func (h *UploadHandler) setAssetDisposition(c echo.Context, asset *domain.Asset) {
	if c.QueryParam("download") == "1" || !strings.HasPrefix(asset.ContentType, "image/") {
		c.Response().Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": asset.Filename}))
	}
}

func (h *UploadHandler) SignIssuePromptAssets(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	issue, err := h.issueRepo.GetByIdentifier(c.Request().Context(), ws.ID, c.Param("identifier"))
	if err != nil || issue == nil {
		return response.NotFound(c, "Issue")
	}

	signed := make(map[string]string)
	expiresAt := time.Now().Add(time.Hour)
	if issue.Description != nil {
		for _, source := range extractProtectedAssetSources(*issue.Description) {
			asset, err := h.assetRepo.GetByID(c.Request().Context(), source.assetID)
			if err != nil || asset == nil || asset.WorkspaceID != ws.ID {
				continue
			}

			token, exp, err := assettoken.Generate(h.tokenSecret, asset.ID, ws.ID, issue.ID, time.Hour)
			if err != nil {
				return response.InternalErrorErr(c, err)
			}
			expiresAt = exp
			signed[source.raw] = "/api/public/assets/" + token
		}
	}

	return response.Success(c, http.StatusOK, map[string]interface{}{
		"assets":     signed,
		"expires_at": expiresAt.Format(time.RFC3339),
	})
}

func (h *UploadHandler) streamAsset(c echo.Context, asset *domain.Asset) error {
	rc, err := h.store.Get(c.Request().Context(), asset.StorageKey)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return response.NotFound(c, "Asset")
		}
		return response.NotFound(c, "Asset")
	}
	defer rc.Close()
	c.Response().Header().Set("Content-Type", asset.ContentType)
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	return c.Stream(http.StatusOK, asset.ContentType, rc)
}

type protectedAssetSource struct {
	raw     string
	assetID uuid.UUID
}

var (
	imgSrcPattern      = regexp.MustCompile(`(?i)<img[^>]+src=["']([^"']+)["']`)
	protectedPathRegex = regexp.MustCompile(`^/api/workspaces/[^/]+/assets/([0-9a-fA-F-]{36})$`)
)

func extractProtectedAssetSources(html string) []protectedAssetSource {
	matches := imgSrcPattern.FindAllStringSubmatch(html, -1)
	sources := make([]protectedAssetSource, 0, len(matches))
	seen := make(map[string]bool)
	for _, match := range matches {
		if len(match) < 2 || seen[match[1]] {
			continue
		}
		path := match[1]
		if u, err := url.Parse(path); err == nil && u.IsAbs() {
			path = u.Path
		}
		pathMatches := protectedPathRegex.FindStringSubmatch(path)
		if len(pathMatches) != 2 {
			continue
		}
		assetID, err := uuid.Parse(pathMatches[1])
		if err != nil {
			continue
		}
		seen[match[1]] = true
		sources = append(sources, protectedAssetSource{raw: match[1], assetID: assetID})
	}
	return sources
}
