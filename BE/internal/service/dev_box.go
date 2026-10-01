package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/repository"
	forgejoclient "github.com/ku/ku-backend/pkg/forgejo"
)

var (
	ErrDevBoxNotFound       = repository.ErrDevBoxNotFound
	ErrDevBoxConflict       = repository.ErrDevBoxConflict
	ErrDevBoxLeaseLost      = repository.ErrDevBoxLeaseLost
	ErrInvalidDevBoxInput   = errors.New("invalid dev box request")
	ErrDevBoxForbidden      = errors.New("dev box access denied")
	ErrDevBoxAuthentication = errors.New("dev box authentication failed")
)

const (
	DevBoxLeaseDuration            = 60 * time.Second
	DevBoxHeartbeatIntervalSeconds = 15
	DevBoxOnlineDuration           = 90 * time.Second
	DevBoxMaxLogBytes              = 4 << 20
	DevBoxMaxLogEntryBytes         = 64 << 10
	DevBoxMaxPatchBytes            = 1 << 20
	DevBoxMaxResultBytes           = dto.DevBoxCompleteBodyLimit
)

type DevBoxMembershipStore interface {
	GetMember(context.Context, uuid.UUID, uuid.UUID) (*domain.WorkspaceMember, error)
}

// DevBoxService registers existing development hosts. It never provisions or
// changes their lifecycle; its worker credential authorizes only assigned jobs.
type DevBoxService struct {
	store       repository.DevBoxStore
	members     DevBoxMembershipStore
	development *DevMachineService
	allowed     func(uuid.UUID) bool
}

func NewDevBoxService(store repository.DevBoxStore, members DevBoxMembershipStore, development *DevMachineService, allowed func(uuid.UUID) bool) *DevBoxService {
	if allowed == nil {
		allowed = func(uuid.UUID) bool { return false }
	}
	return &DevBoxService{store: store, members: members, development: development, allowed: allowed}
}

func (s *DevBoxService) authorize(ctx context.Context, workspaceID, userID uuid.UUID, permission string) error {
	if workspaceID == uuid.Nil || userID == uuid.Nil || !s.allowed(userID) {
		return ErrDevBoxForbidden
	}
	member, err := s.members.GetMember(ctx, workspaceID, userID)
	if err != nil {
		return err
	}
	if member == nil || !domain.HasPermission(member.Role, permission) {
		return ErrDevBoxForbidden
	}
	return nil
}

func newDevBoxToken(prefix string) (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw := prefix + hex.EncodeToString(buf)
	return raw, hashDevBoxToken(raw), nil
}

func hashDevBoxToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func validDevBoxToken(raw, prefix string) bool {
	if len(raw) != len(prefix)+64 || !strings.HasPrefix(raw, prefix) {
		return false
	}
	_, err := hex.DecodeString(raw[len(prefix):])
	return err == nil
}

func validDevBoxText(value string, limit int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit && !strings.ContainsFunc(value, unicode.IsControl)
}

func decorateDevBox(box *domain.DevBox) *domain.DevBox {
	if box != nil {
		box.Online = box.Enabled && box.RevokedAt == nil && box.LastSeenAt != nil && time.Since(*box.LastSeenAt) <= DevBoxOnlineDuration
		if len(box.Providers) == 0 {
			box.Providers = json.RawMessage(`[]`)
		}
	}
	return box
}

func (s *DevBoxService) Create(ctx context.Context, workspaceID, userID uuid.UUID, req dto.CreateDevBoxRequest) (*dto.DevBoxTokenResponse, error) {
	if err := s.authorize(ctx, workspaceID, userID, domain.PermDevMachineCreate); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || !validDevBoxText(name, 100) {
		return nil, ErrInvalidDevBoxInput
	}
	raw, hash, err := newDevBoxToken("kub_")
	if err != nil {
		return nil, err
	}
	box := &domain.DevBox{ID: uuid.New(), WorkspaceID: workspaceID, CreatedByUserID: userID, Name: name, Enabled: true,
		TokenHash: hash, TokenPrefix: raw[:12], Providers: json.RawMessage(`[]`)}
	if err := s.store.CreateBox(ctx, box); err != nil {
		return nil, err
	}
	return &dto.DevBoxTokenResponse{Box: decorateDevBox(box), Token: raw}, nil
}

func (s *DevBoxService) List(ctx context.Context, workspaceID, userID uuid.UUID) ([]domain.DevBox, error) {
	if err := s.authorize(ctx, workspaceID, userID, domain.PermDevMachineRead); err != nil {
		return nil, err
	}
	boxes, err := s.store.ListBoxes(ctx, workspaceID, userID)
	for i := range boxes {
		decorateDevBox(&boxes[i])
	}
	return nonNilSlice(boxes), err
}

func (s *DevBoxService) Update(ctx context.Context, workspaceID, boxID, userID uuid.UUID, req dto.UpdateDevBoxRequest) (*domain.DevBox, error) {
	if err := s.authorize(ctx, workspaceID, userID, domain.PermDevMachineManage); err != nil {
		return nil, err
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || !validDevBoxText(name, 100) {
			return nil, ErrInvalidDevBoxInput
		}
		req.Name = new(name)
	}
	box, err := s.store.UpdateBox(ctx, workspaceID, boxID, userID, req.Name, req.Enabled)
	return decorateDevBox(box), err
}

func (s *DevBoxService) RotateToken(ctx context.Context, workspaceID, boxID, userID uuid.UUID) (*dto.DevBoxTokenResponse, error) {
	if err := s.authorize(ctx, workspaceID, userID, domain.PermDevMachineManage); err != nil {
		return nil, err
	}
	raw, hash, err := newDevBoxToken("kub_")
	if err != nil {
		return nil, err
	}
	box, err := s.store.RotateBoxToken(ctx, workspaceID, boxID, userID, hash, raw[:12])
	if err != nil {
		return nil, err
	}
	return &dto.DevBoxTokenResponse{Box: decorateDevBox(box), Token: raw}, nil
}

func (s *DevBoxService) Revoke(ctx context.Context, workspaceID, boxID, userID uuid.UUID) error {
	if err := s.authorize(ctx, workspaceID, userID, domain.PermDevMachineManage); err != nil {
		return err
	}
	return s.store.RevokeBox(ctx, workspaceID, boxID, userID)
}

func devBoxHasProvider(box *domain.DevBox, id string) bool {
	var providers []domain.DevBoxProvider
	if json.Unmarshal(box.Providers, &providers) != nil {
		return false
	}
	return slices.ContainsFunc(providers, func(provider domain.DevBoxProvider) bool { return provider.ID == id })
}

func validDevBoxProvider(id string) bool {
	return id == "claude-code" || id == "codex" || id == "opencode"
}

func validateDevBoxJob(req dto.CreateDevBoxJobRequest) error {
	if !validDevBoxProvider(req.Provider) || len(req.Prompt) > 50000 || !utf8.ValidString(req.Prompt) || strings.ContainsRune(req.Prompt, '\x00') ||
		len(req.TestCommand) > 32 || len(req.AcceptanceCriteria) > 50 || (req.MaxRuntimeSeconds != 0 && (req.MaxRuntimeSeconds < 30 || req.MaxRuntimeSeconds > 86400)) {
		return ErrInvalidDevBoxInput
	}
	if (req.RepositoryFullName != "" && (len(req.RepositoryFullName) > 500 || !validDevBoxRepositoryName(req.RepositoryFullName))) ||
		(req.BaseBranch != "" && (req.RepositoryFullName == "" || !validDevBoxBaseBranch(req.BaseBranch))) {
		return ErrInvalidDevBoxInput
	}
	for _, arg := range req.TestCommand {
		if arg == "" || len(arg) > 4096 || !utf8.ValidString(arg) || strings.ContainsRune(arg, '\x00') {
			return ErrInvalidDevBoxInput
		}
	}
	for _, criterion := range req.AcceptanceCriteria {
		if len(criterion) > 2000 || !utf8.ValidString(criterion) || strings.ContainsRune(criterion, '\x00') {
			return ErrInvalidDevBoxInput
		}
	}
	return nil
}

func (s *DevBoxService) CreateJob(ctx context.Context, workspaceID, boxID, userID uuid.UUID, req dto.CreateDevBoxJobRequest) (*domain.DevBoxJob, error) {
	if err := s.authorize(ctx, workspaceID, userID, domain.PermDevMachineManage); err != nil {
		return nil, err
	}
	if err := validateDevBoxJob(req); err != nil {
		return nil, err
	}
	box, err := s.store.GetBox(ctx, workspaceID, boxID, userID)
	if err != nil {
		return nil, err
	}
	if box == nil {
		return nil, ErrDevBoxNotFound
	}
	if !box.Enabled || box.RevokedAt != nil || !devBoxHasProvider(box, req.Provider) {
		return nil, fmt.Errorf("%w: select an enabled box advertising this provider", ErrDevBoxConflict)
	}
	issueID, err := uuid.Parse(req.IssueID)
	if err != nil {
		return nil, ErrInvalidDevBoxInput
	}
	issue, err := s.development.store.GetIssueDevelopmentContext(ctx, workspaceID, issueID)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, fmt.Errorf("%w: issue is not in this workspace", ErrInvalidDevBoxInput)
	}
	var linked *domain.ForgejoRepoModel
	var configuredBranch *string
	if req.RepositoryFullName != "" {
		linked, err = s.development.store.GetLinkedRepositoryByFullName(ctx, workspaceID, req.RepositoryFullName)
		if req.BaseBranch != "" {
			configuredBranch = new(req.BaseBranch)
		}
	} else {
		setting, resolveErr := s.development.resolveDevelopmentSetting(ctx, workspaceID, issue, nil)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if setting.ForgejoRepoID == nil {
			return nil, fmt.Errorf("%w: issue has no development repository", ErrInvalidDevBoxInput)
		}
		linked, err = s.development.store.GetLinkedRepository(ctx, workspaceID, *setting.ForgejoRepoID)
		configuredBranch = setting.BaseBranch
	}
	if err != nil {
		return nil, err
	}
	if linked == nil || !linked.IsActive || linked.WorkspaceID != workspaceID ||
		(req.RepositoryFullName != "" && !strings.EqualFold(linked.FullName, req.RepositoryFullName)) {
		return nil, fmt.Errorf("%w: development repository is not linked", ErrInvalidDevBoxInput)
	}
	origin, err := forgejoclient.NormalizeInstanceURL(linked.InstanceURL)
	if err != nil || !validDevBoxRepositoryName(linked.FullName) {
		return nil, fmt.Errorf("%w: invalid development repository", ErrInvalidDevBoxInput)
	}
	baseBranch := linked.DefaultBranch
	if configuredBranch != nil {
		baseBranch = *configuredBranch
	}
	if !validDevBoxBaseBranch(baseBranch) {
		return nil, fmt.Errorf("%w: invalid repository base branch", ErrInvalidDevBoxInput)
	}
	maxRuntime := req.MaxRuntimeSeconds
	if maxRuntime == 0 {
		maxRuntime = 3600
	}
	policy, err := s.development.store.GetPolicy(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if policy != nil && (!providerAllowed(policy.AllowedProviders, req.Provider) || !repositoryAllowed(policy.AllowedRepositories, linked.FullName) || maxRuntime > policy.MaxRuntimeMinutes*60) {
		return nil, fmt.Errorf("%w: job exceeds the workspace development policy", ErrDevBoxConflict)
	}
	jobID := uuid.New()
	branch := "ku/" + strings.ToLower(issue.Identifier) + "-" + jobID.String()[:8]
	if !validGitRef(branch) {
		return nil, ErrInvalidDevBoxInput
	}
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "%s\n\n%s\n\n%s\n\nKu issue execution contract:\nIssue: %s\nRepository: %s\nBase branch: %s\nWorking branch: %s\nWork only in the isolated job worktree. Do not push or open a pull request. Never print or persist credentials.\n", issue.Title, issueDescription(issue), strings.TrimSpace(req.Prompt), issue.Identifier, linked.FullName, baseBranch, branch)
	for _, criterion := range req.AcceptanceCriteria {
		fmt.Fprintf(&prompt, "Acceptance criterion: %s\n", criterion)
	}
	if len(req.TestCommand) > 0 {
		commandJSON, _ := json.Marshal(req.TestCommand)
		fmt.Fprintf(&prompt, "Required test argv: %s\n", commandJSON)
	}
	prompt.WriteString("Finish with the implemented changes, test outcomes, and remaining risks.\n")
	if prompt.Len() > 100000 {
		return nil, fmt.Errorf("%w: combined issue context is too large", ErrInvalidDevBoxInput)
	}
	command, _ := json.Marshal(nonNilSlice(req.TestCommand))
	criteria, _ := json.Marshal(nonNilSlice(req.AcceptanceCriteria))
	job := &domain.DevBoxJob{ID: jobID, WorkspaceID: workspaceID, BoxID: boxID, RequestedByUserID: userID, IssueID: issueID,
		IssueIdentifier: issue.Identifier, ForgejoRepoID: linked.ID, Provider: req.Provider, Prompt: prompt.String(), RepositoryURL: origin + "/" + linked.FullName,
		RepositoryFullName: linked.FullName, BaseBranch: baseBranch, WorkingBranch: branch, TestCommand: command, AcceptanceCriteria: criteria,
		MaxRuntimeSeconds: maxRuntime, Status: domain.DevBoxJobQueued}
	if err := s.store.CreateJob(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
}

func issueDescription(issue *domain.Issue) string {
	if issue.Description == nil {
		return ""
	}
	return *issue.Description
}

func validDevBoxRepositoryName(value string) bool {
	owner, repo, ok := strings.Cut(value, "/")
	return ok && !strings.ContainsRune(repo, '/') && validRepositorySegment(owner) && validRepositorySegment(repo)
}

func validDevBoxBaseBranch(value string) bool {
	return len(value) <= 255 && utf8.ValidString(value) && value != "@" && !strings.Contains(value, "/.") &&
		!strings.ContainsFunc(value, unicode.IsControl) && validGitRef(value)
}

func (s *DevBoxService) ListJobs(ctx context.Context, workspaceID, userID uuid.UUID, params dto.DevBoxJobListParams) ([]domain.DevBoxJob, int, error) {
	if err := s.authorize(ctx, workspaceID, userID, domain.PermDevMachineRead); err != nil {
		return nil, 0, err
	}
	params.Defaults()
	if params.Page < 1 || params.Page > 1000000 || params.PerPage < 1 || params.PerPage > 100 {
		return nil, 0, ErrInvalidDevBoxInput
	}
	var issueID, boxID *uuid.UUID
	for _, filter := range []struct {
		raw    string
		target **uuid.UUID
	}{{params.IssueID, &issueID}, {params.BoxID, &boxID}} {
		if filter.raw != "" {
			id, err := uuid.Parse(filter.raw)
			if err != nil {
				return nil, 0, ErrInvalidDevBoxInput
			}
			*filter.target = new(id)
		}
	}
	jobs, total, err := s.store.ListJobs(ctx, workspaceID, userID, issueID, boxID, params.Page, params.PerPage)
	return nonNilSlice(jobs), total, err
}

func (s *DevBoxService) GetJob(ctx context.Context, workspaceID, jobID, userID uuid.UUID) (*domain.DevBoxJob, error) {
	if err := s.authorize(ctx, workspaceID, userID, domain.PermDevMachineRead); err != nil {
		return nil, err
	}
	return s.store.GetJob(ctx, workspaceID, jobID, userID)
}

func (s *DevBoxService) CancelJob(ctx context.Context, workspaceID, jobID, userID uuid.UUID) (*domain.DevBoxJob, error) {
	if err := s.authorize(ctx, workspaceID, userID, domain.PermDevMachineManage); err != nil {
		return nil, err
	}
	return s.store.CancelJob(ctx, workspaceID, jobID, userID)
}

func (s *DevBoxService) JobLogs(ctx context.Context, workspaceID, jobID, userID uuid.UUID, params dto.DevBoxJobLogParams) ([]domain.DevBoxJobLog, error) {
	if err := s.authorize(ctx, workspaceID, userID, domain.PermDevMachineRead); err != nil {
		return nil, err
	}
	if params.Limit == 0 {
		params.Limit = 100
	}
	if params.AfterID < 0 || params.Limit < 1 || params.Limit > 500 {
		return nil, ErrInvalidDevBoxInput
	}
	logs, err := s.store.ListJobLogs(ctx, workspaceID, jobID, userID, params.AfterID, params.Limit)
	return nonNilSlice(logs), err
}

func (s *DevBoxService) AuthenticateWorker(ctx context.Context, raw string) (*domain.DevBox, error) {
	if !validDevBoxToken(raw, "kub_") {
		return nil, ErrDevBoxAuthentication
	}
	box, err := s.store.GetBoxByTokenHash(ctx, hashDevBoxToken(raw))
	if err != nil {
		if errors.Is(err, ErrDevBoxNotFound) {
			return nil, ErrDevBoxAuthentication
		}
		return nil, err
	}
	if box == nil || !box.Enabled || box.RevokedAt != nil {
		return nil, ErrDevBoxAuthentication
	}
	if err := s.authorize(ctx, box.WorkspaceID, box.CreatedByUserID, domain.PermDevMachineManage); err != nil {
		if errors.Is(err, ErrDevBoxForbidden) {
			return nil, ErrDevBoxAuthentication
		}
		return nil, err
	}
	return decorateDevBox(box), nil
}

func (s *DevBoxService) Heartbeat(ctx context.Context, box *domain.DevBox, req dto.DevBoxHeartbeatRequest) (*dto.DevBoxHeartbeatResponse, error) {
	if req.Hostname == "" || req.OS == "" || req.Arch == "" || req.Version == "" || !validDevBoxText(req.Hostname, 255) || !validDevBoxText(req.OS, 64) || !validDevBoxText(req.Arch, 64) || !validDevBoxText(req.Version, 100) || len(req.Providers) > 3 {
		return nil, ErrInvalidDevBoxInput
	}
	seen := map[string]bool{}
	for _, provider := range req.Providers {
		if !validDevBoxProvider(provider.ID) || seen[provider.ID] || provider.Version == "" || !validDevBoxText(provider.Version, 100) {
			return nil, ErrInvalidDevBoxInput
		}
		seen[provider.ID] = true
	}
	providers, _ := json.Marshal(nonNilSlice(req.Providers))
	updated := *box
	updated.Hostname, updated.OS, updated.Arch, updated.Version, updated.Providers = req.Hostname, req.OS, req.Arch, req.Version, providers
	current, err := s.store.HeartbeatBox(ctx, &updated)
	if err != nil {
		return nil, err
	}
	return &dto.DevBoxHeartbeatResponse{Box: decorateDevBox(current), ServerTime: time.Now().UTC(), HeartbeatIntervalSeconds: DevBoxHeartbeatIntervalSeconds}, nil
}

func (s *DevBoxService) Claim(ctx context.Context, box *domain.DevBox) (*dto.DevBoxClaimResponse, error) {
	raw, hash, err := newDevBoxToken("kul_")
	if err != nil {
		return nil, err
	}
	job, err := s.store.ClaimJob(ctx, box.ID, box.TokenHash, hash, DevBoxLeaseDuration)
	if err != nil || job == nil {
		return nil, err
	}
	if job.LeaseExpiresAt == nil {
		return nil, errors.New("claimed job has no lease deadline")
	}
	return &dto.DevBoxClaimResponse{Job: job, LeaseToken: raw, LeaseExpiresAt: *job.LeaseExpiresAt, ServerTime: time.Now().UTC(), HeartbeatIntervalSeconds: DevBoxHeartbeatIntervalSeconds}, nil
}

func devBoxLeaseHash(raw string) (string, error) {
	if !validDevBoxToken(raw, "kul_") {
		return "", ErrDevBoxLeaseLost
	}
	return hashDevBoxToken(raw), nil
}

func devBoxLeaseResponse(job *domain.DevBoxJob) *dto.DevBoxLeaseResponse {
	return &dto.DevBoxLeaseResponse{Status: job.Status, CancelRequested: job.CancelRequested, LeaseExpiresAt: job.LeaseExpiresAt, DeadlineAt: job.DeadlineAt, ServerTime: time.Now().UTC()}
}

func (s *DevBoxService) Start(ctx context.Context, box *domain.DevBox, jobID uuid.UUID, req dto.DevBoxLeaseRequest) (*dto.DevBoxLeaseResponse, error) {
	hash, err := devBoxLeaseHash(req.LeaseToken)
	if err != nil {
		return nil, err
	}
	job, err := s.store.StartJob(ctx, box.ID, jobID, box.TokenHash, hash, DevBoxLeaseDuration)
	if err != nil {
		return nil, err
	}
	return devBoxLeaseResponse(job), nil
}

func (s *DevBoxService) Renew(ctx context.Context, box *domain.DevBox, jobID uuid.UUID, req dto.DevBoxLeaseRequest) (*dto.DevBoxLeaseResponse, error) {
	hash, err := devBoxLeaseHash(req.LeaseToken)
	if err != nil {
		return nil, err
	}
	job, err := s.store.RenewJob(ctx, box.ID, jobID, box.TokenHash, hash, DevBoxLeaseDuration)
	if err != nil {
		return nil, err
	}
	return devBoxLeaseResponse(job), nil
}

func (s *DevBoxService) AppendLog(ctx context.Context, box *domain.DevBox, jobID uuid.UUID, req dto.DevBoxAppendLogRequest) (*dto.DevBoxAppendLogResponse, error) {
	hash, err := devBoxLeaseHash(req.LeaseToken)
	if err != nil {
		return nil, err
	}
	if req.Sequence < 1 || len(req.Content) == 0 || len(req.Content) > DevBoxMaxLogEntryBytes || !utf8.ValidString(req.Content) || strings.ContainsRune(req.Content, '\x00') || (req.Stream != "stdout" && req.Stream != "stderr" && req.Stream != "system") {
		return nil, ErrInvalidDevBoxInput
	}
	log := &domain.DevBoxJobLog{JobID: jobID, Sequence: req.Sequence, Stream: req.Stream, Content: req.Content}
	accepted, truncated, err := s.store.AppendLog(ctx, box.ID, jobID, box.TokenHash, hash, log, DevBoxMaxLogBytes)
	if err != nil {
		return nil, err
	}
	return &dto.DevBoxAppendLogResponse{Accepted: accepted, LogsTruncated: truncated}, nil
}

func validateDevBoxResult(result *domain.DevBoxJobResult) error {
	if !domain.DevBoxJobTerminal(result.Status) || len(result.Summary) > 8192 || len(result.Patch) > DevBoxMaxPatchBytes ||
		len(result.ChangedFiles) > 500 || len(result.TestsRun) > 100 || len(result.RiskNotes) > 100 ||
		!utf8.ValidString(result.Summary) || !utf8.ValidString(result.Patch) ||
		strings.ContainsRune(result.Summary, '\x00') || strings.ContainsRune(result.Patch, '\x00') ||
		(result.TestStatus != "passed" && result.TestStatus != "failed" && result.TestStatus != "not_run") ||
		len(result.Branch) > 255 || !utf8.ValidString(result.Branch) || (result.Branch != "" && !validGitRef(result.Branch)) {
		return ErrInvalidDevBoxInput
	}
	for _, values := range [][]string{result.ChangedFiles, result.TestsRun, result.RiskNotes} {
		for _, value := range values {
			if len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
				return ErrInvalidDevBoxInput
			}
		}
	}
	return nil
}

func validateDevBoxResultSize(result *domain.DevBoxJobResult) error {
	encoded, err := json.Marshal(result)
	// PostgreSQL's JSONB text adds separators to compact JSON. At most 700
	// array elements and 11 fields fit within this reserved formatting space;
	// Go's HTML escaping can only make the compact encoding larger.
	if err != nil || len(encoded)+1024 > DevBoxMaxResultBytes {
		return ErrInvalidDevBoxInput
	}
	return nil
}

func (s *DevBoxService) Complete(ctx context.Context, box *domain.DevBox, jobID uuid.UUID, req dto.DevBoxCompleteRequest) (*domain.DevBoxJob, error) {
	hash, err := devBoxLeaseHash(req.LeaseToken)
	if err != nil {
		return nil, err
	}
	if err := validateDevBoxResult(&req.Result); err != nil {
		return nil, err
	}
	job, err := s.store.GetWorkerJob(ctx, box.ID, jobID, box.TokenHash, hash)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, ErrDevBoxLeaseLost
	}
	result := req.Result
	var testCommand []string
	if err := json.Unmarshal(job.TestCommand, &testCommand); err != nil {
		return nil, err
	}
	if result.Status == domain.DevBoxJobSucceeded && (result.ExitCode != 0 || result.TestStatus == "failed" || (result.TestExitCode != nil && *result.TestExitCode != 0) || (len(testCommand) > 0 && (result.TestStatus != "passed" || result.TestExitCode == nil || len(result.TestsRun) == 0))) {
		result.Status = domain.DevBoxJobFailed
		result.RiskNotes = append(result.RiskNotes[:min(len(result.RiskNotes), 99)], "The job did not provide a successful harness exit and the required independent test result.")
	}
	result.ChangedFiles, result.TestsRun, result.RiskNotes = nonNilSlice(result.ChangedFiles), nonNilSlice(result.TestsRun), nonNilSlice(result.RiskNotes)
	if result.Branch != "" && result.Branch != job.WorkingBranch {
		return nil, fmt.Errorf("%w: result branch does not match the assigned job", ErrInvalidDevBoxInput)
	}
	if err := validateDevBoxResultSize(&result); err != nil {
		return nil, err
	}
	return s.store.CompleteJob(ctx, box.ID, jobID, box.TokenHash, hash, &result)
}

func (s *DevBoxService) SweepExpired(ctx context.Context) error { return s.store.SweepExpiredJobs(ctx) }
