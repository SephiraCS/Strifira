package dto

import "time"

type CreateDocumentRequest struct {
	Title      string   `json:"title" validate:"max=500"`
	Icon       *string  `json:"icon" validate:"omitempty,max=16"`
	ParentID   *string  `json:"parent_id" validate:"omitempty,uuid"`
	ContentMD  string   `json:"content_md" validate:"max=1000000"`
	IsTemplate bool     `json:"is_template"`
	Tags       []string `json:"tags" validate:"max=50"`
}

type UpdateDocumentRequest struct {
	Title       *string  `json:"title" validate:"omitempty,max=500"`
	Icon        *string  `json:"icon" validate:"omitempty,max=16"`
	ContentHTML *string  `json:"content_html" validate:"omitempty,max=2000000"`
	ContentText *string  `json:"content_text" validate:"omitempty,max=400000"`
	WikiTitles  []string `json:"wiki_titles" validate:"max=200"`
	IsTemplate  *bool    `json:"is_template"`
}

type MoveDocumentRequest struct {
	ParentID *string  `json:"parent_id" validate:"omitempty,uuid"`
	Position *float64 `json:"position"`
}

type DocumentResponse struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	ParentID    *string    `json:"parent_id,omitempty"`
	Title       string     `json:"title"`
	Icon        *string    `json:"icon,omitempty"`
	ContentHTML string     `json:"content_html"`
	ContentText string     `json:"content_text"`
	Position    float64    `json:"position"`
	IsTemplate  bool       `json:"is_template"`
	CreatedBy   string     `json:"created_by"`
	UpdatedBy   *string    `json:"updated_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
	DeletedBy   *string    `json:"deleted_by,omitempty"`
	Tags        []string   `json:"tags"`
}

// DocumentTreeResponse is the compact list entry used to build trees; content
// is omitted so a large workspace stays one cheap request.
type DocumentTreeResponse struct {
	ID         string    `json:"id"`
	ParentID   *string   `json:"parent_id,omitempty"`
	Title      string    `json:"title"`
	Icon       *string   `json:"icon,omitempty"`
	Position   float64   `json:"position"`
	IsTemplate bool      `json:"is_template"`
	UpdatedAt  time.Time `json:"updated_at"`
	Tags       []string  `json:"tags"`
}

type DocumentRevisionResponse struct {
	ID          string    `json:"id"`
	DocumentID  string    `json:"document_id"`
	Title       string    `json:"title"`
	Icon        *string   `json:"icon,omitempty"`
	ContentHTML string    `json:"content_html"`
	ContentText string    `json:"content_text"`
	EditedBy    *string   `json:"edited_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type DocumentBacklinkResponse struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Icon      *string   `json:"icon,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type DocumentGraphResponse struct {
	Nodes []DocumentGraphNode `json:"nodes"`
	Edges []DocumentGraphEdge `json:"edges"`
}

type DocumentGraphNode struct {
	ID    string  `json:"id"`
	Title string  `json:"title"`
	Icon  *string `json:"icon,omitempty"`
}

type DocumentGraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type CreateDocumentTagRequest struct {
	Name  string  `json:"name" validate:"required,min=1,max=50"`
	Color *string `json:"color"`
}

type DocumentTagResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SetDocumentTagsRequest struct {
	Tags []string `json:"tags" validate:"max=50"`
}

type CreateDocumentCommentRequest struct {
	Body     string  `json:"body" validate:"required,min=1,max=20000"`
	ParentID *string `json:"parent_id" validate:"omitempty,uuid"`
}

type DocumentCommentResponse struct {
	ID         string                    `json:"id"`
	DocumentID string                    `json:"document_id"`
	UserID     string                    `json:"user_id"`
	Body       string                    `json:"body"`
	ParentID   *string                   `json:"parent_id,omitempty"`
	ResolvedAt *time.Time                `json:"resolved_at,omitempty"`
	CreatedAt  time.Time                 `json:"created_at"`
	UpdatedAt  time.Time                 `json:"updated_at"`
	Replies    []DocumentCommentResponse `json:"replies,omitempty"`
}

type DuplicateDocumentResponse struct {
	Document DocumentResponse `json:"document"`
}
