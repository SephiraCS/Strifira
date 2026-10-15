package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type UserRepo interface {
	Create(ctx context.Context, user *domain.User) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	GetByEmail(ctx context.Context, email string) (*domain.User, error)
	Update(ctx context.Context, user *domain.User) error
}

type AISettingsRepo interface {
	GetByWorkspaceID(ctx context.Context, workspaceID uuid.UUID) (*domain.AISettings, error)
	Upsert(ctx context.Context, settings *domain.AISettings) error
}

type ScreenShareRepo interface {
	Create(ctx context.Context, share *domain.ScreenShare) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.ScreenShare, error)
	ListActiveByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.ScreenShare, error)
	ListRecentEndedByWorkspace(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.ScreenShare, error)
	GetActiveByPresenter(ctx context.Context, workspaceID, userID uuid.UUID) (*domain.ScreenShare, error)
	MarkEnded(ctx context.Context, id uuid.UUID) error
	TouchPresenterSeen(ctx context.Context, id uuid.UUID) error
}

type RefreshTokenRepo interface {
	Create(ctx context.Context, rt *RefreshToken) error
	GetByHash(ctx context.Context, hash string) (*RefreshToken, error)
	// RotateByHash marks an unexpired, unrotated token as rotated and
	// reports whether the token was still usable: rotated now for the first
	// time, or already rotated within grace. A token missing, expired, or
	// rotated longer than grace ago is reported as not usable.
	RotateByHash(ctx context.Context, hash string, grace time.Duration) (bool, error)
	DeleteByHash(ctx context.Context, hash string) error
	DeleteByUser(ctx context.Context, userID uuid.UUID) error
	DeleteExpired(ctx context.Context) error
}

type WorkspaceRepo interface {
	Create(ctx context.Context, ws *domain.Workspace) error
	CreateWithMemberAndLabels(ctx context.Context, ws *domain.Workspace, member *domain.WorkspaceMember, labels []domain.Label) error
	GetBySlug(ctx context.Context, slug string) (*domain.Workspace, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Workspace, error)
	Update(ctx context.Context, ws *domain.Workspace) error
	Delete(ctx context.Context, id uuid.UUID) error
	ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.Workspace, error)
	AddMember(ctx context.Context, member *domain.WorkspaceMember) error
	GetMember(ctx context.Context, workspaceID, userID uuid.UUID) (*domain.WorkspaceMember, error)
	ListMembers(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceMember, error)
	ListMembersWithUsers(ctx context.Context, workspaceID uuid.UUID) ([]domain.WorkspaceMemberWithUser, error)
	UpdateMemberRole(ctx context.Context, workspaceID, userID uuid.UUID, role string) error
	RemoveMember(ctx context.Context, workspaceID, userID uuid.UUID) error
	CountMembersByRole(ctx context.Context, workspaceID uuid.UUID, role string) (int, error)
}

type TeamRepo interface {
	Create(ctx context.Context, team *domain.Team) error
	// CreateWithDefaults atomically creates the team, its creator membership
	// and its initial statuses.
	CreateWithDefaults(ctx context.Context, team *domain.Team, creator *domain.TeamMember, statuses []domain.TeamStatus) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Team, error)
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.Team, error)
	Update(ctx context.Context, team *domain.Team) error
	// UpdateAndRenameKey changes the team (incl. its key) and rewrites every
	// issue identifier to the new key atomically; fails on collisions.
	UpdateAndRenameKey(ctx context.Context, team *domain.Team, newKey string) (renamedIssues int64, err error)
	Delete(ctx context.Context, id uuid.UUID) error
	AddMember(ctx context.Context, member *domain.TeamMember) error
	GetMember(ctx context.Context, teamID, userID uuid.UUID) (*domain.TeamMember, error)
	ListMembers(ctx context.Context, teamID uuid.UUID) ([]domain.TeamMember, error)
	RemoveMember(ctx context.Context, teamID, userID uuid.UUID) error
}

type IssueRepo interface {
	Create(ctx context.Context, tx *sqlx.Tx, issue *domain.Issue) error
	NextNumber(ctx context.Context, tx *sqlx.Tx, teamID uuid.UUID) (int, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Issue, error)
	GetByIdentifier(ctx context.Context, workspaceID uuid.UUID, identifier string) (*domain.Issue, error)
	List(ctx context.Context, workspaceID uuid.UUID, params dto.IssueFilterParams) ([]domain.Issue, int, error)
	Update(ctx context.Context, issue *domain.Issue) error
	SetArchived(ctx context.Context, workspaceID, issueID uuid.UUID, archived bool) (*domain.Issue, error)
	Delete(ctx context.Context, id uuid.UUID) error
	SetLabels(ctx context.Context, issueID uuid.UUID, labelIDs []uuid.UUID) error
	GetLabels(ctx context.Context, issueID uuid.UUID) ([]domain.Label, error)
	GetLabelsForIssues(ctx context.Context, issueIDs []uuid.UUID) (map[uuid.UUID][]domain.Label, error)
	SetAssignees(ctx context.Context, issueID uuid.UUID, userIDs []uuid.UUID) error
	GetAssignees(ctx context.Context, issueID uuid.UUID) ([]uuid.UUID, error)
	GetAssigneesForIssues(ctx context.Context, issueIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error)
	ListSubIssues(ctx context.Context, parentID uuid.UUID) ([]domain.Issue, error)
	CountSubIssues(ctx context.Context, parentID uuid.UUID) (int, int, error)
	CountSubIssuesForIssues(ctx context.Context, issueIDs []uuid.UUID) (map[uuid.UUID]domain.SubIssueCount, error)
	WouldCreateCycle(ctx context.Context, issueID, parentID uuid.UUID) (bool, error)
	CycleIsActive(ctx context.Context, cycleID uuid.UUID) (bool, error)
	BulkUpdate(ctx context.Context, workspaceID uuid.UUID, issueIDs []uuid.UUID, status *string, priority *int, assigneeID *uuid.UUID, statusID *uuid.UUID, cycleID *uuid.UUID, cycleSet bool, archived *bool) (int, error)
	BulkDelete(ctx context.Context, workspaceID uuid.UUID, issueIDs []uuid.UUID) (int, error)
	BeginTx(ctx context.Context) (*sqlx.Tx, error)
}

type AssetRepo interface {
	Create(ctx context.Context, asset *domain.Asset) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Asset, error)
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, search string, limit, offset int) ([]domain.AssetWithUploader, error)
	CountByWorkspace(ctx context.Context, workspaceID uuid.UUID, search string) (int, error)
	Delete(ctx context.Context, workspaceID, id uuid.UUID) (*domain.Asset, error)
}

type DocumentRepo interface {
	Create(ctx context.Context, doc *domain.Document) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Document, error)
	GetByTitle(ctx context.Context, workspaceID uuid.UUID, title string) (*domain.Document, error)
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.Document, error)
	ListDeleted(ctx context.Context, workspaceID uuid.UUID) ([]domain.Document, error)
	Update(ctx context.Context, doc *domain.Document) error
	MarkDeleted(ctx context.Context, workspaceID, id, userID uuid.UUID) error
	Restore(ctx context.Context, workspaceID, id uuid.UUID) (*domain.Document, error)
	Delete(ctx context.Context, workspaceID, id uuid.UUID) error
	Search(ctx context.Context, workspaceID uuid.UUID, q string, limit int) ([]domain.Document, error)
	Duplicate(ctx context.Context, src *domain.Document, dst *domain.Document) error
	CreateRevision(ctx context.Context, rev *domain.DocumentRevision) error
	ListRevisions(ctx context.Context, documentID uuid.UUID, limit int) ([]domain.DocumentRevision, error)
	GetRevisionByID(ctx context.Context, id uuid.UUID) (*domain.DocumentRevision, error)
	LastRevisionAt(ctx context.Context, documentID uuid.UUID) (sql.NullTime, error)
	ReplaceLinks(ctx context.Context, documentID uuid.UUID, targetIDs []uuid.UUID) error
	ListBacklinks(ctx context.Context, documentID uuid.UUID) ([]domain.Document, error)
	ListLinksByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.DocumentLink, error)
	ListTags(ctx context.Context, workspaceID uuid.UUID) ([]domain.DocumentTag, error)
	CreateTag(ctx context.Context, tag *domain.DocumentTag) error
	GetTagByName(ctx context.Context, workspaceID uuid.UUID, name string) (*domain.DocumentTag, error)
	DeleteTag(ctx context.Context, workspaceID, tagID uuid.UUID) error
	SetDocumentTags(ctx context.Context, documentID uuid.UUID, tagIDs []uuid.UUID) error
	ListTagsForDocument(ctx context.Context, documentID uuid.UUID) ([]domain.DocumentTag, error)
	ListTagsForDocuments(ctx context.Context, documentIDs []uuid.UUID) (map[uuid.UUID][]string, error)
	CreateComment(ctx context.Context, comment *domain.DocumentComment) error
	ListComments(ctx context.Context, documentID uuid.UUID) ([]domain.DocumentComment, error)
	ListCommentReplies(ctx context.Context, parentID uuid.UUID) ([]domain.DocumentComment, error)
	GetCommentByID(ctx context.Context, id uuid.UUID) (*domain.DocumentComment, error)
	ResolveComment(ctx context.Context, id uuid.UUID) error
	ReopenComment(ctx context.Context, id uuid.UUID) error
}

type DocumentUpdateRepo interface {
	AppendUpdates(ctx context.Context, documentID uuid.UUID, updates [][]byte) error
	ListUpdates(ctx context.Context, documentID uuid.UUID) ([][]byte, error)
	CountUpdates(ctx context.Context, documentID uuid.UUID) (int, error)
	Compact(ctx context.Context, documentID uuid.UUID, snapshot []byte, tail [][]byte) error
	DeleteByDocument(ctx context.Context, documentID uuid.UUID) error
}

type UserAvatarRepo interface {
	Upsert(ctx context.Context, avatar *domain.UserAvatar) error
	GetByUserID(ctx context.Context, userID uuid.UUID) (*domain.UserAvatar, error)
	Delete(ctx context.Context, userID uuid.UUID) error
}

type WorkspaceAvatarRepo interface {
	Upsert(ctx context.Context, avatar *domain.WorkspaceAvatar) error
	GetByWorkspaceID(ctx context.Context, workspaceID uuid.UUID) (*domain.WorkspaceAvatar, error)
	Delete(ctx context.Context, workspaceID uuid.UUID) error
}

type LabelRepo interface {
	Create(ctx context.Context, label *domain.Label) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Label, error)
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.Label, error)
	Update(ctx context.Context, label *domain.Label) error
	Delete(ctx context.Context, id uuid.UUID) error
	ExistsByName(ctx context.Context, workspaceID uuid.UUID, name string) (bool, error)
}

type CommentRepo interface {
	Create(ctx context.Context, comment *domain.Comment) error
	ListByIssue(ctx context.Context, issueID uuid.UUID) ([]domain.Comment, error)
	ListReplies(ctx context.Context, parentID uuid.UUID) ([]domain.Comment, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Comment, error)
	Resolve(ctx context.Context, id uuid.UUID) error
	Reopen(ctx context.Context, id uuid.UUID) error
}

type ProjectRepo interface {
	Create(ctx context.Context, project *domain.Project) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Project, error)
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.Project, error)
	ListByTeam(ctx context.Context, teamID uuid.UUID) ([]domain.Project, error)
	Update(ctx context.Context, project *domain.Project) error
	Delete(ctx context.Context, id uuid.UUID) error
	IssueStats(ctx context.Context, projectID uuid.UUID) (total int, completed int, cancelled int, err error)
}

type NotificationRepo interface {
	Create(ctx context.Context, n *domain.Notification) error
	CreateOrRefresh(ctx context.Context, n *domain.Notification, window time.Duration) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Notification, error)
	ListByUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]domain.Notification, error)
	ListSnoozed(ctx context.Context, userID uuid.UUID) ([]domain.Notification, error)
	ListArchived(ctx context.Context, userID uuid.UUID, limit int) ([]domain.Notification, error)
	Update(ctx context.Context, n *domain.Notification) error
	MarkAllRead(ctx context.Context, userID uuid.UUID) error
	UnreadCount(ctx context.Context, userID uuid.UUID) (int, error)
}

type IssueHistoryRepo interface {
	Create(ctx context.Context, issueID, userID uuid.UUID, field string, oldValue, newValue *string) error
	ListByIssue(ctx context.Context, issueID uuid.UUID) ([]domain.IssueHistory, error)
}

type ViewRepo interface {
	Create(ctx context.Context, view *domain.View) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.View, error)
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID, userID uuid.UUID) ([]domain.View, error)
	Update(ctx context.Context, view *domain.View) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type CycleRepo interface {
	Create(ctx context.Context, cycle *domain.Cycle) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Cycle, error)
	ListByTeam(ctx context.Context, teamID uuid.UUID) ([]domain.Cycle, error)
	NextNumber(ctx context.Context, teamID uuid.UUID) (int, error)
	Update(ctx context.Context, cycle *domain.Cycle) error
	Delete(ctx context.Context, id uuid.UUID) error
	IssueStats(ctx context.Context, cycleID uuid.UUID) (total int, completed int, cancelled int, err error)
	BurndownData(ctx context.Context, cycleID uuid.UUID, startDate, endDate time.Time) ([]dto.BurndownPoint, error)
	ExistsByName(ctx context.Context, teamID uuid.UUID, name string) (bool, error)
	HasOverlap(ctx context.Context, teamID uuid.UUID, startDate, endDate time.Time, excludeID *uuid.UUID) (bool, error)
	GetNextUpcoming(ctx context.Context, teamID uuid.UUID) (*domain.Cycle, error)
	CarryOverIssues(ctx context.Context, fromCycleID, toCycleID uuid.UUID) (int, error)
	VelocityData(ctx context.Context, teamID uuid.UUID, limit int) ([]dto.VelocityPoint, error)
}

type IssueRelationRepo interface {
	Create(ctx context.Context, rel *domain.IssueRelation) error
	ListByIssue(ctx context.Context, issueID uuid.UUID) ([]domain.IssueRelation, error)
	ListByIssues(ctx context.Context, issueIDs []uuid.UUID) ([]domain.IssueRelation, error)
	Delete(ctx context.Context, id uuid.UUID) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.IssueRelation, error)
	DeleteByIssues(ctx context.Context, issueID, relatedIssueID uuid.UUID, relType domain.IssueRelationType) error
	CreateWithInverse(ctx context.Context, rel, inverse *domain.IssueRelation) error
	DeleteInWorkspace(ctx context.Context, workspaceID, id uuid.UUID) (bool, error)
}

type TeamStatusRepo interface {
	Create(ctx context.Context, status *domain.TeamStatus) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.TeamStatus, error)
	GetByIDs(ctx context.Context, ids []uuid.UUID) ([]domain.TeamStatus, error)
	GetByTeamAndSlug(ctx context.Context, teamID uuid.UUID, slug string) (*domain.TeamStatus, error)
	ListByTeam(ctx context.Context, teamID uuid.UUID) ([]domain.TeamStatus, error)
	Update(ctx context.Context, status *domain.TeamStatus) error
	Delete(ctx context.Context, id uuid.UUID) error
	NextPosition(ctx context.Context, teamID uuid.UUID) (int, error)
}

type FavoriteRepo interface {
	Create(ctx context.Context, fav *domain.Favorite) error
	ListByUser(ctx context.Context, workspaceID, userID uuid.UUID) ([]domain.Favorite, error)
	Delete(ctx context.Context, workspaceID, userID uuid.UUID, entityType string, entityID uuid.UUID) error
	DeleteByID(ctx context.Context, id uuid.UUID) error
}

type UserPreferencesRepo interface {
	Get(ctx context.Context, userID uuid.UUID) (*domain.UserPreferences, error)
	Upsert(ctx context.Context, prefs *domain.UserPreferences) error
}

type ProjectStatusVisibilityRepo interface {
	SetVisibleStatuses(ctx context.Context, projectID uuid.UUID, statusIDs []uuid.UUID) error
	ListVisibleStatuses(ctx context.Context, projectID uuid.UUID) ([]uuid.UUID, error)
	ListProjectsForStatus(ctx context.Context, statusID uuid.UUID) ([]uuid.UUID, error)
	ListProjectIDsByStatuses(ctx context.Context, statusIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error)
	// CreateStatusWithProjects and UpdateStatusWithProjects write a status
	// and its project visibility atomically.
	CreateStatusWithProjects(ctx context.Context, status *domain.TeamStatus, projectIDs []uuid.UUID) error
	UpdateStatusWithProjects(ctx context.Context, status *domain.TeamStatus, projectIDs *[]uuid.UUID) error
}

type SharedLinkRepo interface {
	Create(ctx context.Context, link *domain.SharedLink) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.SharedLink, error)
	GetByToken(ctx context.Context, token string) (*domain.SharedLink, error)
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.SharedLink, error)
	Update(ctx context.Context, link *domain.SharedLink) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type IssueTemplateRepo interface {
	Create(ctx context.Context, tmpl *domain.IssueTemplate) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.IssueTemplate, error)
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.IssueTemplate, error)
	Update(ctx context.Context, tmpl *domain.IssueTemplate) error
	Delete(ctx context.Context, id uuid.UUID) error
	ListDueForRecurrence(ctx context.Context) ([]domain.IssueTemplate, error)
}

type APIKeyRepo interface {
	Create(ctx context.Context, key *domain.APIKey) error
	ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.APIKey, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.APIKey, error)
	GetByHash(ctx context.Context, tokenHash string) (*domain.APIKey, error)
	Revoke(ctx context.Context, id, userID uuid.UUID) error
	TouchLastUsed(ctx context.Context, id uuid.UUID) error
}
