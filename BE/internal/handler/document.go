package handler

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/internal/service"
	"github.com/ku/ku-backend/pkg/response"
	"github.com/ku/ku-backend/pkg/validate"
	"github.com/labstack/echo/v4"
)

type DocumentHandler struct {
	docSvc   *service.DocumentService
	userRepo repository.UserRepo
}

func NewDocumentHandler(docSvc *service.DocumentService, userRepo repository.UserRepo) *DocumentHandler {
	return &DocumentHandler{docSvc: docSvc, userRepo: userRepo}
}

func (h *DocumentHandler) Create(c echo.Context) error {
	var req dto.CreateDocumentRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, formatDocValidationErrors(err))
	}
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	doc, err := h.docSvc.Create(c.Request().Context(), ws.ID, userID, req)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	tags, _ := h.docSvc.TagsFor(c.Request().Context(), ws.ID, doc.ID)
	return response.Success(c, http.StatusCreated, toDocumentResponse(doc, tags))
}

func (h *DocumentHandler) List(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	docs, tags, err := h.docSvc.List(c.Request().Context(), ws.ID)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	resp := make([]dto.DocumentTreeResponse, len(docs))
	for i := range docs {
		resp[i] = toDocumentTreeResponse(&docs[i], tags[docs[i].ID])
	}
	return response.Success(c, http.StatusOK, resp)
}

func (h *DocumentHandler) Get(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	doc, err := h.docSvc.Get(c.Request().Context(), ws.ID, docID)
	if err != nil {
		return docErrorResponse(c, err)
	}
	tags, _ := h.docSvc.TagsFor(c.Request().Context(), ws.ID, doc.ID)
	return response.Success(c, http.StatusOK, toDocumentResponse(doc, tags))
}

func (h *DocumentHandler) Update(c echo.Context) error {
	var req dto.UpdateDocumentRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, formatDocValidationErrors(err))
	}
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	doc, err := h.docSvc.Update(c.Request().Context(), ws.ID, docID, userID, req)
	if err != nil {
		return docErrorResponse(c, err)
	}
	tags, _ := h.docSvc.TagsFor(c.Request().Context(), ws.ID, doc.ID)
	return response.Success(c, http.StatusOK, toDocumentResponse(doc, tags))
}

func (h *DocumentHandler) Move(c echo.Context) error {
	var req dto.MoveDocumentRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	doc, err := h.docSvc.Move(c.Request().Context(), ws.ID, docID, userID, req)
	if err != nil {
		return docErrorResponse(c, err)
	}
	return response.Success(c, http.StatusOK, toDocumentResponse(doc, nil))
}

func (h *DocumentHandler) Delete(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	if err := h.docSvc.Delete(c.Request().Context(), ws.ID, docID, userID); err != nil {
		return docErrorResponse(c, err)
	}
	return response.Success(c, http.StatusOK, map[string]bool{"ok": true})
}

func (h *DocumentHandler) Restore(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	doc, err := h.docSvc.Restore(c.Request().Context(), ws.ID, docID)
	if err != nil {
		return docErrorResponse(c, err)
	}
	return response.Success(c, http.StatusOK, toDocumentResponse(doc, nil))
}

func (h *DocumentHandler) Purge(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	if err := h.docSvc.Purge(c.Request().Context(), ws.ID, docID); err != nil {
		return docErrorResponse(c, err)
	}
	return response.Success(c, http.StatusOK, map[string]bool{"ok": true})
}

func (h *DocumentHandler) Duplicate(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	doc, err := h.docSvc.Duplicate(c.Request().Context(), ws.ID, docID, userID)
	if err != nil {
		return docErrorResponse(c, err)
	}
	return response.Success(c, http.StatusCreated, toDocumentResponse(doc, nil))
}

func (h *DocumentHandler) ListTrash(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	docs, err := h.docSvc.ListTrash(c.Request().Context(), ws.ID)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	resp := make([]dto.DocumentTreeResponse, len(docs))
	for i := range docs {
		resp[i] = toDocumentTreeResponse(&docs[i], nil)
	}
	return response.Success(c, http.StatusOK, resp)
}

func (h *DocumentHandler) Search(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	docs, err := h.docSvc.Search(c.Request().Context(), ws.ID, c.QueryParam("q"))
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	resp := make([]dto.DocumentTreeResponse, len(docs))
	for i := range docs {
		resp[i] = toDocumentTreeResponse(&docs[i], nil)
	}
	return response.Success(c, http.StatusOK, resp)
}

func (h *DocumentHandler) ListRevisions(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	revs, err := h.docSvc.ListRevisions(c.Request().Context(), ws.ID, docID)
	if err != nil {
		return docErrorResponse(c, err)
	}
	resp := make([]dto.DocumentRevisionResponse, len(revs))
	for i := range revs {
		resp[i] = toDocumentRevisionResponse(&revs[i])
	}
	return response.Success(c, http.StatusOK, resp)
}

func (h *DocumentHandler) RestoreRevision(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	revisionID, err := uuid.Parse(c.Param("revisionId"))
	if err != nil {
		return response.NotFound(c, "Revision")
	}
	doc, err := h.docSvc.RestoreRevision(c.Request().Context(), ws.ID, docID, revisionID, userID)
	if err != nil {
		return docErrorResponse(c, err)
	}
	return response.Success(c, http.StatusOK, toDocumentResponse(doc, nil))
}

func (h *DocumentHandler) Backlinks(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	docs, err := h.docSvc.Backlinks(c.Request().Context(), ws.ID, docID)
	if err != nil {
		return docErrorResponse(c, err)
	}
	resp := make([]dto.DocumentBacklinkResponse, len(docs))
	for i := range docs {
		resp[i] = dto.DocumentBacklinkResponse{ID: docs[i].ID.String(), Title: docs[i].Title, Icon: docs[i].Icon, UpdatedAt: docs[i].UpdatedAt}
	}
	return response.Success(c, http.StatusOK, resp)
}

func (h *DocumentHandler) Graph(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	graph, err := h.docSvc.Graph(c.Request().Context(), ws.ID)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusOK, graph)
}

func (h *DocumentHandler) ListTags(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	tags, err := h.docSvc.ListTags(c.Request().Context(), ws.ID)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	resp := make([]dto.DocumentTagResponse, len(tags))
	for i := range tags {
		resp[i] = dto.DocumentTagResponse{ID: tags[i].ID.String(), Name: tags[i].Name}
	}
	return response.Success(c, http.StatusOK, resp)
}

func (h *DocumentHandler) CreateTag(c echo.Context) error {
	var req dto.CreateDocumentTagRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, formatDocValidationErrors(err))
	}
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	tag, err := h.docSvc.CreateTag(c.Request().Context(), ws.ID, userID, req)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusCreated, dto.DocumentTagResponse{ID: tag.ID.String(), Name: tag.Name})
}

func (h *DocumentHandler) DeleteTag(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	tagID, err := uuid.Parse(c.Param("tagId"))
	if err != nil {
		return response.NotFound(c, "Tag")
	}
	if err := h.docSvc.DeleteTag(c.Request().Context(), ws.ID, tagID); err != nil {
		return response.InternalErrorErr(c, err)
	}
	return response.Success(c, http.StatusOK, map[string]bool{"ok": true})
}

func (h *DocumentHandler) SetTags(c echo.Context) error {
	var req dto.SetDocumentTagsRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	tags, err := h.docSvc.SetTags(c.Request().Context(), ws.ID, userID, docID, req.Tags)
	if err != nil {
		return docErrorResponse(c, err)
	}
	resp := make([]dto.DocumentTagResponse, len(tags))
	for i := range tags {
		resp[i] = dto.DocumentTagResponse{ID: tags[i].ID.String(), Name: tags[i].Name}
	}
	return response.Success(c, http.StatusOK, resp)
}

func (h *DocumentHandler) ListComments(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	comments, err := h.docSvc.ListComments(c.Request().Context(), ws.ID, docID)
	if err != nil {
		return docErrorResponse(c, err)
	}
	resp := make([]dto.DocumentCommentResponse, 0, len(comments))
	for i := range comments {
		resp = append(resp, h.toDocumentCommentResponse(c, &comments[i]))
	}
	return response.Success(c, http.StatusOK, resp)
}

func (h *DocumentHandler) CreateComment(c echo.Context) error {
	var req dto.CreateDocumentCommentRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, formatDocValidationErrors(err))
	}
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	comment, err := h.docSvc.CreateComment(c.Request().Context(), ws.ID, docID, userID, req)
	if err != nil {
		return docErrorResponse(c, err)
	}
	return response.Success(c, http.StatusCreated, h.toDocumentCommentResponse(c, comment))
}

func (h *DocumentHandler) ResolveComment(c echo.Context) error {
	return h.setCommentResolved(c, true)
}

func (h *DocumentHandler) ReopenComment(c echo.Context) error {
	return h.setCommentResolved(c, false)
}

func (h *DocumentHandler) setCommentResolved(c echo.Context, resolved bool) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)
	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	commentID, err := uuid.Parse(c.Param("commentId"))
	if err != nil {
		return response.NotFound(c, "Comment")
	}
	var err2 error
	if resolved {
		err2 = h.docSvc.ResolveComment(c.Request().Context(), ws.ID, docID, commentID, userID)
	} else {
		err2 = h.docSvc.ReopenComment(c.Request().Context(), ws.ID, docID, commentID, userID)
	}
	if err2 != nil {
		return docErrorResponse(c, err2)
	}
	return response.Success(c, http.StatusOK, map[string]bool{"ok": true})
}

func (h *DocumentHandler) toDocumentCommentResponse(c echo.Context, comment *domain.DocumentComment) dto.DocumentCommentResponse {
	resp := dto.DocumentCommentResponse{
		ID:         comment.ID.String(),
		DocumentID: comment.DocumentID.String(),
		UserID:     comment.UserID.String(),
		Body:       comment.Body,
		ParentID:   nil,
		ResolvedAt: comment.ResolvedAt,
		CreatedAt:  comment.CreatedAt,
		UpdatedAt:  comment.UpdatedAt,
	}
	if comment.ParentID != nil {
		pid := comment.ParentID.String()
		resp.ParentID = &pid
	}
	if comment.ParentID == nil && h.userRepo != nil {
		replies, err := h.docSvc.ListCommentReplies(c.Request().Context(), comment.ID)
		if err == nil {
			for i := range replies {
				resp.Replies = append(resp.Replies, h.toDocumentCommentResponse(c, &replies[i]))
			}
		}
	}
	return resp
}

func docErrorResponse(c echo.Context, err error) error {
	if errors.Is(err, service.ErrDocumentNotFound) {
		return response.NotFound(c, "Document")
	}
	if errors.Is(err, service.ErrDocumentCycle) {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Cannot move a document under itself")
	}
	if errors.Is(err, service.ErrDocumentNotTrashed) {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Document must be in trash before permanent delete")
	}
	return response.InternalErrorErr(c, err)
}

func formatDocValidationErrors(err error) []dto.ErrorDetail {
	details := make([]dto.ErrorDetail, 0)
	for _, e := range validate.FormatErrors(err) {
		details = append(details, dto.ErrorDetail{Field: e["field"], Message: e["message"]})
	}
	return details
}

func toDocumentResponse(doc *domain.Document, tags []string) dto.DocumentResponse {
	resp := dto.DocumentResponse{
		ID:          doc.ID.String(),
		WorkspaceID: doc.WorkspaceID.String(),
		Title:       doc.Title,
		ContentHTML: doc.ContentHTML,
		ContentText: doc.ContentText,
		Position:    doc.Position,
		IsTemplate:  doc.IsTemplate,
		CreatedBy:   doc.CreatedBy.String(),
		CreatedAt:   doc.CreatedAt,
		UpdatedAt:   doc.UpdatedAt,
		Tags:        tags,
	}
	if doc.ParentID != nil {
		pid := doc.ParentID.String()
		resp.ParentID = &pid
	}
	if doc.Icon != nil {
		resp.Icon = doc.Icon
	}
	if doc.UpdatedBy != nil {
		uid := doc.UpdatedBy.String()
		resp.UpdatedBy = &uid
	}
	if doc.DeletedAt != nil {
		resp.DeletedAt = doc.DeletedAt
	}
	if doc.DeletedBy != nil {
		uid := doc.DeletedBy.String()
		resp.DeletedBy = &uid
	}
	if resp.Tags == nil {
		resp.Tags = []string{}
	}
	return resp
}

func toDocumentTreeResponse(doc *domain.Document, tags []string) dto.DocumentTreeResponse {
	resp := dto.DocumentTreeResponse{
		ID:         doc.ID.String(),
		Title:      doc.Title,
		Position:   doc.Position,
		IsTemplate: doc.IsTemplate,
		UpdatedAt:  doc.UpdatedAt,
		Tags:       tags,
	}
	if doc.ParentID != nil {
		pid := doc.ParentID.String()
		resp.ParentID = &pid
	}
	if doc.Icon != nil {
		resp.Icon = doc.Icon
	}
	if resp.Tags == nil {
		resp.Tags = []string{}
	}
	return resp
}

func toDocumentRevisionResponse(rev *domain.DocumentRevision) dto.DocumentRevisionResponse {
	resp := dto.DocumentRevisionResponse{
		ID:          rev.ID.String(),
		DocumentID:  rev.DocumentID.String(),
		Title:       rev.Title,
		ContentHTML: rev.ContentHTML,
		ContentText: rev.ContentText,
		CreatedAt:   rev.CreatedAt,
	}
	if rev.Icon != nil {
		resp.Icon = rev.Icon
	}
	if rev.EditedBy != nil {
		uid := rev.EditedBy.String()
		resp.EditedBy = &uid
	}
	return resp
}
