package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/pkg/crypto"
	"github.com/SephiraCS/strifira-backend/pkg/mailclient"
	"github.com/SephiraCS/strifira-backend/pkg/safego"
	"github.com/SephiraCS/strifira-backend/pkg/sanitize"
	mailaddress "github.com/emersion/go-message/mail"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// Errors surfaced as 4xx responses by the handler.
var (
	ErrMailNoRecipients      = errors.New("a message needs at least one recipient")
	ErrMailDraftNoAccount    = errors.New("choose an account before sending")
	ErrMailAttachmentTooBig  = errors.New("attachment is too large")
	ErrMailConnectionFailed  = errors.New("could not connect with the given settings")
	ErrMailSendFailed        = errors.New("the server refused the message")
)

// Mail limits. Fixed in code: attachments are user data, not configuration.
const (
	MailMaxAttachmentBytes = 10 << 20 // 10 MiB per file
	MailMaxMessageBytes    = 25 << 20 // 25 MiB per message total
	mailSyncInterval       = 45 * time.Second
	mailPageSize           = 100
)

// MailService owns the workspace's e-mail accounts, the INBOX cache and the
// compose/send flows.
type MailService struct {
	accountRepo repository.MailAccountRepo
	messageRepo repository.MailMessageRepo
	draftRepo   repository.MailDraftRepo
	hub         *realtime.Hub
	encryptKey  []byte
}

func NewMailService(
	accountRepo repository.MailAccountRepo,
	messageRepo repository.MailMessageRepo,
	draftRepo repository.MailDraftRepo,
	hub *realtime.Hub,
	encryptKey []byte,
) *MailService {
	return &MailService{
		accountRepo: accountRepo,
		messageRepo: messageRepo,
		draftRepo:   draftRepo,
		hub:         hub,
		encryptKey:  encryptKey,
	}
}

// StartSyncer runs the periodic INBOX poller until ctx is cancelled.
func (s *MailService) StartSyncer(ctx context.Context) {
	safego.Every(ctx, "mail inbox sync", mailSyncInterval, true, func(ctx context.Context) {
		s.SyncDueAccounts(ctx)
	})
}

// --- Accounts ---

func (s *MailService) ListAccounts(ctx context.Context, workspaceID uuid.UUID) ([]domain.MailAccount, error) {
	return s.accountRepo.ListByWorkspace(ctx, workspaceID)
}

// CreateAccount verifies both connections with the provided credentials
// before storing them, so a typo never becomes a silently broken mailbox.
func (s *MailService) CreateAccount(ctx context.Context, workspaceID, userID uuid.UUID, req dto.CreateMailAccountRequest) (*domain.MailAccount, error) {
	if err := s.testConnections(req.IMAPHost, req.IMAPPort, req.IMAPSecurity, req.SMTPHost, req.SMTPPort, req.SMTPSecurity, req.Username, req.Password); err != nil {
		return nil, err
	}
	encrypted, err := crypto.Encrypt(req.Password, s.encryptKey)
	if err != nil {
		return nil, err
	}
	account := &domain.MailAccount{
		ID:                uuid.New(),
		WorkspaceID:       workspaceID,
		CreatedBy:         userID,
		EmailAddress:      strings.ToLower(strings.TrimSpace(req.EmailAddress)),
		DisplayName:       strings.TrimSpace(req.DisplayName),
		IMAPHost:          strings.TrimSpace(req.IMAPHost),
		IMAPPort:          req.IMAPPort,
		IMAPSecurity:      req.IMAPSecurity,
		SMTPHost:          strings.TrimSpace(req.SMTPHost),
		SMTPPort:          req.SMTPPort,
		SMTPSecurity:      req.SMTPSecurity,
		Username:          req.Username,
		PasswordEncrypted: encrypted,
		Signature:         req.Signature,
	}
	if err := s.accountRepo.Create(ctx, account); err != nil {
		return nil, err
	}
	// First sync in the background: creation returns fast even on slow
	// mailboxes, and the client polls/lists afterwards.
	safego.Go("mail first sync", func() {
		s.syncAccount(context.Background(), *account)
	})
	return account, nil
}

func (s *MailService) UpdateAccount(ctx context.Context, workspaceID, id uuid.UUID, req dto.UpdateMailAccountRequest) (*domain.MailAccount, error) {
	account, err := s.accountRepo.GetByID(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	applyAccountUpdate(account, req)

	if req.Password != nil && *req.Password != "" {
		// Only reconnect-check when credentials changed; otherwise the
		// stored password keeps working even if the server is unreachable.
		if err := s.testConnections(account.IMAPHost, account.IMAPPort, account.IMAPSecurity, account.SMTPHost, account.SMTPPort, account.SMTPSecurity, account.Username, *req.Password); err != nil {
			return nil, err
		}
		encrypted, err := crypto.Encrypt(*req.Password, s.encryptKey)
		if err != nil {
			return nil, err
		}
		account.PasswordEncrypted = encrypted
	}
	if err := s.accountRepo.Update(ctx, account); err != nil {
		return nil, err
	}
	return account, nil
}

func applyAccountUpdate(account *domain.MailAccount, req dto.UpdateMailAccountRequest) {
	if req.EmailAddress != "" {
		account.EmailAddress = strings.ToLower(strings.TrimSpace(req.EmailAddress))
	}
	if req.DisplayName != "" {
		account.DisplayName = strings.TrimSpace(req.DisplayName)
	}
	if req.IMAPHost != "" {
		account.IMAPHost = strings.TrimSpace(req.IMAPHost)
	}
	if req.IMAPPort != nil {
		account.IMAPPort = *req.IMAPPort
	}
	if req.IMAPSecurity != "" {
		account.IMAPSecurity = req.IMAPSecurity
	}
	if req.SMTPHost != "" {
		account.SMTPHost = strings.TrimSpace(req.SMTPHost)
	}
	if req.SMTPPort != nil {
		account.SMTPPort = *req.SMTPPort
	}
	if req.SMTPSecurity != "" {
		account.SMTPSecurity = req.SMTPSecurity
	}
	if req.Username != "" {
		account.Username = req.Username
	}
	if req.Signature != nil {
		account.Signature = *req.Signature
	}
}

func (s *MailService) DeleteAccount(ctx context.Context, workspaceID, id uuid.UUID) error {
	return s.accountRepo.Delete(ctx, workspaceID, id)
}

// SyncNow forces an immediate INBOX sync for one account.
func (s *MailService) SyncNow(ctx context.Context, workspaceID, id uuid.UUID) error {
	account, err := s.accountRepo.GetByID(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	return s.syncAccount(ctx, *account)
}

func (s *MailService) testConnections(imapHost string, imapPort int, imapSecurity, smtpHost string, smtpPort int, smtpSecurity, username, password string) error {
	if err := mailclient.TestIMAPConnection(mailclient.FetchOptions{
		Host: imapHost, Port: imapPort, Security: imapSecurity, Username: username, Password: password,
	}); err != nil {
		return fmt.Errorf("%w: imap: %v", ErrMailConnectionFailed, err)
	}
	if err := mailclient.TestSMTPConnection(mailclient.SendOptions{
		Host: smtpHost, Port: smtpPort, Security: smtpSecurity, Username: username, Password: password,
	}); err != nil {
		return fmt.Errorf("%w: smtp: %v", ErrMailConnectionFailed, err)
	}
	return nil
}

// decryptPassword returns the account password in memory.
func (s *MailService) decryptPassword(account *domain.MailAccount) (string, error) {
	return crypto.Decrypt(account.PasswordEncrypted, s.encryptKey)
}

func (s *MailService) fetchOptions(account *domain.MailAccount, password string) mailclient.FetchOptions {
	return mailclient.FetchOptions{
		Host: account.IMAPHost, Port: account.IMAPPort, Security: account.IMAPSecurity,
		Username: account.Username, Password: password,
	}
}

func (s *MailService) sendOptions(account *domain.MailAccount, password string) mailclient.SendOptions {
	return mailclient.SendOptions{
		Host: account.SMTPHost, Port: account.SMTPPort, Security: account.SMTPSecurity,
		Username: account.Username, Password: password,
	}
}

// --- Sync ---

// SyncDueAccounts syncs every account whose cursor is older than the sync
// interval, across all workspaces. Called by the background loop.
func (s *MailService) SyncDueAccounts(ctx context.Context) {
	accounts, err := s.accountRepo.ListAll(ctx)
	if err != nil {
		log.WithError(err).Warn("mail sync: list accounts failed")
		return
	}
	now := time.Now()
	for i := range accounts {
		account := accounts[i]
		if account.LastSyncedAt != nil && now.Sub(*account.LastSyncedAt) < mailSyncInterval/2 {
			continue
		}
		safego.Run("mail sync account", func() {
			_ = s.syncAccount(ctx, account)
		})
	}
}

// syncAccount pulls new INBOX messages into the local cache and broadcasts
// mail.updated so open clients refresh. Errors persist on the account.
func (s *MailService) syncAccount(ctx context.Context, account domain.MailAccount) error {
	password, err := s.decryptPassword(&account)
	if err != nil {
		return err
	}

	uidValidity, lastUID, err := s.messageRepo.SyncState(ctx, account.ID)
	if err != nil {
		return err
	}
	result, err := mailclient.FetchNewMessages(s.fetchOptions(&account, password), uint64(lastUID), uidValidity)
	now := time.Now()
	if err != nil {
		msg := truncateSyncError(err)
		_ = s.messageRepo.UpdateSyncState(ctx, account.ID, uidValidity, lastUID, msg, now)
		log.WithError(err).WithField("account", account.EmailAddress).Warn("mail sync failed")
		return err
	}

	messages := make([]repository.MailInboxMessage, 0, len(result.Messages))
	for i := range result.Messages {
		fetched := result.Messages[i]
		parsed := fetched.Parsed
		sentAt := fetched.InternalDate

		messageID := ""
		inReplyTo := ""
		var references []string
		subject, bodyText, bodyHTML := "", "", ""
		var to, cc []domain.MailAddress
		var replyTo string
		from := domain.MailAddress{Address: account.EmailAddress}
		attachments := make([]domain.MailAttachment, 0, len(parsed.Attachments))
		if parsed != nil {
			messageID = parsed.MessageID
			inReplyTo = parsed.InReplyTo
			references = parsed.References
			subject = parsed.Subject
			bodyText = parsed.BodyText
			bodyHTML = sanitize.SanitizeHTML(parsed.BodyHTML)
			to = toDomainAddresses(parsed.To)
			cc = toDomainAddresses(parsed.CC)
			from = domain.MailAddress{Name: parsed.From.Name, Address: parsed.From.Address}
			replyTo = replyToOf(parsed)
			for _, att := range parsed.Attachments {
				if len(att.Content) > MailMaxAttachmentBytes {
					log.WithField("account", account.EmailAddress).
						Warnf("mail sync: dropping oversize attachment %q (%d bytes)", att.Filename, len(att.Content))
					continue
				}
				attachments = append(attachments, domain.MailAttachment{
					ID:          uuid.New(),
					Filename:    att.Filename,
					ContentType: att.ContentType,
					SizeBytes:   int64(len(att.Content)),
					Content:     att.Content,
				})
			}
		}
		if messageID == "" {
			messageID = mailclient.FallbackMessageID()
		}
		if sentAt.IsZero() {
			sentAt = now
		}
		threadKey := mailclient.ThreadKey(messageID, inReplyTo, references, subject, from.Address)
		if threadKey == "" {
			threadKey = messageID
		}
		messages = append(messages, repository.MailInboxMessage{
			Message: domain.MailMessage{
				ID:          uuid.New(),
				WorkspaceID: account.WorkspaceID,
				AccountID:   account.ID,
				Folder:      domain.MailFolderInbox,
				MessageID:   messageID,
				InReplyTo:   inReplyTo,
				ThreadKey:   threadKey,
				Subject:     subject,
				From:        from,
				ReplyTo:     replyTo,
				To:          to,
				CC:          cc,
				BodyText:    bodyText,
				BodyHTML:    bodyHTML,
				Snippet:     snippetText(bodyText),
				Unread:      !fetched.Seen,
				UID:         int64(fetched.UID),
				SentAt:      sentAt,
			},
			Attachments: attachments,
		})
	}

	inserted, err := s.messageRepo.InsertInboxMessages(ctx, messages)
	if err != nil {
		return err
	}

	newLastUID := lastUID
	for _, fetched := range result.Messages {
		if int64(fetched.UID) > newLastUID {
			newLastUID = int64(fetched.UID)
		}
	}
	if err := s.messageRepo.UpdateSyncState(ctx, account.ID, result.UIDValidity, newLastUID, "", time.Now()); err != nil {
		return err
	}

	if len(inserted) > 0 {
		log.WithFields(log.Fields{
			"account":   account.EmailAddress,
			"new_count": len(inserted),
		}).Info("mail sync: new messages")
		s.hub.Broadcast(account.WorkspaceID, realtime.Event{
			Type: "mail.updated",
			Payload: map[string]any{
				"account_id": account.ID.String(),
				"folder":     domain.MailFolderInbox,
				"new_count":  len(inserted),
			},
		})
	}
	return nil
}

// replyToOf picks the best Reply-To target, falling back to the sender.
func replyToOf(parsed *mailclient.ParsedMessage) string {
	if parsed == nil {
		return ""
	}
	if parsed.ReplyTo != "" {
		return parsed.ReplyTo
	}
	return parsed.From.Address
}

func truncateSyncError(err error) string {
	msg := err.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return msg
}

func snippetText(body string) string {
	compact := strings.Join(strings.Fields(strings.TrimSpace(body)), " ")
	if len(compact) > 240 {
		return compact[:240]
	}
	return compact
}

func toDomainAddresses(in []mailaddress.Address) []domain.MailAddress {
	out := make([]domain.MailAddress, 0, len(in))
	for _, a := range in {
		out = append(out, domain.MailAddress{Name: a.Name, Address: a.Address})
	}
	return out
}
