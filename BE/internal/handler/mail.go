package handler

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/middleware"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/internal/service"
	"github.com/SephiraCS/strifira-backend/pkg/mailconfig"
	"github.com/SephiraCS/strifira-backend/pkg/mailoauth"
	"github.com/SephiraCS/strifira-backend/pkg/response"
	"github.com/SephiraCS/strifira-backend/pkg/validate"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type MailHandler struct {
	mailSvc *service.MailService
}

func NewMailHandler(mailSvc *service.MailService) *MailHandler {
	return &MailHandler{mailSvc: mailSvc}
}

// --- Accounts ---

func (h *MailHandler) ListAccounts(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	accounts, err := h.mailSvc.ListAccounts(c.Request().Context(), ws.ID)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	list := make([]dto.MailAccountResponse, 0, len(accounts))
	for _, account := range accounts {
		list = append(list, mailAccountResponse(account))
	}
	return response.Success(c, http.StatusOK, dto.MailAccountListResponse{Accounts: list})
}

func (h *MailHandler) DiscoverAccount(c echo.Context) error {
	var req dto.DiscoverMailAccountRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, validationDetails(err))
	}
	config, err := h.mailSvc.DiscoverAccount(c.Request().Context(), req.EmailAddress)
	if err != nil {
		return mailAccountError(c, err)
	}
	return response.Success(c, http.StatusOK, config)
}

func mailAccountError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, repository.ErrMailAccountConflict):
		return response.Error(c, 409, "MAIL_ACCOUNT_CHANGED", repository.ErrMailAccountConflict.Error())
	case errors.Is(err, mailoauth.ErrRegistration):
		return response.Error(c, 503, "MAIL_OAUTH_REGISTRATION", mailoauth.ErrRegistration.Error())
	case errors.Is(err, mailoauth.ErrUnsupported):
		return response.Error(c, 422, "MAIL_OAUTH_UNSUPPORTED", mailoauth.ErrUnsupported.Error())
	case errors.Is(err, mailoauth.ErrAuthorization), errors.Is(err, repository.ErrMailOAuthSession):
		return response.Error(c, 422, "MAIL_OAUTH_AUTHORIZATION", mailoauth.ErrAuthorization.Error())
	case errors.Is(err, mailoauth.ErrUnavailable):
		return response.Error(c, 503, "MAIL_OAUTH_UNAVAILABLE", mailoauth.ErrUnavailable.Error())
	case errors.Is(err, mailconfig.ErrAddress):
		return response.Error(c, 422, "MAIL_ADDRESS_INVALID", "Informe um endereço de e-mail válido.")
	case errors.Is(err, mailconfig.ErrNotFound):
		return response.Error(c, 422, "MAIL_DISCOVERY_NOT_FOUND", "Não encontramos uma configuração segura para este domínio. Confira o endereço ou tente novamente mais tarde.")
	case errors.Is(err, mailconfig.ErrUnavailable):
		return response.Error(c, 503, "MAIL_DISCOVERY_UNAVAILABLE", "A descoberta do provedor está indisponível. Tente novamente em instantes.")
	case errors.Is(err, service.ErrMailCredentialRequired):
		return response.Error(c, 422, "MAIL_CREDENTIAL_REQUIRED", service.ErrMailCredentialRequired.Error())
	case errors.Is(err, service.ErrMailOAuthRequired):
		return response.Error(c, 422, "MAIL_OAUTH_REQUIRED", service.ErrMailOAuthRequired.Error())
	case errors.Is(err, service.ErrMailAuthentication):
		return response.Error(c, 422, "MAIL_AUTHENTICATION_FAILED", service.ErrMailAuthentication.Error())
	case errors.Is(err, service.ErrMailConnectionFailed):
		return response.Error(c, 422, "MAIL_CONNECTION_FAILED", service.ErrMailConnectionFailed.Error())
	case errors.Is(err, repository.ErrMailAccountNotFound):
		return response.NotFound(c, "Account")
	default:
		return response.InternalErrorErr(c, err)
	}
}

func (h *MailHandler) CreateAccount(c echo.Context) error {
	var req dto.CreateMailAccountRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, validationDetails(err))
	}

	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)

	account, err := h.mailSvc.CreateAccount(c.Request().Context(), ws.ID, userID, req)
	if err != nil {
		return mailAccountError(c, err)
	}

	return response.Success(c, http.StatusCreated, mailAccountResponse(*account))
}

func (h *MailHandler) UpdateAccount(c echo.Context) error {
	var req dto.UpdateMailAccountRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, validationDetails(err))
	}

	ws := c.Get("workspace").(*domain.Workspace)
	id, err := uuid.Parse(c.Param("accountId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid account ID")
	}

	account, err := h.mailSvc.UpdateAccount(c.Request().Context(), ws.ID, id, req)
	if err != nil {
		return mailAccountError(c, err)
	}

	return response.Success(c, http.StatusOK, mailAccountResponse(*account))
}

func (h *MailHandler) DeleteAccount(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	id, err := uuid.Parse(c.Param("accountId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid account ID")
	}
	if err := h.mailSvc.DeleteAccount(c.Request().Context(), ws.ID, id); err != nil {
		return mailAccountError(c, err)
	}
	return response.Success(c, http.StatusOK, map[string]bool{"deleted": true})
}

func (h *MailHandler) SyncAccount(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	id, err := uuid.Parse(c.Param("accountId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid account ID")
	}
	if err := h.mailSvc.SyncNow(c.Request().Context(), ws.ID, id); err != nil {
		return mailAccountError(c, err)
	}
	return response.Success(c, http.StatusOK, map[string]bool{"synced": true})
}

// --- Messages ---

func (h *MailHandler) ListMessages(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)

	query := service.MailListQuery{
		Folder: c.QueryParam("folder"),
		Search: strings.TrimSpace(c.QueryParam("q")),
	}
	if raw := c.QueryParam("accountId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid account ID")
		}
		query.AccountID = &id
	}
	query.Unread = optionalBool(c.QueryParam("unread"))
	query.Starred = optionalBool(c.QueryParam("starred"))
	query.Archived = optionalBool(c.QueryParam("archived"))
	query.Spam = optionalBool(c.QueryParam("spam"))
	query.Limit, _ = strconv.Atoi(c.QueryParam("limit"))
	query.Offset, _ = strconv.Atoi(c.QueryParam("offset"))

	messages, err := h.mailSvc.ListMessages(c.Request().Context(), ws.ID, query)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}

	// Thread continuation needs the reading pane anyway; the list carries
	// full thread keys so the UI can group without extra round-trips.
	list := make([]dto.MailMessageSummary, 0, len(messages))
	for _, message := range messages {
		list = append(list, mailMessageSummary(message))
	}
	unread, err := h.mailSvc.CountUnreadInbox(c.Request().Context(), ws.ID, query.AccountID)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusOK, dto.MailMessageListResponse{Messages: list, UnreadInboxCount: unread})
}

func (h *MailHandler) GetMessage(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	id, err := uuid.Parse(c.Param("messageId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid message ID")
	}

	message, attachments, err := h.mailSvc.GetMessage(c.Request().Context(), ws.ID, id)
	if err != nil {
		if errors.Is(err, repository.ErrMailMessageNotFound) {
			return response.NotFound(c, "Message")
		}
		return response.InternalErrorErr(c, err)
	}

	detail := dto.MailMessageDetail{
		MailMessageSummary: mailMessageSummary(*message),
		ReplyTo:            message.ReplyTo,
		BodyText:           message.BodyText,
		BodyHTML:           message.BodyHTML,
		InReplyTo:          message.InReplyTo,
		Attachments:        make([]dto.MailAttachmentMeta, 0, len(attachments)),
	}
	for _, att := range attachments {
		detail.Attachments = append(detail.Attachments, mailAttachmentMeta(att))
	}
	return response.Success(c, http.StatusOK, detail)
}

func (h *MailHandler) UpdateMessage(c echo.Context) error {
	var req dto.UpdateMailMessageRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, validationDetails(err))
	}

	ws := c.Get("workspace").(*domain.Workspace)
	id, err := uuid.Parse(c.Param("messageId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid message ID")
	}

	message, err := h.mailSvc.SetMessageFlags(c.Request().Context(), ws.ID, id, req.Unread, req.Starred, req.Archived, req.Spam)
	if err != nil {
		if errors.Is(err, repository.ErrMailMessageNotFound) {
			return response.NotFound(c, "Message")
		}
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusOK, mailMessageSummary(*message))
}

func (h *MailHandler) DeleteMessage(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	id, err := uuid.Parse(c.Param("messageId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid message ID")
	}
	if err := h.mailSvc.DeleteMessage(c.Request().Context(), ws.ID, id); err != nil {
		if errors.Is(err, repository.ErrMailMessageNotFound) {
			return response.NotFound(c, "Message")
		}
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusOK, map[string]bool{"deleted": true})
}

// GetMessageAttachment streams an attachment with its original filename.
func (h *MailHandler) GetMessageAttachment(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	attachmentID, err := uuid.Parse(c.Param("attachmentId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid attachment ID")
	}

	att, err := h.mailSvc.GetMessageAttachment(c.Request().Context(), ws.ID, attachmentID)
	if err != nil {
		if errors.Is(err, repository.ErrMailMessageNotFound) {
			return response.NotFound(c, "Attachment")
		}
		return response.InternalErrorErr(c, err)
	}
	return mailAttachmentResponse(c, att)
}

// --- Drafts ---

func (h *MailHandler) ListDrafts(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	drafts, err := h.mailSvc.ListDrafts(c.Request().Context(), ws.ID, userID)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	list := make([]dto.MailDraftResponse, 0, len(drafts))
	for _, draft := range drafts {
		list = append(list, mailDraftResponse(draft, nil))
	}
	return response.Success(c, http.StatusOK, dto.MailDraftListResponse{Drafts: list})
}

func (h *MailHandler) CreateDraft(c echo.Context) error {
	var req dto.CreateMailDraftRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, validationDetails(err))
	}

	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)

	draft, err := h.mailSvc.CreateDraft(c.Request().Context(), ws.ID, userID, req)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrMailAccountNotFound):
			return response.NotFound(c, "Account")
		case errors.Is(err, repository.ErrMailMessageNotFound):
			return response.NotFound(c, "Message")
		}
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusCreated, mailDraftResponse(*draft, nil))
}

func (h *MailHandler) UpdateDraft(c echo.Context) error {
	var req dto.UpdateMailDraftRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, validationDetails(err))
	}

	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	id, err := uuid.Parse(c.Param("draftId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid draft ID")
	}

	draft, err := h.mailSvc.UpdateDraft(c.Request().Context(), ws.ID, userID, id, req)
	if err != nil {
		if errors.Is(err, repository.ErrMailDraftNotFound) {
			return response.NotFound(c, "Draft")
		}
		return response.InternalErrorErr(c, err)
	}
	attachments, err := h.mailSvc.ListDraftAttachments(c.Request().Context(), ws.ID, userID, id)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusOK, mailDraftResponse(*draft, attachments))
}

func (h *MailHandler) GetDraft(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	id, err := uuid.Parse(c.Param("draftId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid draft ID")
	}
	draft, attachments, err := h.mailSvc.GetDraft(c.Request().Context(), ws.ID, userID, id)
	if err != nil {
		if errors.Is(err, repository.ErrMailDraftNotFound) {
			return response.NotFound(c, "Draft")
		}
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusOK, mailDraftResponse(*draft, attachments))
}

func (h *MailHandler) DeleteDraft(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	id, err := uuid.Parse(c.Param("draftId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid draft ID")
	}
	if err := h.mailSvc.DeleteDraft(c.Request().Context(), ws.ID, userID, id); err != nil {
		if errors.Is(err, repository.ErrMailDraftNotFound) {
			return response.NotFound(c, "Draft")
		}
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusOK, map[string]bool{"deleted": true})
}

// UploadDraftAttachment accepts multipart files (field "attachments") onto a
// draft; every file part is stored as its own staged attachment.
func (h *MailHandler) UploadDraftAttachment(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	draftID, err := uuid.Parse(c.Param("draftId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid draft ID")
	}

	form, err := c.MultipartForm()
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid multipart form")
	}
	files := form.File["attachments"]
	if len(files) == 0 {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "No attachments provided")
	}

	staged := make([]dto.MailAttachmentMeta, 0, len(files))
	for _, fileHeader := range files {
		file, err := fileHeader.Open()
		if err != nil {
			return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Could not read uploaded file")
		}
		content, err := io.ReadAll(io.LimitReader(file, int64(service.MailMaxAttachmentBytes+1)))
		_ = file.Close()
		if err != nil {
			return response.InternalErrorErr(c, err)
		}
		att, err := h.mailSvc.AddDraftAttachment(c.Request().Context(), ws.ID, userID, draftID, fileHeader.Filename, fileHeader.Header.Get("Content-Type"), content)
		if err != nil {
			if errors.Is(err, service.ErrMailAttachmentTooBig) {
				return response.Error(c, http.StatusUnprocessableEntity, "MAIL_ATTACHMENT_TOO_LARGE", "Attachment exceeds the size limit")
			}
			if errors.Is(err, repository.ErrMailDraftNotFound) {
				return response.NotFound(c, "Draft")
			}
			return response.InternalErrorErr(c, err)
		}
		staged = append(staged, mailAttachmentMeta(*att))
	}
	return response.Success(c, http.StatusCreated, map[string]any{"attachments": staged})
}

func (h *MailHandler) DeleteDraftAttachment(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	draftID, err := uuid.Parse(c.Param("draftId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid draft ID")
	}
	attachmentID, err := uuid.Parse(c.Param("attachmentId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid attachment ID")
	}
	if err := h.mailSvc.DeleteDraftAttachment(c.Request().Context(), ws.ID, userID, draftID, attachmentID); err != nil {
		if errors.Is(err, repository.ErrMailDraftNotFound) {
			return response.NotFound(c, "Draft")
		}
		if errors.Is(err, repository.ErrMailMessageNotFound) {
			return response.NotFound(c, "Attachment")
		}
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusOK, map[string]bool{"deleted": true})
}

func (h *MailHandler) GetDraftAttachment(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	draftID, err := uuid.Parse(c.Param("draftId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid draft ID")
	}
	attachmentID, err := uuid.Parse(c.Param("attachmentId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid attachment ID")
	}
	att, err := h.mailSvc.GetDraftAttachment(c.Request().Context(), ws.ID, userID, draftID, attachmentID)
	if err != nil {
		if errors.Is(err, repository.ErrMailDraftNotFound) || errors.Is(err, repository.ErrMailMessageNotFound) {
			return response.NotFound(c, "Attachment")
		}
		return response.InternalErrorErr(c, err)
	}
	return mailAttachmentResponse(c, att)
}

// SendDraft delivers the draft and returns the archived sent message.
func (h *MailHandler) SendDraft(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	id, err := uuid.Parse(c.Param("draftId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid draft ID")
	}

	sent, err := h.mailSvc.SendDraft(c.Request().Context(), ws.ID, userID, id)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrMailDraftNotFound):
			return response.NotFound(c, "Draft")
		case errors.Is(err, repository.ErrMailAccountNotFound):
			return response.NotFound(c, "Account")
		case errors.Is(err, service.ErrMailDraftNoAccount):
			return response.Error(c, http.StatusUnprocessableEntity, "MAIL_NO_ACCOUNT", "Choose an account before sending")
		case errors.Is(err, service.ErrMailNoRecipients):
			return response.Error(c, http.StatusUnprocessableEntity, "MAIL_NO_RECIPIENTS", "Add at least one recipient")
		case errors.Is(err, service.ErrMailAttachmentTooBig):
			return response.Error(c, http.StatusUnprocessableEntity, "MAIL_ATTACHMENT_TOO_LARGE", "Attachments exceed the size limit")
		case errors.Is(err, service.ErrMailSendFailed):
			return response.Error(c, http.StatusBadGateway, "MAIL_SEND_FAILED", err.Error())
		}
		return mailAccountError(c, err)
	}
	return response.Success(c, http.StatusOK, dto.SendDraftResponse{MessageID: sent.ID.String()})
}

// --- Mappers and helpers ---

func mailAccountResponse(account domain.MailAccount) dto.MailAccountResponse {
	provider := account.OAuthProvider
	if provider == "" {
		provider = mailoauth.ProviderFor(&mailconfig.Config{Incoming: mailconfig.Server{Host: account.IMAPHost}, Outgoing: mailconfig.Server{Host: account.SMTPHost}})
	}
	if account.LastSyncError != "" && account.LastSyncError != service.ErrMailAuthentication.Error() && account.LastSyncError != mailoauth.ErrAuthorization.Error() && account.LastSyncError != mailoauth.ErrRegistration.Error() && account.LastSyncError != mailoauth.ErrUnavailable.Error() {
		account.LastSyncError = service.ErrMailConnectionFailed.Error()
	}
	return dto.MailAccountResponse{
		AuthMethod:    account.AuthMethod,
		OAuthProvider: provider,
		ID:            account.ID.String(),
		EmailAddress:  account.EmailAddress,
		DisplayName:   account.DisplayName,
		IMAPHost:      account.IMAPHost,
		IMAPPort:      account.IMAPPort,
		IMAPSecurity:  account.IMAPSecurity,
		SMTPHost:      account.SMTPHost,
		SMTPPort:      account.SMTPPort,
		SMTPSecurity:  account.SMTPSecurity,
		Username:      account.Username,
		Signature:     account.Signature,
		LastSyncedAt:  account.LastSyncedAt,
		LastSyncError: account.LastSyncError,
		CreatedAt:     account.CreatedAt,
	}
}

func mailMessageSummary(message domain.MailMessage) dto.MailMessageSummary {
	to := make([]dto.MailAddress, 0, len(message.To))
	for _, a := range message.To {
		to = append(to, dto.MailAddress{Name: a.Name, Address: a.Address})
	}
	cc := make([]dto.MailAddress, 0, len(message.CC))
	for _, a := range message.CC {
		cc = append(cc, dto.MailAddress{Name: a.Name, Address: a.Address})
	}
	from := dto.MailAddress{Name: message.From.Name, Address: message.From.Address}
	return dto.MailMessageSummary{
		ID:             message.ID.String(),
		AccountID:      message.AccountID.String(),
		Folder:         message.Folder,
		ThreadKey:      message.ThreadKey,
		Subject:        message.Subject,
		From:           from,
		To:             to,
		CC:             cc,
		Snippet:        message.Snippet,
		Unread:         message.Unread,
		Starred:        message.Starred,
		Archived:       message.Archived,
		Spam:           message.Spam,
		HasAttachments: message.HasAttachments,
		SentAt:         message.SentAt,
	}
}

func mailAttachmentMeta(att domain.MailAttachment) dto.MailAttachmentMeta {
	meta := dto.MailAttachmentMeta{
		ID:          att.ID.String(),
		Filename:    att.Filename,
		ContentType: att.ContentType,
		SizeBytes:   att.SizeBytes,
	}
	if att.MessageID != nil {
		meta.MessageID = att.MessageID.String()
	}
	if att.DraftID != nil {
		meta.DraftID = att.DraftID.String()
	}
	return meta
}

func mailDraftResponse(draft domain.MailDraft, attachments []domain.MailAttachment) dto.MailDraftResponse {
	to := make([]dto.MailAddress, 0, len(draft.To))
	for _, a := range draft.To {
		to = append(to, dto.MailAddress{Name: a.Name, Address: a.Address})
	}
	cc := make([]dto.MailAddress, 0, len(draft.CC))
	for _, a := range draft.CC {
		cc = append(cc, dto.MailAddress{Name: a.Name, Address: a.Address})
	}
	bcc := make([]dto.MailAddress, 0, len(draft.BCC))
	for _, a := range draft.BCC {
		bcc = append(bcc, dto.MailAddress{Name: a.Name, Address: a.Address})
	}
	resp := dto.MailDraftResponse{
		ID:          draft.ID.String(),
		To:          to,
		CC:          cc,
		BCC:         bcc,
		Subject:     draft.Subject,
		Body:        draft.Body,
		Attachments: make([]dto.MailAttachmentMeta, 0, len(attachments)),
		UpdatedAt:   draft.UpdatedAt,
	}
	if draft.AccountID != nil {
		resp.AccountID = draft.AccountID.String()
	}
	if draft.InReplyToMessageID != nil {
		resp.InReplyToMessageID = draft.InReplyToMessageID.String()
	}
	if draft.ForwardFromMessageID != nil {
		resp.ForwardFromMessageID = draft.ForwardFromMessageID.String()
	}
	for _, att := range attachments {
		resp.Attachments = append(resp.Attachments, mailAttachmentMeta(att))
	}
	return resp
}

// mailAttachmentResponse streams the bytes with a download disposition and
// a conservative content type.
func mailAttachmentResponse(c echo.Context, att *domain.MailAttachment) error {
	contentType := att.ContentType
	if contentType == "" || strings.Contains(contentType, "text/html") || strings.Contains(contentType, "svg") {
		contentType = "application/octet-stream"
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(att.Filename)+`"`)
	return c.Blob(http.StatusOK, contentType, att.Content)
}

// sanitizeFilename strips path separators and quotes from Content-Disposition.
func sanitizeFilename(name string) string {
	replacer := strings.NewReplacer(`\`, "_", `/`, "_", `"`, "_", "\r", "", "\n", "")
	cleaned := replacer.Replace(name)
	if cleaned == "" {
		return "attachment"
	}
	return cleaned
}

func optionalBool(raw string) *bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1":
		v := true
		return &v
	case "false", "0":
		v := false
		return &v
	}
	return nil
}
