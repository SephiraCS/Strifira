package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/realtime"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDocumentRepo is an in-memory DocumentRepo covering the behavior the
// DocumentService tests exercise.
type fakeDocumentRepo struct {
	docs       map[uuid.UUID]*domain.Document
	deleted    map[uuid.UUID]bool
	links      map[uuid.UUID][]uuid.UUID
	revisions  []domain.DocumentRevision
	lastRevAt  sql.NullTime
	duplicated *domain.Document
	getByTitle func(title string) *domain.Document
	moveDenied func(doc, parent *domain.Document) bool
}

func newFakeDocumentRepo() *fakeDocumentRepo {
	return &fakeDocumentRepo{
		docs:    make(map[uuid.UUID]*domain.Document),
		deleted: make(map[uuid.UUID]bool),
		links:   make(map[uuid.UUID][]uuid.UUID),
	}
}

func (f *fakeDocumentRepo) Create(ctx context.Context, doc *domain.Document) error {
	f.docs[doc.ID] = doc
	return nil
}

func (f *fakeDocumentRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Document, error) {
	doc, ok := f.docs[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return doc, nil
}

func (f *fakeDocumentRepo) GetByTitle(ctx context.Context, workspaceID uuid.UUID, title string) (*domain.Document, error) {
	if f.getByTitle != nil {
		if doc := f.getByTitle(title); doc != nil {
			return doc, nil
		}
		return nil, sql.ErrNoRows
	}
	for _, doc := range f.docs {
		if doc.WorkspaceID == workspaceID && doc.Title == title && !f.deleted[doc.ID] {
			return doc, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (f *fakeDocumentRepo) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.Document, error) {
	var docs []domain.Document
	for _, doc := range f.docs {
		if doc.WorkspaceID == workspaceID && !f.deleted[doc.ID] {
			docs = append(docs, *doc)
		}
	}
	return docs, nil
}

func (f *fakeDocumentRepo) ListDeleted(ctx context.Context, workspaceID uuid.UUID) ([]domain.Document, error) {
	var docs []domain.Document
	for _, doc := range f.docs {
		if doc.WorkspaceID == workspaceID && f.deleted[doc.ID] {
			docs = append(docs, *doc)
		}
	}
	return docs, nil
}

func (f *fakeDocumentRepo) Update(ctx context.Context, doc *domain.Document) error {
	f.docs[doc.ID] = doc
	return nil
}

func (f *fakeDocumentRepo) MarkDeleted(ctx context.Context, workspaceID, id, userID uuid.UUID) error {
	f.deleted[id] = true
	if doc, ok := f.docs[id]; ok {
		now := time.Now()
		doc.DeletedAt = &now
		uid := userID
		doc.DeletedBy = &uid
	}
	return nil
}

func (f *fakeDocumentRepo) Restore(ctx context.Context, workspaceID, id uuid.UUID) (*domain.Document, error) {
	delete(f.deleted, id)
	if doc, ok := f.docs[id]; ok {
		doc.DeletedAt = nil
		doc.DeletedBy = nil
	}
	return f.docs[id], nil
}

func (f *fakeDocumentRepo) Delete(ctx context.Context, workspaceID, id uuid.UUID) error {
	delete(f.docs, id)
	return nil
}

func (f *fakeDocumentRepo) Search(ctx context.Context, workspaceID uuid.UUID, q string, limit int) ([]domain.Document, error) {
	return nil, nil
}

func (f *fakeDocumentRepo) Duplicate(ctx context.Context, src *domain.Document, dst *domain.Document) error {
	f.duplicated = dst
	f.docs[dst.ID] = dst
	return nil
}

func (f *fakeDocumentRepo) CreateRevision(ctx context.Context, rev *domain.DocumentRevision) error {
	f.revisions = append(f.revisions, *rev)
	return nil
}

func (f *fakeDocumentRepo) ListRevisions(ctx context.Context, documentID uuid.UUID, limit int) ([]domain.DocumentRevision, error) {
	return f.revisions, nil
}

func (f *fakeDocumentRepo) GetRevisionByID(ctx context.Context, id uuid.UUID) (*domain.DocumentRevision, error) {
	for i := range f.revisions {
		if f.revisions[i].ID == id {
			return &f.revisions[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func (f *fakeDocumentRepo) LastRevisionAt(ctx context.Context, documentID uuid.UUID) (sql.NullTime, error) {
	return f.lastRevAt, nil
}

func (f *fakeDocumentRepo) ReplaceLinks(ctx context.Context, documentID uuid.UUID, targetIDs []uuid.UUID) error {
	f.links[documentID] = targetIDs
	return nil
}

func (f *fakeDocumentRepo) ListBacklinks(ctx context.Context, documentID uuid.UUID) ([]domain.Document, error) {
	var docs []domain.Document
	for source, targets := range f.links {
		for _, t := range targets {
			if t == documentID {
				docs = append(docs, *f.docs[source])
			}
		}
	}
	return docs, nil
}

func (f *fakeDocumentRepo) ListLinksByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.DocumentLink, error) {
	var links []domain.DocumentLink
	for source, targets := range f.links {
		for _, t := range targets {
			links = append(links, domain.DocumentLink{SourceID: source, TargetID: t})
		}
	}
	return links, nil
}

func (f *fakeDocumentRepo) ListTags(ctx context.Context, workspaceID uuid.UUID) ([]domain.DocumentTag, error) {
	return nil, nil
}

func (f *fakeDocumentRepo) CreateTag(ctx context.Context, tag *domain.DocumentTag) error {
	return nil
}

func (f *fakeDocumentRepo) GetTagByName(ctx context.Context, workspaceID uuid.UUID, name string) (*domain.DocumentTag, error) {
	return nil, sql.ErrNoRows
}

func (f *fakeDocumentRepo) DeleteTag(ctx context.Context, workspaceID, tagID uuid.UUID) error {
	return nil
}

func (f *fakeDocumentRepo) SetDocumentTags(ctx context.Context, documentID uuid.UUID, tagIDs []uuid.UUID) error {
	return nil
}

func (f *fakeDocumentRepo) ListTagsForDocument(ctx context.Context, documentID uuid.UUID) ([]domain.DocumentTag, error) {
	return nil, nil
}

func (f *fakeDocumentRepo) ListTagsForDocuments(ctx context.Context, documentIDs []uuid.UUID) (map[uuid.UUID][]string, error) {
	return map[uuid.UUID][]string{}, nil
}

func (f *fakeDocumentRepo) CreateComment(ctx context.Context, comment *domain.DocumentComment) error {
	return nil
}

func (f *fakeDocumentRepo) ListComments(ctx context.Context, documentID uuid.UUID) ([]domain.DocumentComment, error) {
	return nil, nil
}

func (f *fakeDocumentRepo) ListCommentReplies(ctx context.Context, parentID uuid.UUID) ([]domain.DocumentComment, error) {
	return nil, nil
}

func (f *fakeDocumentRepo) GetCommentByID(ctx context.Context, id uuid.UUID) (*domain.DocumentComment, error) {
	return nil, sql.ErrNoRows
}

func (f *fakeDocumentRepo) ResolveComment(ctx context.Context, id uuid.UUID) error { return nil }
func (f *fakeDocumentRepo) ReopenComment(ctx context.Context, id uuid.UUID) error  { return nil }

var _ repository.DocumentRepo = (*fakeDocumentRepo)(nil)

func newDocumentTestService(t *testing.T) (*fakeDocumentRepo, *DocumentService) {
	t.Helper()
	repo := newFakeDocumentRepo()
	svc := NewDocumentService(repo, realtime.NewHub(), nil, nil)
	return repo, svc
}

func TestDocumentService_CreateAppendsPositionAndConvertsMarkdown(t *testing.T) {
	repo, svc := newDocumentTestService(t)
	wsID, userID := uuid.New(), uuid.New()
	existing := &domain.Document{ID: uuid.New(), WorkspaceID: wsID, Title: "First", Position: 1024}
	require.NoError(t, repo.Create(context.Background(), existing))

	doc, err := svc.Create(context.Background(), wsID, userID, dto.CreateDocumentRequest{
		Title:     "Second",
		ContentMD: "# Heading\n\nSome **bold** text",
	})
	require.NoError(t, err)
	assert.Equal(t, "Second", doc.Title)
	assert.InDelta(t, 2048.0, doc.Position, 0.001)
	assert.Contains(t, doc.ContentHTML, "<h1>")
	assert.Contains(t, doc.ContentHTML, "<strong>bold</strong>")
	assert.Equal(t, userID, doc.CreatedBy)
}

func TestDocumentService_UpdateSanitizesRecordsRevisionAndLinks(t *testing.T) {
	repo, svc := newDocumentTestService(t)
	wsID, userID := uuid.New(), uuid.New()
	doc := &domain.Document{ID: uuid.New(), WorkspaceID: wsID, Title: "Page", Position: 1024}
	require.NoError(t, repo.Create(context.Background(), doc))

	target := &domain.Document{ID: uuid.New(), WorkspaceID: wsID, Title: "Target", Position: 1024}
	require.NoError(t, repo.Create(context.Background(), target))

	newTitle := "Renamed"
	html := "<p>Hello <script>alert(1)</script>world</p><a class=\"wiki-link\" data-wiki-title=\"Target\" href=\"/x\">Target</a>"
	text := "Hello world"
	updated, err := svc.Update(context.Background(), wsID, doc.ID, userID, dto.UpdateDocumentRequest{
		Title:       &newTitle,
		ContentHTML: &html,
		ContentText: &text,
		WikiTitles:  []string{"Target"},
	})
	require.NoError(t, err)
	assert.Equal(t, "Renamed", updated.Title)
	assert.NotContains(t, updated.ContentHTML, "<script>")
	assert.Equal(t, target.ID, repo.links[doc.ID][0])
	require.Len(t, repo.revisions, 1)
	assert.Equal(t, "Renamed", repo.revisions[0].Title)
}

func TestDocumentService_UpdateSkipsRevisionWithinInterval(t *testing.T) {
	repo, svc := newDocumentTestService(t)
	wsID, userID := uuid.New(), uuid.New()
	doc := &domain.Document{ID: uuid.New(), WorkspaceID: wsID, Title: "Page"}
	require.NoError(t, repo.Create(context.Background(), doc))
	repo.lastRevAt.Valid = true
	repo.lastRevAt.Time = time.Now()

	html := "<p>new</p>"
	_, err := svc.Update(context.Background(), wsID, doc.ID, userID, dto.UpdateDocumentRequest{ContentHTML: &html})
	require.NoError(t, err)
	assert.Empty(t, repo.revisions)
}

func TestDocumentService_MoveRejectsCycle(t *testing.T) {
	repo, svc := newDocumentTestService(t)
	wsID, userID := uuid.New(), uuid.New()
	parent := &domain.Document{ID: uuid.New(), WorkspaceID: wsID, Title: "Parent"}
	child := &domain.Document{ID: uuid.New(), WorkspaceID: wsID, Title: "Child", ParentID: &parent.ID}
	require.NoError(t, repo.Create(context.Background(), parent))
	require.NoError(t, repo.Create(context.Background(), child))

	// Moving the parent under its own child must fail.
	childID := child.ID.String()
	_, err := svc.Move(context.Background(), wsID, parent.ID, userID, dto.MoveDocumentRequest{ParentID: &childID})
	assert.ErrorIs(t, err, ErrDocumentCycle)
}

func TestDocumentService_PurgeRequiresTrashed(t *testing.T) {
	repo, svc := newDocumentTestService(t)
	wsID := uuid.New()
	doc := &domain.Document{ID: uuid.New(), WorkspaceID: wsID, Title: "Page"}
	require.NoError(t, repo.Create(context.Background(), doc))

	err := svc.Purge(context.Background(), wsID, doc.ID)
	assert.Error(t, err)

	require.NoError(t, svc.Delete(context.Background(), wsID, doc.ID, uuid.New()))
	assert.NoError(t, svc.Purge(context.Background(), wsID, doc.ID))
}

func TestDocumentService_DuplicateCopiesContent(t *testing.T) {
	repo, svc := newDocumentTestService(t)
	wsID, userID := uuid.New(), uuid.New()
	doc := &domain.Document{ID: uuid.New(), WorkspaceID: wsID, Title: "Original", ContentHTML: "<p>x</p>"}
	require.NoError(t, repo.Create(context.Background(), doc))

	dup, err := svc.Duplicate(context.Background(), wsID, doc.ID, userID)
	require.NoError(t, err)
	assert.NotEqual(t, doc.ID, dup.ID)
	assert.Equal(t, "Original (copy)", dup.Title)
	assert.Equal(t, "<p>x</p>", dup.ContentHTML)
}
