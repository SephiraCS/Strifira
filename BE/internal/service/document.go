package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/realtime"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/pkg/sanitize"
)

const (
	// documentPositionStep spaces sibling inserts so the UI can place a page
	// between two others by averaging positions without touching siblings.
	documentPositionStep = 1024.0
	// revisionMinInterval rate-limits history snapshots; editing bursts still
	// leave a trail at most this dense.
	revisionMinInterval = 10 * time.Minute
	documentSearchLimit = 20
)

var (
	ErrDocumentNotFound   = errors.New("document not found")
	ErrDocumentCycle      = errors.New("cannot move a document under itself")
	ErrDocumentNotTrashed = errors.New("document must be in trash before permanent delete")
)

type DocumentService struct {
	docRepo  repository.DocumentRepo
	hub      *realtime.Hub
	notifSvc *NotificationService
	docSync  *DocSyncService
}

func NewDocumentService(docRepo repository.DocumentRepo, hub *realtime.Hub, notifSvc *NotificationService, docSync *DocSyncService) *DocumentService {
	return &DocumentService{docRepo: docRepo, hub: hub, notifSvc: notifSvc, docSync: docSync}
}

func (s *DocumentService) Create(ctx context.Context, workspaceID, userID uuid.UUID, req dto.CreateDocumentRequest) (*domain.Document, error) {
	doc := &domain.Document{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		Title:       strings.TrimSpace(req.Title),
		Icon:        req.Icon,
		IsTemplate:  req.IsTemplate,
		CreatedBy:   userID,
	}
	if req.ContentMD != "" {
		doc.ContentHTML = sanitize.SanitizeEditorContent(req.ContentMD)
	}
	if req.ParentID != nil && *req.ParentID != "" {
		pid, err := uuid.Parse(*req.ParentID)
		if err != nil {
			return nil, err
		}
		parent, err := s.docRepo.GetByID(ctx, pid)
		if err != nil || parent == nil || parent.WorkspaceID != workspaceID || parent.DeletedAt != nil {
			return nil, ErrDocumentNotFound
		}
		doc.ParentID = &pid
	}

	position, err := s.nextPosition(ctx, workspaceID, doc.ParentID)
	if err != nil {
		return nil, err
	}
	doc.Position = position

	if err := s.docRepo.Create(ctx, doc); err != nil {
		return nil, err
	}

	if len(req.Tags) > 0 {
		if _, err := s.resolveAndApplyTags(ctx, workspaceID, userID, doc.ID, req.Tags); err != nil {
			return nil, err
		}
	}

	s.hub.Broadcast(workspaceID, realtime.Event{
		Type:    "doc.created",
		Payload: map[string]any{"document_id": doc.ID.String()},
	})
	return doc, nil
}

func (s *DocumentService) Get(ctx context.Context, workspaceID, id uuid.UUID) (*domain.Document, error) {
	doc, err := s.docRepo.GetByID(ctx, id)
	if err != nil || doc == nil || doc.WorkspaceID != workspaceID {
		return nil, ErrDocumentNotFound
	}
	return doc, nil
}

func (s *DocumentService) List(ctx context.Context, workspaceID uuid.UUID) ([]domain.Document, map[uuid.UUID][]string, error) {
	docs, err := s.docRepo.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]uuid.UUID, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	tags, err := s.docRepo.ListTagsForDocuments(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	return docs, tags, nil
}

func (s *DocumentService) ListTrash(ctx context.Context, workspaceID uuid.UUID) ([]domain.Document, error) {
	return s.docRepo.ListDeleted(ctx, workspaceID)
}

func (s *DocumentService) Search(ctx context.Context, workspaceID uuid.UUID, q string) ([]domain.Document, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	return s.docRepo.Search(ctx, workspaceID, q, documentSearchLimit)
}

// Update applies metadata/content patches from the editor. The HTML snapshot
// is server-sanitized; when it changes, a revision may be recorded and the
// resolved [[wiki links]] are refreshed for backlinks and the graph view.
func (s *DocumentService) Update(ctx context.Context, workspaceID, id, userID uuid.UUID, req dto.UpdateDocumentRequest) (*domain.Document, error) {
	doc, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}

	contentChanged := false
	if req.Title != nil {
		doc.Title = strings.TrimSpace(*req.Title)
	}
	if req.Icon != nil {
		icon := strings.TrimSpace(*req.Icon)
		if icon == "" {
			doc.Icon = nil
		} else {
			doc.Icon = &icon
		}
	}
	if req.IsTemplate != nil {
		doc.IsTemplate = *req.IsTemplate
	}
	if req.ContentHTML != nil {
		cleaned := sanitize.SanitizeEditorContent(*req.ContentHTML)
		if cleaned != doc.ContentHTML {
			contentChanged = true
			doc.ContentHTML = cleaned
		}
		if req.ContentText != nil {
			doc.ContentText = *req.ContentText
		} else {
			doc.ContentText = sanitize.PlainText(sanitize.StripHTML(cleaned))
		}
	}
	doc.UpdatedBy = &userID

	if err := s.docRepo.Update(ctx, doc); err != nil {
		return nil, err
	}

	if contentChanged {
		s.maybeCreateRevision(ctx, doc, userID)
	}
	if req.WikiTitles != nil {
		if err := s.refreshLinks(ctx, workspaceID, doc.ID, req.WikiTitles); err != nil {
			return nil, err
		}
	}

	s.hub.Broadcast(workspaceID, realtime.Event{
		Type:    "doc.updated",
		Payload: map[string]any{"document_id": doc.ID.String()},
	})
	return doc, nil
}

func (s *DocumentService) Move(ctx context.Context, workspaceID, id, userID uuid.UUID, req dto.MoveDocumentRequest) (*domain.Document, error) {
	doc, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}

	if req.ParentID != nil {
		if *req.ParentID == "" {
			doc.ParentID = nil
		} else {
			pid, err := uuid.Parse(*req.ParentID)
			if err != nil {
				return nil, err
			}
			if pid == doc.ID {
				return nil, ErrDocumentCycle
			}
			parent, err := s.docRepo.GetByID(ctx, pid)
			if err != nil || parent == nil || parent.WorkspaceID != workspaceID || parent.DeletedAt != nil {
				return nil, ErrDocumentNotFound
			}
			if err := s.ensureNoCycle(ctx, doc.ID, parent); err != nil {
				return nil, err
			}
			doc.ParentID = &pid
		}
	}

	if req.Position != nil {
		doc.Position = *req.Position
	} else {
		pos, err := s.nextPosition(ctx, workspaceID, doc.ParentID)
		if err != nil {
			return nil, err
		}
		doc.Position = pos
	}
	doc.UpdatedBy = &userID
	if err := s.docRepo.Update(ctx, doc); err != nil {
		return nil, err
	}

	s.hub.Broadcast(workspaceID, realtime.Event{
		Type:    "doc.moved",
		Payload: map[string]any{"document_id": doc.ID.String()},
	})
	return doc, nil
}

func (s *DocumentService) Delete(ctx context.Context, workspaceID, id, userID uuid.UUID) error {
	doc, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	if err := s.docRepo.MarkDeleted(ctx, workspaceID, doc.ID, userID); err != nil {
		return err
	}
	s.hub.Broadcast(workspaceID, realtime.Event{
		Type:    "doc.deleted",
		Payload: map[string]any{"document_id": doc.ID.String()},
	})
	return nil
}

func (s *DocumentService) Restore(ctx context.Context, workspaceID, id uuid.UUID) (*domain.Document, error) {
	doc, err := s.docRepo.Restore(ctx, workspaceID, id)
	if err != nil || doc == nil || doc.WorkspaceID != workspaceID {
		return nil, ErrDocumentNotFound
	}
	s.hub.Broadcast(workspaceID, realtime.Event{
		Type:    "doc.restored",
		Payload: map[string]any{"document_id": doc.ID.String()},
	})
	return doc, nil
}

// Purge permanently removes a trashed document (and its descendants).
func (s *DocumentService) Purge(ctx context.Context, workspaceID, id uuid.UUID) error {
	doc, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	if doc.DeletedAt == nil {
		return ErrDocumentNotTrashed
	}
	if err := s.docRepo.Delete(ctx, workspaceID, doc.ID); err != nil {
		return err
	}
	s.hub.Broadcast(workspaceID, realtime.Event{
		Type:    "doc.purged",
		Payload: map[string]any{"document_id": doc.ID.String()},
	})
	return nil
}

func (s *DocumentService) Duplicate(ctx context.Context, workspaceID, id, userID uuid.UUID) (*domain.Document, error) {
	src, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	dst := &domain.Document{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		ParentID:    src.ParentID,
		Title:       duplicateTitle(src.Title),
		Icon:        src.Icon,
		ContentHTML: src.ContentHTML,
		ContentText: src.ContentText,
		IsTemplate:  false,
		CreatedBy:   userID,
	}
	position, err := s.nextPosition(ctx, workspaceID, src.ParentID)
	if err != nil {
		return nil, err
	}
	dst.Position = position

	if err := s.docRepo.Duplicate(ctx, src, dst); err != nil {
		return nil, err
	}
	s.hub.Broadcast(workspaceID, realtime.Event{
		Type:    "doc.created",
		Payload: map[string]any{"document_id": dst.ID.String()},
	})
	return dst, nil
}

func (s *DocumentService) ListRevisions(ctx context.Context, workspaceID, id uuid.UUID) ([]domain.DocumentRevision, error) {
	if _, err := s.Get(ctx, workspaceID, id); err != nil {
		return nil, err
	}
	return s.docRepo.ListRevisions(ctx, id, 50)
}

// RestoreRevision snapshots the current state first (so restoring is itself
// undoable), then rewinds the document to the chosen revision.
func (s *DocumentService) RestoreRevision(ctx context.Context, workspaceID, id, revisionID, userID uuid.UUID) (*domain.Document, error) {
	doc, err := s.Get(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	rev, err := s.docRepo.GetRevisionByID(ctx, revisionID)
	if err != nil || rev == nil || rev.DocumentID != doc.ID {
		return nil, ErrDocumentNotFound
	}

	s.maybeCreateRevision(ctx, doc, userID)

	doc.Title = rev.Title
	doc.Icon = rev.Icon
	doc.ContentHTML = rev.ContentHTML
	doc.ContentText = rev.ContentText
	doc.UpdatedBy = &userID
	if err := s.docRepo.Update(ctx, doc); err != nil {
		return nil, err
	}
	// The restored HTML is now authoritative: wipe the Yjs update log and
	// bounce live editors, otherwise their stale Yjs state (replayed from
	// the old log on reconnect, or pushed by their next autosave) would
	// clobber the restore.
	if s.docSync != nil {
		s.docSync.ResetDocument(doc.ID)
	}
	s.hub.Broadcast(workspaceID, realtime.Event{
		Type:    "doc.updated",
		Payload: map[string]any{"document_id": doc.ID.String()},
	})
	return doc, nil
}

func (s *DocumentService) Backlinks(ctx context.Context, workspaceID, id uuid.UUID) ([]domain.Document, error) {
	if _, err := s.Get(ctx, workspaceID, id); err != nil {
		return nil, err
	}
	return s.docRepo.ListBacklinks(ctx, id)
}

func (s *DocumentService) Graph(ctx context.Context, workspaceID uuid.UUID) (dto.DocumentGraphResponse, error) {
	docs, err := s.docRepo.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return dto.DocumentGraphResponse{}, err
	}
	links, err := s.docRepo.ListLinksByWorkspace(ctx, workspaceID)
	if err != nil {
		return dto.DocumentGraphResponse{}, err
	}
	graph := dto.DocumentGraphResponse{
		Nodes: make([]dto.DocumentGraphNode, 0, len(docs)),
		Edges: make([]dto.DocumentGraphEdge, 0, len(links)),
	}
	for _, d := range docs {
		graph.Nodes = append(graph.Nodes, dto.DocumentGraphNode{ID: d.ID.String(), Title: d.Title, Icon: d.Icon})
	}
	for _, l := range links {
		graph.Edges = append(graph.Edges, dto.DocumentGraphEdge{Source: l.SourceID.String(), Target: l.TargetID.String()})
	}
	return graph, nil
}

func (s *DocumentService) ListTags(ctx context.Context, workspaceID uuid.UUID) ([]domain.DocumentTag, error) {
	return s.docRepo.ListTags(ctx, workspaceID)
}

// TagsFor returns the tag names attached to one document.
func (s *DocumentService) TagsFor(ctx context.Context, workspaceID, documentID uuid.UUID) ([]string, error) {
	tags, err := s.docRepo.ListTagsForDocument(ctx, documentID)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Name
	}
	return names, nil
}

func (s *DocumentService) CreateTag(ctx context.Context, workspaceID, userID uuid.UUID, req dto.CreateDocumentTagRequest) (*domain.DocumentTag, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, errors.New("tag name is required")
	}
	if existing, err := s.docRepo.GetTagByName(ctx, workspaceID, name); err == nil && existing != nil {
		return existing, nil
	}
	tag := &domain.DocumentTag{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		Name:        name,
		Color:       req.Color,
		CreatedBy:   userID,
	}
	if err := s.docRepo.CreateTag(ctx, tag); err != nil {
		return nil, err
	}
	return tag, nil
}

func (s *DocumentService) DeleteTag(ctx context.Context, workspaceID, tagID uuid.UUID) error {
	return s.docRepo.DeleteTag(ctx, workspaceID, tagID)
}

// SetTags applies tag names to a document, creating missing tags on the fly.
func (s *DocumentService) SetTags(ctx context.Context, workspaceID, userID, documentID uuid.UUID, names []string) ([]domain.DocumentTag, error) {
	if _, err := s.Get(ctx, workspaceID, documentID); err != nil {
		return nil, err
	}
	names, err := s.resolveAndApplyTags(ctx, workspaceID, userID, documentID, names)
	if err != nil {
		return nil, err
	}
	s.hub.Broadcast(workspaceID, realtime.Event{
		Type:    "doc.updated",
		Payload: map[string]any{"document_id": documentID.String()},
	})
	return s.docRepo.ListTagsForDocument(ctx, documentID)
}

func (s *DocumentService) resolveAndApplyTags(ctx context.Context, workspaceID, userID, documentID uuid.UUID, names []string) ([]string, error) {
	tagIDs := make([]uuid.UUID, 0, len(names))
	resolved := make([]string, 0, len(names))
	seen := make(map[string]bool)
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		tag, err := s.docRepo.GetTagByName(ctx, workspaceID, name)
		if err != nil || tag == nil {
			tag = &domain.DocumentTag{
				ID:          uuid.New(),
				WorkspaceID: workspaceID,
				Name:        name,
				CreatedBy:   userID,
			}
			if err := s.docRepo.CreateTag(ctx, tag); err != nil {
				return nil, err
			}
		}
		tagIDs = append(tagIDs, tag.ID)
		resolved = append(resolved, tag.Name)
	}
	if err := s.docRepo.SetDocumentTags(ctx, documentID, tagIDs); err != nil {
		return nil, err
	}
	return resolved, nil
}

func (s *DocumentService) CreateComment(ctx context.Context, workspaceID, documentID, userID uuid.UUID, req dto.CreateDocumentCommentRequest) (*domain.DocumentComment, error) {
	doc, err := s.Get(ctx, workspaceID, documentID)
	if err != nil {
		return nil, err
	}
	comment := &domain.DocumentComment{
		ID:         uuid.New(),
		DocumentID: documentID,
		UserID:     userID,
		Body:       sanitize.SanitizeHTML(req.Body),
	}
	if req.ParentID != nil && *req.ParentID != "" {
		pid, err := uuid.Parse(*req.ParentID)
		if err != nil {
			return nil, err
		}
		parent, err := s.docRepo.GetCommentByID(ctx, pid)
		if err != nil || parent == nil || parent.DocumentID != documentID {
			return nil, ErrDocumentNotFound
		}
		if parent.ParentID != nil {
			return nil, errors.New("cannot reply to a reply")
		}
		comment.ParentID = &pid
	}
	if err := s.docRepo.CreateComment(ctx, comment); err != nil {
		return nil, err
	}

	s.hub.Broadcast(workspaceID, realtime.Event{
		Type:    "doc.comment.created",
		Payload: map[string]any{"document_id": documentID.String(), "comment_id": comment.ID.String()},
	})

	// Notify the page creator and @mentioned users (never the commenter).
	recipients := make(map[uuid.UUID]bool)
	if doc.CreatedBy != userID {
		recipients[doc.CreatedBy] = true
	}
	title := fmt.Sprintf("New comment on doc: %s", displayTitle(doc))
	for uid := range recipients {
		if err := s.notifSvc.Create(ctx, uid, workspaceID, nil, "commented", title); err == nil {
			s.hub.BroadcastToUser(workspaceID, uid, realtime.Event{
				Type:    "notification.created",
				Payload: map[string]string{"type": "commented"},
			})
		}
	}
	mentionTitle := fmt.Sprintf("You were mentioned in doc: %s", displayTitle(doc))
	for _, uid := range extractMentionedUserIDs(comment.Body) {
		if uid == userID || recipients[uid] {
			continue
		}
		if err := s.notifSvc.Create(ctx, uid, workspaceID, nil, "mentioned", mentionTitle); err == nil {
			s.hub.BroadcastToUser(workspaceID, uid, realtime.Event{
				Type:    "notification.created",
				Payload: map[string]string{"type": "mentioned"},
			})
		}
	}
	return comment, nil
}

func (s *DocumentService) ListComments(ctx context.Context, workspaceID, documentID uuid.UUID) ([]domain.DocumentComment, error) {
	if _, err := s.Get(ctx, workspaceID, documentID); err != nil {
		return nil, err
	}
	return s.docRepo.ListComments(ctx, documentID)
}

func (s *DocumentService) ListCommentReplies(ctx context.Context, parentID uuid.UUID) ([]domain.DocumentComment, error) {
	return s.docRepo.ListCommentReplies(ctx, parentID)
}

func (s *DocumentService) ResolveComment(ctx context.Context, workspaceID, documentID, commentID, userID uuid.UUID) error {
	if _, err := s.Get(ctx, workspaceID, documentID); err != nil {
		return err
	}
	comment, err := s.docRepo.GetCommentByID(ctx, commentID)
	if err != nil || comment == nil || comment.DocumentID != documentID {
		return ErrDocumentNotFound
	}
	if err := s.docRepo.ResolveComment(ctx, commentID); err != nil {
		return err
	}
	s.hub.Broadcast(workspaceID, realtime.Event{
		Type:    "doc.comment.updated",
		Payload: map[string]any{"document_id": documentID.String(), "comment_id": commentID.String()},
	})
	return nil
}

func (s *DocumentService) ReopenComment(ctx context.Context, workspaceID, documentID, commentID, userID uuid.UUID) error {
	if _, err := s.Get(ctx, workspaceID, documentID); err != nil {
		return err
	}
	comment, err := s.docRepo.GetCommentByID(ctx, commentID)
	if err != nil || comment == nil || comment.DocumentID != documentID {
		return ErrDocumentNotFound
	}
	if err := s.docRepo.ReopenComment(ctx, commentID); err != nil {
		return err
	}
	s.hub.Broadcast(workspaceID, realtime.Event{
		Type:    "doc.comment.updated",
		Payload: map[string]any{"document_id": documentID.String(), "comment_id": commentID.String()},
	})
	return nil
}

func (s *DocumentService) nextPosition(ctx context.Context, workspaceID uuid.UUID, parentID *uuid.UUID) (float64, error) {
	docs, err := s.docRepo.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	max := 0.0
	for _, d := range docs {
		sameParent := (d.ParentID == nil && parentID == nil) ||
			(d.ParentID != nil && parentID != nil && *d.ParentID == *parentID)
		if sameParent && d.Position > max {
			max = d.Position
		}
	}
	return max + documentPositionStep, nil
}

// ensureNoCycle walks the ancestor chain of parent and fails if it reaches
// the moved document (a document cannot become its own ancestor).
func (s *DocumentService) ensureNoCycle(ctx context.Context, docID uuid.UUID, parent *domain.Document) error {
	current := parent
	for current != nil {
		if current.ID == docID {
			return ErrDocumentCycle
		}
		if current.ParentID == nil {
			return nil
		}
		next, err := s.docRepo.GetByID(ctx, *current.ParentID)
		if err != nil || next == nil {
			return nil
		}
		current = next
	}
	return nil
}

func (s *DocumentService) maybeCreateRevision(ctx context.Context, doc *domain.Document, userID uuid.UUID) {
	last, err := s.docRepo.LastRevisionAt(ctx, doc.ID)
	if err != nil {
		return
	}
	if last.Valid && time.Since(last.Time) < revisionMinInterval {
		return
	}
	rev := &domain.DocumentRevision{
		ID:          uuid.New(),
		DocumentID:  doc.ID,
		Title:       doc.Title,
		Icon:        doc.Icon,
		ContentHTML: doc.ContentHTML,
		ContentText: doc.ContentText,
		EditedBy:    &userID,
	}
	_ = s.docRepo.CreateRevision(ctx, rev)
}

// refreshLinks resolves wiki-link titles to document ids (case-insensitive)
// and swaps the document's outgoing links.
func (s *DocumentService) refreshLinks(ctx context.Context, workspaceID, documentID uuid.UUID, titles []string) error {
	targets := make([]uuid.UUID, 0, len(titles))
	seen := make(map[uuid.UUID]bool)
	for _, title := range titles {
		title = strings.TrimSpace(title)
		if title == "" {
			continue
		}
		target, err := s.docRepo.GetByTitle(ctx, workspaceID, title)
		if err != nil || target == nil || target.ID == documentID || seen[target.ID] {
			continue
		}
		seen[target.ID] = true
		targets = append(targets, target.ID)
	}
	return s.docRepo.ReplaceLinks(ctx, documentID, targets)
}

func displayTitle(doc *domain.Document) string {
	if doc.Title != "" {
		return doc.Title
	}
	return "Untitled"
}

func duplicateTitle(title string) string {
	if title == "" {
		return ""
	}
	if strings.Contains(title, " (copy") {
		return title + "*"
	}
	return title + " (copy)"
}
