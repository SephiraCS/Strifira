package domain

import (
	"time"

	"github.com/google/uuid"
)

// Mail message folders. inbox holds messages fetched from the remote IMAP
// INBOX; sent holds messages composed inside the app. Archived and spam are
// row flags on top of the folder so moving mail between them never touches
// the remote server.
const (
	MailFolderInbox = "inbox"
	MailFolderSent  = "sent"
)

// Supported TLS modes for IMAP/SMTP connections.
const (
	MailSecurityNone     = "none"
	MailSecurityStartTLS = "starttls"
	MailSecurityTLS      = "tls"
)

// MailAddress is one RFC 5322 mailbox: a display name and an address.
type MailAddress struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// MailAccount is a workspace e-mail account (shared by workspace members).
type MailAccount struct {
	ID           uuid.UUID `json:"id" db:"id"`
	WorkspaceID  uuid.UUID `json:"workspace_id" db:"workspace_id"`
	CreatedBy    uuid.UUID `json:"created_by" db:"created_by"`
	EmailAddress string    `json:"email_address" db:"email_address"`
	DisplayName  string    `json:"display_name" db:"display_name"`
	IMAPHost     string    `json:"imap_host" db:"imap_host"`
	IMAPPort     int       `json:"imap_port" db:"imap_port"`
	IMAPSecurity string    `json:"imap_security" db:"imap_security"`
	SMTPHost     string    `json:"smtp_host" db:"smtp_host"`
	SMTPPort     int       `json:"smtp_port" db:"smtp_port"`
	SMTPSecurity string    `json:"smtp_security" db:"smtp_security"`
	Username     string    `json:"username" db:"username"`
	SMTPUsername string    `json:"smtp_username" db:"smtp_username"`
	// PasswordEncrypted is AES-256-GCM hex; never serialized to clients.
	PasswordEncrypted string     `json:"-" db:"password_encrypted"`
	Signature         string     `json:"signature" db:"signature"`
	UIDValidity       uint32     `json:"-" db:"uid_validity"`
	LastUID           int64      `json:"-" db:"last_uid"`
	LastSyncedAt      *time.Time `json:"last_synced_at" db:"last_synced_at"`
	LastSyncError     string     `json:"last_sync_error" db:"last_sync_error"`
	CreatedAt         time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at" db:"updated_at"`
}

// MailMessage is one RFC 5322 message cached locally, received or sent.
type MailMessage struct {
	ID          uuid.UUID `json:"id" db:"id"`
	WorkspaceID uuid.UUID `json:"workspace_id" db:"workspace_id"`
	AccountID   uuid.UUID `json:"account_id" db:"account_id"`
	Folder      string    `json:"folder" db:"folder"`
	MessageID   string    `json:"message_id" db:"message_id"`
	InReplyTo   string    `json:"in_reply_to" db:"in_reply_to"`
	// ThreadKey groups messages of one conversation: the root Message-ID of
	// the References chain, or the message's own Message-ID.
	ThreadKey string        `json:"thread_key" db:"thread_key"`
	Subject   string        `json:"subject" db:"subject"`
	From      MailAddress   `json:"from"`
	ReplyTo   string        `json:"reply_to" db:"reply_to"`
	To        []MailAddress `json:"to"`
	CC        []MailAddress `json:"cc"`
	BCC       []MailAddress `json:"bcc,omitempty"`
	// BodyText/BodyHTML are the decoded bodies; Snippet is a short plain
	// preview for list rendering.
	BodyText  string    `json:"body_text,omitempty" db:"body_text"`
	BodyHTML  string    `json:"body_html,omitempty" db:"body_html"`
	Snippet   string    `json:"snippet" db:"snippet"`
	Unread    bool      `json:"unread" db:"unread"`
	Starred   bool      `json:"starred" db:"starred"`
	Archived  bool      `json:"archived" db:"archived"`
	Spam      bool      `json:"spam" db:"spam"`
	UID       int64     `json:"-" db:"uid"`
	SentAt    time.Time `json:"sent_at" db:"sent_at"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	// HasAttachments is resolved by list queries (EXISTS subquery).
	HasAttachments bool `json:"has_attachments" db:"has_attachments"`
}

// MailAttachment is a file carried by a message or staged on a draft. The
// bytes stay in the database; clients download them by ID.
type MailAttachment struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	MessageID   *uuid.UUID `json:"message_id,omitempty" db:"message_id"`
	DraftID     *uuid.UUID `json:"draft_id,omitempty" db:"draft_id"`
	Filename    string     `json:"filename" db:"filename"`
	ContentType string     `json:"content_type" db:"content_type"`
	SizeBytes   int64      `json:"size_bytes" db:"size_bytes"`
	Content     []byte     `json:"-" db:"content"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
}

// MailDraft is an in-progress composition owned by its author.
type MailDraft struct {
	ID                   uuid.UUID     `json:"id" db:"id"`
	WorkspaceID          uuid.UUID     `json:"workspace_id" db:"workspace_id"`
	AccountID            *uuid.UUID    `json:"account_id,omitempty" db:"account_id"`
	UserID               uuid.UUID     `json:"user_id" db:"user_id"`
	ThreadKey            string        `json:"thread_key" db:"thread_key"`
	InReplyToMessageID   *uuid.UUID    `json:"in_reply_to_message_id,omitempty" db:"in_reply_to_message_id"`
	ForwardFromMessageID *uuid.UUID    `json:"forward_from_message_id,omitempty" db:"forward_from_message_id"`
	To                   []MailAddress `json:"to"`
	CC                   []MailAddress `json:"cc"`
	BCC                  []MailAddress `json:"bcc"`
	Subject              string        `json:"subject" db:"subject"`
	Body                 string        `json:"body" db:"body"`
	CreatedAt            time.Time     `json:"created_at" db:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at" db:"updated_at"`
}
