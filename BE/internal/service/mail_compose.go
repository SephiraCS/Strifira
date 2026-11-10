package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/pkg/mailclient"
	mailaddress "github.com/emersion/go-message/mail"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// --- Message listing / reading ---

type MailListQuery struct {
	AccountID *uuid.UUID
	Folder    string
	Search    string
	Unread    *bool
	Starred   *bool
	Archived  *bool
	Spam      *bool
	Limit     int
	Offset    int
}

func (s *MailService) ListMessages(ctx context.Context, workspaceID uuid.UUID, q MailListQuery) ([]domain.MailMessage, error) {
	if q.Folder == "" {
		q.Folder = domain.MailFolderInbox
	}
	if q.Limit <= 0 || q.Limit > mailPageSize {
		q.Limit = mailPageSize
	}
	filters := repository.MailFilters{
		Unread: q.Unread, Starred: q.Starred, Archived: q.Archived, Spam: q.Spam,
	}
	return s.messageRepo.ListByFolder(ctx, workspaceID, q.AccountID, q.Folder, q.Search, filters, q.Limit, q.Offset)
}

func (s *MailService) GetMessage(ctx context.Context, workspaceID, id uuid.UUID) (*domain.MailMessage, []domain.MailAttachment, error) {
	message, err := s.messageRepo.GetByID(ctx, workspaceID, id)
	if err != nil {
		return nil, nil, err
	}
	attachments, err := s.messageRepo.ListAttachmentsByMessage(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return message, attachments, nil
}

func (s *MailService) GetMessageAttachment(ctx context.Context, workspaceID, attachmentID uuid.UUID) (*domain.MailAttachment, error) {
	return s.messageRepo.GetAttachment(ctx, workspaceID, attachmentID)
}

func (s *MailService) SetMessageFlags(ctx context.Context, workspaceID, id uuid.UUID, unread, starred, archived, spam *bool) (*domain.MailMessage, error) {
	return s.messageRepo.SetFlags(ctx, workspaceID, id, unread, starred, archived, spam)
}

func (s *MailService) DeleteMessage(ctx context.Context, workspaceID, id uuid.UUID) error {
	return s.messageRepo.Delete(ctx, workspaceID, id)
}

// --- Drafts ---

func (s *MailService) CreateDraft(ctx context.Context, workspaceID, userID uuid.UUID, req dto.CreateMailDraftRequest) (*domain.MailDraft, error) {
	draft := &domain.MailDraft{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		UserID:      userID,
		Subject:     req.Subject,
		Body:        req.Body,
		To:          toDomainDraftAddresses(req.To),
		CC:          toDomainDraftAddresses(req.CC),
		BCC:         toDomainDraftAddresses(req.BCC),
	}
	if err := s.applyDraftContext(ctx, workspaceID, draft, req.AccountID, req.InReplyToMessageID, req.ForwardFromMessageID); err != nil {
		return nil, err
	}
	if err := s.draftRepo.Create(ctx, draft); err != nil {
		return nil, err
	}
	return draft, nil
}

func (s *MailService) UpdateDraft(ctx context.Context, workspaceID, userID, id uuid.UUID, req dto.UpdateMailDraftRequest) (*domain.MailDraft, error) {
	draft, err := s.draftRepo.GetByID(ctx, workspaceID, userID, id)
	if err != nil {
		return nil, err
	}
	if req.To != nil {
		draft.To = toDomainDraftAddresses(req.To)
	}
	if req.CC != nil {
		draft.CC = toDomainDraftAddresses(req.CC)
	}
	if req.BCC != nil {
		draft.BCC = toDomainDraftAddresses(req.BCC)
	}
	if req.Subject != nil {
		draft.Subject = *req.Subject
	}
	if req.Body != nil {
		draft.Body = *req.Body
	}
	if req.AccountID != "" {
		if err := s.applyDraftContext(ctx, workspaceID, draft, req.AccountID, "", ""); err != nil {
			return nil, err
		}
	}
	if err := s.draftRepo.Update(ctx, draft); err != nil {
		return nil, err
	}
	return draft, nil
}

// applyDraftContext resolves the account / reply / forward links of a draft
// and prefills reply metadata (thread, recipients, quoted subject).
func (s *MailService) applyDraftContext(ctx context.Context, workspaceID uuid.UUID, draft *domain.MailDraft, accountID, inReplyToID, forwardFromID string) error {
	if accountID != "" {
		parsed, err := uuid.Parse(accountID)
		if err != nil {
			return repository.ErrMailAccountNotFound
		}
		draft.AccountID = &parsed
	}
	if inReplyToID != "" {
		id, err := uuid.Parse(inReplyToID)
		if err != nil {
			return repository.ErrMailMessageNotFound
		}
		original, err := s.messageRepo.GetByID(ctx, workspaceID, id)
		if err != nil {
			return err
		}
		draft.InReplyToMessageID = &id
		draft.ThreadKey = original.ThreadKey
		if strings.TrimSpace(draft.Subject) == "" {
			draft.Subject = replySubject(original.Subject)
		}
	} else if forwardFromID != "" {
		id, err := uuid.Parse(forwardFromID)
		if err != nil {
			return repository.ErrMailMessageNotFound
		}
		original, err := s.messageRepo.GetByID(ctx, workspaceID, id)
		if err != nil {
			return err
		}
		draft.ForwardFromMessageID = &id
		draft.ThreadKey = ""
		if strings.TrimSpace(draft.Subject) == "" {
			draft.Subject = forwardSubject(original.Subject)
		}
	}
	return nil
}

func (s *MailService) GetDraft(ctx context.Context, workspaceID, userID, id uuid.UUID) (*domain.MailDraft, []domain.MailAttachment, error) {
	draft, err := s.draftRepo.GetByID(ctx, workspaceID, userID, id)
	if err != nil {
		return nil, nil, err
	}
	attachments, err := s.draftRepo.ListAttachments(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return draft, attachments, nil
}

func (s *MailService) ListDrafts(ctx context.Context, workspaceID, userID uuid.UUID) ([]domain.MailDraft, error) {
	return s.draftRepo.ListByUser(ctx, workspaceID, userID)
}

func (s *MailService) DeleteDraft(ctx context.Context, workspaceID, userID, id uuid.UUID) error {
	return s.draftRepo.Delete(ctx, workspaceID, userID, id)
}

// ListDraftAttachments returns a draft's staged files without their bytes.
func (s *MailService) ListDraftAttachments(ctx context.Context, workspaceID, userID, draftID uuid.UUID) ([]domain.MailAttachment, error) {
	if _, err := s.draftRepo.GetByID(ctx, workspaceID, userID, draftID); err != nil {
		return nil, err
	}
	return s.draftRepo.ListAttachments(ctx, draftID)
}

// AddDraftAttachment stages one file on a draft. Oversize files are refused
// before they enter the database.
func (s *MailService) AddDraftAttachment(ctx context.Context, workspaceID, userID, draftID uuid.UUID, filename, contentType string, content []byte) (*domain.MailAttachment, error) {
	if _, err := s.draftRepo.GetByID(ctx, workspaceID, userID, draftID); err != nil {
		return nil, err
	}
	if len(content) > MailMaxAttachmentBytes {
		return nil, ErrMailAttachmentTooBig
	}
	total := int64(len(content))
	existing, err := s.draftRepo.ListAttachments(ctx, draftID)
	if err != nil {
		return nil, err
	}
	for _, att := range existing {
		total += att.SizeBytes
	}
	if total > MailMaxMessageBytes {
		return nil, ErrMailAttachmentTooBig
	}
	att := &domain.MailAttachment{
		ID:          uuid.New(),
		DraftID:     &draftID,
		Filename:    filename,
		ContentType: contentType,
		SizeBytes:   int64(len(content)),
		Content:     content,
	}
	if err := s.draftRepo.AddAttachment(ctx, att); err != nil {
		return nil, err
	}
	att.Content = nil
	return att, nil
}

func (s *MailService) DeleteDraftAttachment(ctx context.Context, workspaceID, userID, draftID, attachmentID uuid.UUID) error {
	if _, err := s.draftRepo.GetByID(ctx, workspaceID, userID, draftID); err != nil {
		return err
	}
	return s.draftRepo.DeleteAttachment(ctx, draftID, attachmentID)
}

func (s *MailService) GetDraftAttachment(ctx context.Context, workspaceID, userID, draftID, attachmentID uuid.UUID) (*domain.MailAttachment, error) {
	if _, err := s.draftRepo.GetByID(ctx, workspaceID, userID, draftID); err != nil {
		return nil, err
	}
	return s.draftRepo.GetAttachment(ctx, draftID, attachmentID)
}

// --- Send ---

// SendDraft sends a draft through its account's SMTP server and archives the
// exact RFC 5322 bytes as a sent message. Replies carry In-Reply-To and
// References; forwards re-attach the original files.
func (s *MailService) SendDraft(ctx context.Context, workspaceID, userID, draftID uuid.UUID) (*domain.MailMessage, error) {
	draft, err := s.draftRepo.GetByID(ctx, workspaceID, userID, draftID)
	if err != nil {
		return nil, err
	}
	if draft.AccountID == nil {
		return nil, ErrMailDraftNoAccount
	}
	if len(draft.To) == 0 && len(draft.CC) == 0 && len(draft.BCC) == 0 {
		return nil, ErrMailNoRecipients
	}

	account, err := s.accountRepo.GetByID(ctx, workspaceID, *draft.AccountID)
	if err != nil {
		return nil, err
	}
	password, err := s.decryptPassword(account)
	if err != nil {
		return nil, err
	}
	staged, err := s.draftRepo.ListAttachments(ctx, draft.ID)
	if err != nil {
		return nil, err
	}

	outgoing := &mailclient.OutgoingMessage{
		From:    mailaddress.Address{Name: senderDisplayName(account), Address: account.EmailAddress},
		To:      toClientAddresses(draft.To),
		CC:      toClientAddresses(draft.CC),
		BCC:     toClientAddresses(draft.BCC),
		Subject: draft.Subject,
		Body:    composeBody(draft.Body, account.Signature),
	}

	// Staged files ride along as-is; their bytes are re-read because the
	// list endpoint deliberately strips content.
	attachments := make([]domain.MailAttachment, 0, len(staged))
	totalBytes := int64(0)
	for _, att := range staged {
		full, err := s.draftRepo.GetAttachment(ctx, draft.ID, att.ID)
		if err != nil {
			return nil, err
		}
		totalBytes += full.SizeBytes
		if full.SizeBytes > MailMaxAttachmentBytes || totalBytes > MailMaxMessageBytes {
			return nil, ErrMailAttachmentTooBig
		}
		attachments = append(attachments, *full)
	}

	// Reply context: thread continuity through In-Reply-To/References, and
	// the original's files when this is a forward.
	if draft.InReplyToMessageID != nil || draft.ForwardFromMessageID != nil {
		sourceID := draft.InReplyToMessageID
		if sourceID == nil {
			sourceID = draft.ForwardFromMessageID
		}
		original, err := s.messageRepo.GetByID(ctx, workspaceID, *sourceID)
		if err == nil {
			if draft.InReplyToMessageID != nil {
				outgoing.InReplyTo = original.MessageID
				outgoing.References = originalThreadReferences(original)
			} else {
				originalAttachments, err := s.messageRepo.ListAttachmentsByMessage(ctx, original.ID)
				if err == nil {
					for _, att := range originalAttachments {
						full, err := s.messageRepo.GetAttachment(ctx, workspaceID, att.ID)
						if err != nil {
							continue
						}
						totalBytes += full.SizeBytes
						if full.SizeBytes > MailMaxAttachmentBytes || totalBytes > MailMaxMessageBytes {
							return nil, ErrMailAttachmentTooBig
						}
						attachments = append(attachments, *full)
					}
				}
			}
		}
	}
	if len(attachments) > 0 {
		outgoing.Attachments = make([]mailclient.ParsedAttachment, 0, len(attachments))
		for _, att := range attachments {
			outgoing.Attachments = append(outgoing.Attachments, mailclient.ParsedAttachment{
				Filename:    att.Filename,
				ContentType: att.ContentType,
				Content:     att.Content,
			})
		}
	}

	messageID := mailclient.FallbackMessageID()
	outgoing.MessageID = messageID
	raw, err := mailclient.SendMessage(s.sendOptions(account, password), outgoing)
	if err != nil {
		log.WithError(err).WithField("account", account.EmailAddress).Warn("mail send failed")
		return nil, fmt.Errorf("%w: %v", ErrMailSendFailed, err)
	}

	parsedRaw, err := mailclient.ParseMessage(raw)
	if err != nil {
		// The SMTP server accepted the message; a local parse hiccup must
		// not lose the send. Fall back to the fields we know.
		parsedRaw = nil
	}
	sent := &domain.MailMessage{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		AccountID:   account.ID,
		Folder:      domain.MailFolderSent,
		MessageID:   messageID,
		Subject:     draft.Subject,
		From:        domain.MailAddress{Name: senderDisplayName(account), Address: account.EmailAddress},
		To:          draft.To,
		CC:          draft.CC,
		BCC:         draft.BCC,
		BodyText:    draft.Body,
		Snippet:     snippetText(draft.Body),
		SentAt:      time.Now().UTC(),
	}
	if parsedRaw != nil {
		sent.MessageID = parsedRaw.MessageID
		sent.BodyText = parsedRaw.BodyText
		sent.BodyHTML = parsedRaw.BodyHTML
	}
	if draft.InReplyToMessageID != nil {
		original, err := s.messageRepo.GetByID(ctx, workspaceID, *draft.InReplyToMessageID)
		if err == nil {
			sent.InReplyTo = original.MessageID
			sent.ThreadKey = original.ThreadKey
		}
	}
	if sent.ThreadKey == "" {
		sent.ThreadKey = mailclient.ThreadKey(sent.MessageID, sent.InReplyTo, nil, sent.Subject, sent.From.Address)
	}

	if err := s.messageRepo.FinalizeSentMessage(ctx, sent, attachments, &draft.ID); err != nil {
		return nil, err
	}

	s.hub.Broadcast(workspaceID, realtime.Event{
		Type: "mail.updated",
		Payload: map[string]any{
			"account_id": account.ID.String(),
			"folder":     domain.MailFolderSent,
		},
	})
	return sent, nil
}

// originalThreadReferences rebuilds the References chain of a reply: the
// original's chain plus the original itself.
func originalThreadReferences(original *domain.MailMessage) []string {
	refs := []string{}
	if original.InReplyTo != "" && original.MessageID != original.InReplyTo {
		// Best effort: the parent's own chain is not cached, so anchor the
		// thread at the cached message.
		refs = append(refs, original.InReplyTo)
	}
	if original.MessageID != "" {
		refs = append(refs, original.MessageID)
	}
	return refs
}

func senderDisplayName(account *domain.MailAccount) string {
	if strings.TrimSpace(account.DisplayName) != "" {
		return account.DisplayName
	}
	return account.EmailAddress
}

// composeBody appends the account signature when one is configured.
func composeBody(body, signature string) string {
	body = strings.TrimRight(body, "\n")
	sig := strings.TrimSpace(signature)
	if sig == "" {
		return body + "\n"
	}
	return body + "\n\n--\n" + sig + "\n"
}

func replySubject(subject string) string {
	if subject == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(subject), "re:") {
		return subject
	}
	return "Re: " + subject
}

func forwardSubject(subject string) string {
	if subject == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(subject), "fwd:") {
		return subject
	}
	return "Fwd: " + subject
}

func toDomainDraftAddresses(in []dto.MailAddress) []domain.MailAddress {
	out := make([]domain.MailAddress, 0, len(in))
	for _, a := range in {
		out = append(out, domain.MailAddress{Name: a.Name, Address: a.Address})
	}
	return out
}

func toClientAddresses(in []domain.MailAddress) []mailaddress.Address {
	out := make([]mailaddress.Address, 0, len(in))
	for _, a := range in {
		out = append(out, mailaddress.Address{Name: a.Name, Address: a.Address})
	}
	return out
}

