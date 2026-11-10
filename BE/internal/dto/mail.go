package dto

import "time"

// MailAddress is one RFC 5322 mailbox as the UI sends/receives it.
type MailAddress struct {
	Name    string `json:"name"`
	Address string `json:"address" validate:"required,email"`
}

type CreateMailAccountRequest struct {
	EmailAddress string `json:"email_address" validate:"required,email,max=254"`
	DisplayName  string `json:"display_name" validate:"max=120"`
	Password     string `json:"password" validate:"required,max=1024"`
	Signature    string `json:"signature" validate:"max=4000"`
}

type DiscoverMailAccountRequest struct {
	EmailAddress string `json:"email_address" validate:"required,email,max=254"`
}

type StartMailOAuthRequest struct {
	EmailAddress string `json:"email_address" validate:"required,email,max=254"`
	AccountID    string `json:"account_id" validate:"omitempty,uuid4"`
	DisplayName  string `json:"display_name" validate:"max=120"`
	Signature    string `json:"signature" validate:"max=4000"`
}

type CompleteMailOAuthRequest struct {
	State         string `json:"state" validate:"required,min=32,max=128"`
	Code          string `json:"code" validate:"max=4096"`
	ProviderError string `json:"provider_error" validate:"max=128"`
}

type UpdateMailAccountRequest struct {
	EmailAddress string  `json:"email_address" validate:"omitempty,email,max=254"`
	DisplayName  *string `json:"display_name" validate:"omitempty,max=120"`
	Password     *string `json:"password" validate:"omitempty,max=1024"`
	Signature    *string `json:"signature" validate:"omitempty,max=4000"`
}

type MailAccountResponse struct {
	AuthMethod    string     `json:"auth_method"`
	OAuthProvider string     `json:"oauth_provider,omitempty"`
	ID            string     `json:"id"`
	EmailAddress  string     `json:"email_address"`
	DisplayName   string     `json:"display_name"`
	IMAPHost      string     `json:"imap_host"`
	IMAPPort      int        `json:"imap_port"`
	IMAPSecurity  string     `json:"imap_security"`
	SMTPHost      string     `json:"smtp_host"`
	SMTPPort      int        `json:"smtp_port"`
	SMTPSecurity  string     `json:"smtp_security"`
	Username      string     `json:"username"`
	Signature     string     `json:"signature"`
	LastSyncedAt  *time.Time `json:"last_synced_at,omitempty"`
	LastSyncError string     `json:"last_sync_error"`
	CreatedAt     time.Time  `json:"created_at"`
}

type MailAccountListResponse struct {
	Accounts []MailAccountResponse `json:"accounts"`
}

// MailMessageSummary is the list-row shape: no body, no attachments.
type MailMessageSummary struct {
	ID             string        `json:"id"`
	AccountID      string        `json:"account_id"`
	Folder         string        `json:"folder"`
	ThreadKey      string        `json:"thread_key"`
	Subject        string        `json:"subject"`
	From           MailAddress   `json:"from"`
	To             []MailAddress `json:"to"`
	CC             []MailAddress `json:"cc,omitempty"`
	Snippet        string        `json:"snippet"`
	Unread         bool          `json:"unread"`
	Starred        bool          `json:"starred"`
	Archived       bool          `json:"archived"`
	Spam           bool          `json:"spam"`
	HasAttachments bool          `json:"has_attachments"`
	SentAt         time.Time     `json:"sent_at"`
}

type MailMessageListResponse struct {
	Messages []MailMessageSummary `json:"messages"`
}

// MailMessageDetail is the reading-pane shape.
type MailMessageDetail struct {
	MailMessageSummary
	ReplyTo     string               `json:"reply_to"`
	BodyText    string               `json:"body_text"`
	BodyHTML    string               `json:"body_html"`
	InReplyTo   string               `json:"in_reply_to"`
	Attachments []MailAttachmentMeta `json:"attachments"`
}

type MailAttachmentMeta struct {
	ID          string `json:"id"`
	MessageID   string `json:"message_id,omitempty"`
	DraftID     string `json:"draft_id,omitempty"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

type UpdateMailMessageRequest struct {
	Unread   *bool `json:"unread" validate:"omitempty"`
	Starred  *bool `json:"starred" validate:"omitempty"`
	Archived *bool `json:"archived" validate:"omitempty"`
	Spam     *bool `json:"spam" validate:"omitempty"`
}

type CreateMailDraftRequest struct {
	AccountID            string        `json:"account_id" validate:"omitempty,uuid4"`
	To                   []MailAddress `json:"to" validate:"omitempty,dive"`
	CC                   []MailAddress `json:"cc" validate:"omitempty,dive"`
	BCC                  []MailAddress `json:"bcc" validate:"omitempty,dive"`
	Subject              string        `json:"subject" validate:"max=500"`
	Body                 string        `json:"body" validate:"max=200000"`
	InReplyToMessageID   string        `json:"in_reply_to_message_id" validate:"omitempty,uuid4"`
	ForwardFromMessageID string        `json:"forward_from_message_id" validate:"omitempty,uuid4"`
}

type UpdateMailDraftRequest struct {
	AccountID string        `json:"account_id" validate:"omitempty,uuid4"`
	To        []MailAddress `json:"to" validate:"omitempty,dive"`
	CC        []MailAddress `json:"cc" validate:"omitempty,dive"`
	BCC       []MailAddress `json:"bcc" validate:"omitempty,dive"`
	Subject   *string       `json:"subject" validate:"omitempty,max=500"`
	Body      *string       `json:"body" validate:"omitempty,max=200000"`
}

type MailDraftResponse struct {
	ID                   string               `json:"id"`
	AccountID            string               `json:"account_id,omitempty"`
	To                   []MailAddress        `json:"to"`
	CC                   []MailAddress        `json:"cc,omitempty"`
	BCC                  []MailAddress        `json:"bcc,omitempty"`
	Subject              string               `json:"subject"`
	Body                 string               `json:"body"`
	InReplyToMessageID   string               `json:"in_reply_to_message_id,omitempty"`
	ForwardFromMessageID string               `json:"forward_from_message_id,omitempty"`
	Attachments          []MailAttachmentMeta `json:"attachments"`
	UpdatedAt            time.Time            `json:"updated_at"`
}

type MailDraftListResponse struct {
	Drafts []MailDraftResponse `json:"drafts"`
}

type SendDraftResponse struct {
	MessageID string `json:"message_id"`
}
