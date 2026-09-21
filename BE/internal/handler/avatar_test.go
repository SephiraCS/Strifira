package handler

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryAvatarRepo struct {
	avatars map[uuid.UUID]*domain.UserAvatar
}

func (r *memoryAvatarRepo) Upsert(_ context.Context, avatar *domain.UserAvatar) error {
	if r.avatars == nil {
		r.avatars = make(map[uuid.UUID]*domain.UserAvatar)
	}
	avatar.UpdatedAt = time.Now()
	stored := *avatar
	r.avatars[avatar.UserID] = &stored
	return nil
}

func (r *memoryAvatarRepo) GetByUserID(_ context.Context, userID uuid.UUID) (*domain.UserAvatar, error) {
	avatar := r.avatars[userID]
	return avatar, nil
}

func (r *memoryAvatarRepo) Delete(_ context.Context, userID uuid.UUID) error {
	delete(r.avatars, userID)
	return nil
}

type memoryUserRepo struct {
	users map[uuid.UUID]*domain.User
}

func (r *memoryUserRepo) Create(_ context.Context, user *domain.User) error {
	if r.users == nil {
		r.users = make(map[uuid.UUID]*domain.User)
	}
	r.users[user.ID] = user
	return nil
}

func (r *memoryUserRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.User, error) {
	user := r.users[id]
	return user, nil
}

func (r *memoryUserRepo) GetByEmail(_ context.Context, email string) (*domain.User, error) {
	for _, user := range r.users {
		if user.Email == email {
			return user, nil
		}
	}
	return nil, nil
}

func (r *memoryUserRepo) Update(_ context.Context, user *domain.User) error {
	r.users[user.ID] = user
	return nil
}

func avatarMultipart(t *testing.T, filename, contentType string, payload []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write(payload)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return body, writer.FormDataContentType()
}

func newAvatarContext(e *echo.Echo, body *bytes.Buffer, contentType string, userID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/me/avatar", body)
	req.Header.Set(echo.HeaderContentType, contentType)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", userID)
	return c, rec
}

func TestAvatarUploadStoresImageAndSetsURL(t *testing.T) {
	e := echo.New()
	userID := uuid.New()
	store := &memoryStorage{}
	avatarRepo := &memoryAvatarRepo{}
	userRepo := &memoryUserRepo{users: map[uuid.UUID]*domain.User{
		userID: {ID: userID, Email: "mario@example.com", Name: "Mario"},
	}}
	h := NewAvatarHandler(store, avatarRepo, userRepo)

	pngBytes := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	body, contentType := avatarMultipart(t, "me.png", "image/png", pngBytes)
	c, rec := newAvatarContext(e, body, contentType, userID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `/api/avatars/`+userID.String()+`?v=`)

	avatar := avatarRepo.avatars[userID]
	require.NotNil(t, avatar)
	assert.Equal(t, "image/png", avatar.ContentType)
	assert.Equal(t, "me.png", avatar.Filename)
	assert.Equal(t, string(pngBytes), store.files[avatar.StorageKey])
	assert.True(t, strings.HasPrefix(avatar.StorageKey, "avatars/"))
	assert.Equal(t, fmt.Sprintf("/api/avatars/%s?v=%d", userID, avatar.UpdatedAt.Unix()), *userRepo.users[userID].AvatarURL)
}

func TestAvatarUploadReplacesPreviousObject(t *testing.T) {
	e := echo.New()
	userID := uuid.New()
	store := &memoryStorage{}
	avatarRepo := &memoryAvatarRepo{}
	userRepo := &memoryUserRepo{users: map[uuid.UUID]*domain.User{userID: {ID: userID}}}
	h := NewAvatarHandler(store, avatarRepo, userRepo)

	gifBytes := []byte("GIF89a....")
	body, contentType := avatarMultipart(t, "old.gif", "image/gif", gifBytes)
	c, _ := newAvatarContext(e, body, contentType, userID)
	require.NoError(t, h.Upload(c))
	firstKey := avatarRepo.avatars[userID].StorageKey

	pngBytes := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	body, contentType = avatarMultipart(t, "new.png", "image/png", pngBytes)
	c, rec := newAvatarContext(e, body, contentType, userID)
	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, avatarRepo.avatars, 1)
	secondKey := avatarRepo.avatars[userID].StorageKey
	assert.NotEqual(t, firstKey, secondKey)
	assert.Equal(t, string(pngBytes), store.files[secondKey])
	assert.NotContains(t, store.files, firstKey)
}

func TestAvatarUploadAcceptsSVG(t *testing.T) {
	e := echo.New()
	userID := uuid.New()
	store := &memoryStorage{}
	avatarRepo := &memoryAvatarRepo{}
	userRepo := &memoryUserRepo{users: map[uuid.UUID]*domain.User{userID: {ID: userID}}}
	h := NewAvatarHandler(store, avatarRepo, userRepo)

	svgBytes := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="8"/></svg>`)
	body, contentType := avatarMultipart(t, "logo.svg", "image/svg+xml", svgBytes)
	c, rec := newAvatarContext(e, body, contentType, userID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/svg+xml", avatarRepo.avatars[userID].ContentType)
}

func TestAvatarUploadRejectsNonImage(t *testing.T) {
	e := echo.New()
	userID := uuid.New()
	h := NewAvatarHandler(&memoryStorage{}, &memoryAvatarRepo{}, &memoryUserRepo{users: map[uuid.UUID]*domain.User{userID: {ID: userID}}})

	body, contentType := avatarMultipart(t, "page.html", "text/html", []byte("<html><script>alert(1)</script></html>"))
	c, rec := newAvatarContext(e, body, contentType, userID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAvatarUploadRejectsSpoofedImage(t *testing.T) {
	e := echo.New()
	userID := uuid.New()
	h := NewAvatarHandler(&memoryStorage{}, &memoryAvatarRepo{}, &memoryUserRepo{users: map[uuid.UUID]*domain.User{userID: {ID: userID}}})

	body, contentType := avatarMultipart(t, "evil.png", "image/png", []byte("<!doctype html><html><body>not an image</body></html>"))
	c, rec := newAvatarContext(e, body, contentType, userID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAvatarUploadRejectsExtensionMismatch(t *testing.T) {
	e := echo.New()
	userID := uuid.New()
	h := NewAvatarHandler(&memoryStorage{}, &memoryAvatarRepo{}, &memoryUserRepo{users: map[uuid.UUID]*domain.User{userID: {ID: userID}}})

	pngBytes := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	body, contentType := avatarMultipart(t, "image.gif", "image/png", pngBytes)
	c, rec := newAvatarContext(e, body, contentType, userID)

	require.NoError(t, h.Upload(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAvatarRemoveClearsURLAndObject(t *testing.T) {
	e := echo.New()
	userID := uuid.New()
	store := &memoryStorage{files: map[string]string{"avatars/old.png": "bytes"}}
	avatarRepo := &memoryAvatarRepo{avatars: map[uuid.UUID]*domain.UserAvatar{
		userID: {ID: uuid.New(), UserID: userID, StorageKey: "avatars/old.png", ContentType: "image/png"},
	}}
	url := "/api/avatars/" + userID.String() + "?v=1"
	userRepo := &memoryUserRepo{users: map[uuid.UUID]*domain.User{userID: {ID: userID, AvatarURL: &url}}}
	h := NewAvatarHandler(store, avatarRepo, userRepo)

	req := httptest.NewRequest(http.MethodDelete, "/api/auth/me/avatar", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("user_id", userID)

	require.NoError(t, h.Remove(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, userRepo.users[userID].AvatarURL)
	assert.NotContains(t, store.files, "avatars/old.png")
	assert.NotContains(t, avatarRepo.avatars, userID)

	// Idempotent: removing again still succeeds.
	req = httptest.NewRequest(http.MethodDelete, "/api/auth/me/avatar", nil)
	rec = httptest.NewRecorder()
	c = e.NewContext(req, rec)
	c.Set("user_id", userID)
	require.NoError(t, h.Remove(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAvatarGetStreamsImageWithScriptlessCSP(t *testing.T) {
	e := echo.New()
	userID := uuid.New()
	svg := `<svg xmlns="http://www.w3.org/2000/svg"></svg>`
	store := &memoryStorage{files: map[string]string{"avatars/a.svg": svg}}
	avatarRepo := &memoryAvatarRepo{avatars: map[uuid.UUID]*domain.UserAvatar{
		userID: {ID: uuid.New(), UserID: userID, StorageKey: "avatars/a.svg", ContentType: "image/svg+xml"},
	}}
	h := NewAvatarHandler(store, avatarRepo, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/avatars/"+userID.String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("userId")
	c.SetParamValues(userID.String())

	require.NoError(t, h.Get(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/svg+xml", rec.Header().Get("Content-Type"))
	assert.Equal(t, "default-src 'none'; style-src 'unsafe-inline'", rec.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, svg, rec.Body.String())
}

func TestAvatarGetUnknownUserReturns404(t *testing.T) {
	e := echo.New()
	h := NewAvatarHandler(&memoryStorage{}, &memoryAvatarRepo{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/avatars/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("userId")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.Get(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
