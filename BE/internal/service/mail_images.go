package service

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/pkg/mailconfig"
	"github.com/SephiraCS/strifira-backend/pkg/sanitize"
	"github.com/google/uuid"
)

var (
	ErrMailImageBlocked     = errors.New("image blocked by security policy")
	ErrMailImageInvalid     = errors.New("image reference is invalid or missing")
	ErrMailImageUnavailable = errors.New("image is unavailable")
)

const mailImageMaxBytes = 5 << 20

var mailImageHTTP = &http.Client{
	Timeout: 12 * time.Second,
	// No proxy, cookie jar, credentials or browser headers. The dialer pins
	// validated public DNS answers and also rejects private literal addresses.
	Transport: &http.Transport{
		DialContext:            mailconfig.PublicDialContext,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  8 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !sanitize.IsMailRemoteImage(req.URL.String()) {
			return ErrMailImageBlocked
		}
		return nil
	},
}

// GetMessageImage accepts only a reference present in the selected message.
// A copied attachment ID cannot grant access to a different message/account.
func (s *MailService) GetMessageImage(ctx context.Context, workspaceID, messageID uuid.UUID, source string) (string, error) {
	message, err := s.messageRepo.GetByID(ctx, workspaceID, messageID)
	if err != nil {
		return "", err
	}
	if !slices.Contains(sanitize.MailImageSources(message.BodyHTML), source) {
		return "", ErrMailImageInvalid
	}
	if strings.HasPrefix(source, "attachment:") {
		id, err := uuid.Parse(strings.TrimPrefix(source, "attachment:"))
		if err != nil {
			return "", ErrMailImageInvalid
		}
		att, err := s.messageRepo.GetAttachment(ctx, workspaceID, id)
		if err != nil {
			return "", err
		}
		if att.MessageID == nil || *att.MessageID != messageID {
			return "", repository.ErrMailMessageNotFound
		}
		return mailImageData(att.Content)
	}
	if !sanitize.IsMailRemoteImage(source) {
		return "", ErrMailImageBlocked
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return "", ErrMailImageInvalid
	}
	request.Header.Set("Accept", "image/png, image/jpeg, image/gif, image/webp")
	request.Header.Set("User-Agent", "Strifira-Mail-Images")
	response, err := s.imageHTTP.Do(request)
	if err != nil {
		if mailconfig.IsUnsafeDestination(err) || errors.Is(err, ErrMailImageBlocked) {
			return "", ErrMailImageBlocked
		}
		return "", ErrMailImageUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", ErrMailImageUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, mailImageMaxBytes+1))
	if err != nil {
		return "", ErrMailImageUnavailable
	}
	return mailImageData(body)
}

func mailImageData(body []byte) (string, error) {
	if len(body) == 0 || len(body) > mailImageMaxBytes {
		return "", ErrMailImageUnavailable
	}
	contentType := http.DetectContentType(body)
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(body), nil
	default:
		// SVG, HTML and other active formats are never rendered as mail images.
		return "", ErrMailImageBlocked
	}
}
