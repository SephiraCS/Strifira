package service

import "errors"

// Not-found errors for workspace-scoped resources. A row that exists in a
// different workspace is reported exactly like a missing row so callers
// cannot probe foreign IDs; handlers map these to HTTP 404.
var (
	ErrWebhookNotFound    = errors.New("webhook not found")
	ErrRelationNotFound   = errors.New("relation not found")
	ErrTemplateNotFound   = errors.New("template not found")
	ErrCycleNotFound      = errors.New("cycle not found")
	ErrTeamStatusNotFound = errors.New("status not found")
	ErrSharedLinkNotFound = errors.New("shared link not found")
)
