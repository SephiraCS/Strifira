package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/pkg/crypto"
	"github.com/SephiraCS/strifira-backend/pkg/mailclient"
	"github.com/SephiraCS/strifira-backend/pkg/mailconfig"
	"github.com/SephiraCS/strifira-backend/pkg/mailoauth"
	"github.com/SephiraCS/strifira-backend/pkg/safego"
	"github.com/SephiraCS/strifira-backend/pkg/sanitize"
	mailaddress "github.com/emersion/go-message/mail"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// Errors surfaced as 4xx responses by the handler.
var (
	ErrMailNoRecipients     = errors.New("a message needs at least one recipient")
	ErrMailDraftNoAccount   = errors.New("choose an account before sending")
	ErrMailAttachmentTooBig = errors.New("attachment is too large")
	ErrMailConnectionFailed = errors.New("Não foi possível conectar ao provedor com segurança. Tente novamente em instantes.")
	ErrMailAuthentication   = errors.New("O provedor recusou a autenticação. Confira o endereço e a senha da caixa ou senha de aplicativo.")
	ErrMailOAuthRequired    = errors.New("Esta conta exige autorização no provedor. Use Autorizar conta para conectar ou renovar o acesso, sem informar uma senha.")
	ErrMailSendFailed       = errors.New("the server refused the message")
)

var ErrMailCredentialRequired = errors.New("Informe a senha da nova caixa para alterar o endereço. A credencial da conta anterior não será enviada a outro provedor.")

// Mail limits. Fixed in code: attachments are user data, not configuration.
const (
	MailMaxAttachmentBytes = 10 << 20 // 10 MiB per file
	MailMaxMessageBytes    = 25 << 20 // 25 MiB per message total
	mailSyncInterval       = 45 * time.Second
	mailPageSize           = 100
)

type mailDiscoverer interface {
	Discover(context.Context, string) (*mailconfig.Config, error)
}

// MailService owns mailbox accounts, the INBOX cache and compose/send flows.
type MailService struct {
	accountRepo          repository.MailAccountRepo
	messageRepo          repository.MailMessageRepo
	draftRepo            repository.MailDraftRepo
	hub                  *realtime.Hub
	encryptKey           []byte
	discovery            mailDiscoverer
	oauthRepo            *repository.MailOAuthRepository
	oauth                mailOAuthClient
	validateOAuthMailbox func(*domain.MailAccount, string) error
}

func NewMailService(
	accountRepo repository.MailAccountRepo,
	messageRepo repository.MailMessageRepo,
	draftRepo repository.MailDraftRepo,
	hub *realtime.Hub,
	encryptKey []byte,
) *MailService {
	s := &MailService{
		accountRepo: accountRepo,
		messageRepo: messageRepo,
		draftRepo:   draftRepo,
		hub:         hub,
		encryptKey:  encryptKey,
		discovery:   mailconfig.NewDiscoverer(),
	}
	s.validateOAuthMailbox = s.testOAuthConnections
	return s
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
	config, err := s.DiscoverAccount(ctx, req.EmailAddress)
	if err != nil {
		return nil, err
	}
	if config.Authentication == "oauth2" {
		return nil, ErrMailOAuthRequired
	}
	if err := s.testConnections(config.Incoming.Host, config.Incoming.Port, config.Incoming.Security, config.Outgoing.Host, config.Outgoing.Port, config.Outgoing.Security, config.Incoming.Username, config.Outgoing.Username, req.Password); err != nil {
		return nil, err
	}

	encrypted, err := crypto.Encrypt(req.Password, s.encryptKey)
	if err != nil {
		return nil, err
	}
	account := accountFromConfig(workspaceID, userID, config, req.DisplayName, req.Signature)
	account.AuthMethod = "password"
	account.PasswordEncrypted = encrypted
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

// DiscoverAccount returns only public connection metadata, never credentials.
func (s *MailService) DiscoverAccount(ctx context.Context, address string) (*mailconfig.Config, error) {
	config, err := s.discovery.Discover(ctx, address)
	if err != nil {
		return nil, err
	}
	copy := *config
	copy.OAuthProvider = mailoauth.ProviderFor(config)
	return &copy, nil
}

func (s *MailService) UpdateAccount(ctx context.Context, workspaceID, id uuid.UUID, req dto.UpdateMailAccountRequest) (*domain.MailAccount, error) {
	stored, err := s.accountRepo.GetByID(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	// Validate a copy; failed rediscovery/authentication must not mutate a linked account.
	account := *stored
	if account.AuthMethod == "oauth2" && (req.EmailAddress != "" && req.EmailAddress != account.EmailAddress || req.Password != nil && *req.Password != "") {
		return nil, ErrMailOAuthRequired
	}
	changedAddress := req.EmailAddress != "" && req.EmailAddress != account.EmailAddress
	if changedAddress {
		if req.Password == nil || *req.Password == "" {
			return nil, ErrMailCredentialRequired
		}
		config, err := s.DiscoverAccount(ctx, req.EmailAddress)
		if err != nil {
			return nil, err
		}
		if config.Authentication == "oauth2" {
			return nil, ErrMailOAuthRequired
		}
		account.EmailAddress = config.EmailAddress
		account.IMAPHost, account.IMAPPort, account.IMAPSecurity = config.Incoming.Host, config.Incoming.Port, config.Incoming.Security
		account.SMTPHost, account.SMTPPort, account.SMTPSecurity = config.Outgoing.Host, config.Outgoing.Port, config.Outgoing.Security
		account.Username = config.Incoming.Username
		account.SMTPUsername = config.Outgoing.Username
	}
	if req.DisplayName != nil {
		account.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.Signature != nil {
		account.Signature = *req.Signature
	}
	if changedAddress || req.Password != nil && *req.Password != "" {
		password, err := s.decryptPassword(&account)
		if req.Password != nil && *req.Password != "" {
			password, err = *req.Password, nil
		}
		if err != nil {
			return nil, err
		}
		if err := s.testConnections(account.IMAPHost, account.IMAPPort, account.IMAPSecurity, account.SMTPHost, account.SMTPPort, account.SMTPSecurity, account.Username, smtpUsername(&account), password); err != nil {
			return nil, err
		}
		encrypted, err := crypto.Encrypt(password, s.encryptKey)
		if err != nil {
			return nil, err
		}
		account.PasswordEncrypted = encrypted
	}
	if err := s.accountRepo.Update(ctx, &account); err != nil {
		return nil, err
	}
	return &account, nil
}

func (s *MailService) DeleteAccount(ctx context.Context, workspaceID, id uuid.UUID) error {
	account, err := s.accountRepo.GetByID(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	if account.AuthMethod == "oauth2" {
		return s.disconnectOAuth(ctx, account)
	}
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

func (s *MailService) testConnections(imapHost string, imapPort int, imapSecurity, smtpHost string, smtpPort int, smtpSecurity, username, outgoingUsername, password string) error {
	if err := mailclient.TestIMAPConnection(mailclient.FetchOptions{
		Host: imapHost, Port: imapPort, Security: imapSecurity, Username: username, Password: password,
	}); err != nil {
		return mailConnectionError(err)
	}
	if err := mailclient.TestSMTPConnection(mailclient.SendOptions{
		Host: smtpHost, Port: smtpPort, Security: smtpSecurity, Username: outgoingUsername, Password: password,
	}); err != nil {
		return mailConnectionError(err)
	}
	return nil
}

func mailConnectionError(err error) error {
	if errors.Is(err, mailclient.ErrAuthentication) {
		return ErrMailAuthentication
	}
	return ErrMailConnectionFailed
}

// decryptPassword returns the account password in memory.
func (s *MailService) decryptPassword(account *domain.MailAccount) (string, error) {
	return crypto.Decrypt(account.PasswordEncrypted, s.encryptKey)
}

func (s *MailService) fetchOptions(account *domain.MailAccount, password string) mailclient.FetchOptions {
	options := mailclient.FetchOptions{
		Host: account.IMAPHost, Port: account.IMAPPort, Security: account.IMAPSecurity,
		Username: account.Username, Password: password,
	}
	if account.AuthMethod == "oauth2" {
		options.Password, options.AccessToken = "", password
	}
	return options
}

func smtpUsername(account *domain.MailAccount) string {
	if account.SMTPUsername != "" {
		return account.SMTPUsername
	}
	return account.Username
}

func (s *MailService) sendOptions(account *domain.MailAccount, password string) mailclient.SendOptions {
	options := mailclient.SendOptions{
		Host: account.SMTPHost, Port: account.SMTPPort, Security: account.SMTPSecurity,
		Username: smtpUsername(account), Password: password,
	}
	if account.AuthMethod == "oauth2" {
		options.Password, options.AccessToken = "", password
	}
	return options
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
	uidValidity, lastUID, err := s.messageRepo.SyncState(ctx, account.ID)
	if err != nil {
		return err
	}
	password, err := s.credential(ctx, &account)
	if err != nil {
		msg := ErrMailConnectionFailed.Error()
		if errors.Is(err, mailoauth.ErrAuthorization) || errors.Is(err, mailoauth.ErrRegistration) || errors.Is(err, mailoauth.ErrUnavailable) {
			msg = err.Error()
		}
		_ = s.messageRepo.UpdateSyncState(ctx, account.ID, uidValidity, lastUID, msg, time.Now())
		return err
	}
	result, err := mailclient.FetchNewMessages(s.fetchOptions(&account, password), uint64(lastUID), uidValidity)
	now := time.Now()
	if err != nil {
		msg := accountConnectionError(&account, err).Error()
		_ = s.messageRepo.UpdateSyncState(ctx, account.ID, uidValidity, lastUID, msg, now)
		log.WithField("account_id", account.ID).Warn("mail sync failed")
		return accountConnectionError(&account, err)
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
