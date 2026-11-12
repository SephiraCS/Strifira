package service

import (
	"bytes"
	"context"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/pkg/mailclient"
	"github.com/SephiraCS/strifira-backend/pkg/sanitize"
)

// The old sanitizer erased CID references before caching them. Recover the
// original read-only, matching bytes against existing attachments, preserving
// IDs, read/star/archive flags and sync cursors. A provider failure keeps the
// cached message readable and can be retried on its next opening.
func (s *MailService) repairMailImages(ctx context.Context, message *domain.MailMessage, attachments []domain.MailAttachment) {
	if message.Folder != domain.MailFolderInbox || message.UID <= 0 || !sanitize.NeedsMailImageRepair(message.BodyHTML) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	_ = s.withAccountOperation(ctx, message.WorkspaceID, message.AccountID, func(ctx context.Context, account *domain.MailAccount) error {
		password, err := s.credential(ctx, account)
		if err != nil {
			return err
		}
		parsed, err := mailclient.FetchImageSource(ctx, s.fetchOptions(account, password), uint32(message.UID), account.UIDValidity)
		if err != nil {
			return err
		}
		if parsed.MessageID != message.MessageID {
			return ErrMailImageInvalid
		}
		existing := make([]domain.MailAttachment, 0, len(attachments))
		for _, meta := range attachments {
			full, err := s.messageRepo.GetAttachment(ctx, message.WorkspaceID, meta.ID)
			if err != nil {
				return err
			}
			existing = append(existing, *full)
		}
		ids := make(map[string]string)
		for _, part := range parsed.Attachments {
			if part.ContentID == "" {
				continue
			}
			for _, cached := range existing {
				if part.ContentType == cached.ContentType && bytes.Equal(part.Content, cached.Content) {
					if _, exists := ids[part.ContentID]; !exists {
						ids[part.ContentID] = cached.ID.String()
					}
					break
				}
			}
		}
		repaired := sanitize.PrepareMailHTML(parsed.BodyHTML, ids)
		if err := s.messageRepo.UpdateImageHTML(ctx, message.WorkspaceID, message.ID, message.BodyHTML, repaired); err != nil {
			return err
		}
		message.BodyHTML = repaired
		return nil
	})
}
