package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
	"github.com/ku/ku-backend/internal/domain"
)

var (
	ErrDevBoxNotFound  = errors.New("dev box or job not found")
	ErrDevBoxConflict  = errors.New("dev box state conflict")
	ErrDevBoxLeaseLost = errors.New("dev box job lease lost")
)

type DevBoxStore interface {
	CreateBox(context.Context, *domain.DevBox) error
	ListBoxes(context.Context, uuid.UUID, uuid.UUID) ([]domain.DevBox, error)
	GetBox(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.DevBox, error)
	UpdateBox(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, *string, *bool) (*domain.DevBox, error)
	RotateBoxToken(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string) (*domain.DevBox, error)
	RevokeBox(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
	GetBoxByTokenHash(context.Context, string) (*domain.DevBox, error)
	HeartbeatBox(context.Context, *domain.DevBox) (*domain.DevBox, error)
	CreateJob(context.Context, *domain.DevBoxJob) error
	ListJobs(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, *uuid.UUID, int, int) ([]domain.DevBoxJob, int, error)
	GetJob(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.DevBoxJob, error)
	CancelJob(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.DevBoxJob, error)
	ClaimJob(context.Context, uuid.UUID, string, string, time.Duration) (*domain.DevBoxJob, error)
	GetWorkerJob(context.Context, uuid.UUID, uuid.UUID, string, string) (*domain.DevBoxJob, error)
	StartJob(context.Context, uuid.UUID, uuid.UUID, string, string, time.Duration) (*domain.DevBoxJob, error)
	RenewJob(context.Context, uuid.UUID, uuid.UUID, string, string, time.Duration) (*domain.DevBoxJob, error)
	AppendLog(context.Context, uuid.UUID, uuid.UUID, string, string, *domain.DevBoxJobLog, int64) (bool, bool, error)
	CompleteJob(context.Context, uuid.UUID, uuid.UUID, string, string, *domain.DevBoxJobResult) (*domain.DevBoxJob, error)
	ListJobLogs(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int64, int) ([]domain.DevBoxJobLog, error)
	SweepExpiredJobs(context.Context) error
}

type DevBoxRepository struct{ db *sqlx.DB }

func NewDevBoxRepository(db *sqlx.DB) *DevBoxRepository { return &DevBoxRepository{db: db} }

var _ DevBoxStore = (*DevBoxRepository)(nil)

// Membership is checked on every worker operation; demoting or removing the creator
// immediately removes the worker authority even when its token remains on disk.
const devBoxAuthority = `b.enabled AND b.revoked_at IS NULL AND EXISTS (
	SELECT 1 FROM workspace_members wm WHERE wm.workspace_id=b.workspace_id
	AND wm.user_id=b.created_by_user_id AND wm.role IN ('owner','admin','member'))`

func devBoxError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDevBoxNotFound
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && (pgErr.Code == "23505" || pgErr.Code == "23503" || pgErr.Code == "23514") {
		return fmt.Errorf("%w: %s", ErrDevBoxConflict, pgErr.ConstraintName)
	}
	return err
}

func (r *DevBoxRepository) CreateBox(ctx context.Context, box *domain.DevBox) error {
	if len(box.Providers) == 0 {
		box.Providers = json.RawMessage(`[]`)
	}
	err := r.db.QueryRowContext(ctx, `INSERT INTO dev_boxes (id,workspace_id,created_by_user_id,name,enabled,token_hash,token_prefix,providers)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at,updated_at`,
		box.ID, box.WorkspaceID, box.CreatedByUserID, box.Name, box.Enabled, box.TokenHash, box.TokenPrefix, box.Providers).Scan(&box.CreatedAt, &box.UpdatedAt)
	return devBoxError(err)
}

func (r *DevBoxRepository) ListBoxes(ctx context.Context, workspaceID, userID uuid.UUID) ([]domain.DevBox, error) {
	boxes := []domain.DevBox{}
	err := r.db.SelectContext(ctx, &boxes, `SELECT * FROM dev_boxes WHERE workspace_id=$1 AND created_by_user_id=$2 AND revoked_at IS NULL ORDER BY created_at DESC,id`, workspaceID, userID)
	return boxes, err
}

func (r *DevBoxRepository) GetBox(ctx context.Context, workspaceID, boxID, userID uuid.UUID) (*domain.DevBox, error) {
	var box domain.DevBox
	err := r.db.GetContext(ctx, &box, `SELECT * FROM dev_boxes WHERE workspace_id=$1 AND id=$2 AND created_by_user_id=$3 AND revoked_at IS NULL`, workspaceID, boxID, userID)
	if err != nil {
		return nil, devBoxError(err)
	}
	return &box, nil
}

func (r *DevBoxRepository) mutateBox(ctx context.Context, workspaceID, boxID, userID uuid.UUID, invalidateJobs bool, query string, args ...any) (*domain.DevBox, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var box domain.DevBox
	if err = tx.GetContext(ctx, &box, `SELECT * FROM dev_boxes WHERE workspace_id=$1 AND id=$2 AND created_by_user_id=$3 AND revoked_at IS NULL FOR UPDATE`, workspaceID, boxID, userID); err != nil {
		return nil, devBoxError(err)
	}
	values := append([]any{boxID}, args...)
	if err = tx.GetContext(ctx, &box, query, values...); err != nil {
		return nil, devBoxError(err)
	}
	if !box.Enabled || box.RevokedAt != nil || invalidateJobs {
		if _, err = tx.ExecContext(ctx, `UPDATE dev_box_jobs SET status='cancelled',cancel_requested=true,lease_expires_at=NULL,
			completed_at=clock_timestamp(),updated_at=clock_timestamp(),result=jsonb_build_object('status','cancelled','summary','Worker registration changed')
			WHERE box_id=$1 AND status IN ('queued','leased','running')`, boxID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &box, nil
}

func (r *DevBoxRepository) UpdateBox(ctx context.Context, workspaceID, boxID, userID uuid.UUID, name *string, enabled *bool) (*domain.DevBox, error) {
	return r.mutateBox(ctx, workspaceID, boxID, userID, false, `UPDATE dev_boxes SET name=COALESCE($2,name),enabled=COALESCE($3,enabled),updated_at=clock_timestamp() WHERE id=$1 RETURNING *`, name, enabled)
}

const rotateDevBoxTokenQuery = `UPDATE dev_boxes SET token_hash=$2,token_prefix=$3,last_seen_at=NULL,updated_at=clock_timestamp() WHERE id=$1 RETURNING *`

func (r *DevBoxRepository) RotateBoxToken(ctx context.Context, workspaceID, boxID, userID uuid.UUID, hash, prefix string) (*domain.DevBox, error) {
	return r.mutateBox(ctx, workspaceID, boxID, userID, true, rotateDevBoxTokenQuery, hash, prefix)
}
func (r *DevBoxRepository) RevokeBox(ctx context.Context, workspaceID, boxID, userID uuid.UUID) error {
	_, err := r.mutateBox(ctx, workspaceID, boxID, userID, true, `UPDATE dev_boxes SET enabled=false,revoked_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 RETURNING *`)
	return err
}
func (r *DevBoxRepository) GetBoxByTokenHash(ctx context.Context, hash string) (*domain.DevBox, error) {
	var box domain.DevBox
	err := r.db.GetContext(ctx, &box, `SELECT b.* FROM dev_boxes b WHERE b.token_hash=$1 AND `+devBoxAuthority, hash)
	if err != nil {
		return nil, devBoxError(err)
	}
	return &box, nil
}
func (r *DevBoxRepository) HeartbeatBox(ctx context.Context, box *domain.DevBox) (*domain.DevBox, error) {
	var updated domain.DevBox
	err := r.db.GetContext(ctx, &updated, `UPDATE dev_boxes b SET hostname=$3,os=$4,arch=$5,version=$6,providers=$7,
		last_seen_at=clock_timestamp(),updated_at=clock_timestamp() WHERE b.id=$1 AND b.token_hash=$2 AND `+devBoxAuthority+` RETURNING b.*`,
		box.ID, box.TokenHash, box.Hostname, box.OS, box.Arch, box.Version, box.Providers)
	if err != nil {
		return nil, devBoxError(err)
	}
	return &updated, nil
}

func lockDevBox(ctx context.Context, tx *sqlx.Tx, boxID uuid.UUID) (*domain.DevBox, error) {
	var box domain.DevBox
	err := tx.GetContext(ctx, &box, `SELECT b.* FROM dev_boxes b WHERE b.id=$1 AND `+devBoxAuthority+` FOR UPDATE OF b`, boxID)
	if err != nil {
		return nil, devBoxError(err)
	}
	return &box, nil
}

// Authenticate again inside the transaction so a request which passed HTTP
// authentication before token rotation cannot mutate or claim work afterward.
func lockAuthenticatedDevBox(ctx context.Context, tx *sqlx.Tx, boxID uuid.UUID, tokenHash string) (*domain.DevBox, error) {
	if tokenHash == "" {
		return nil, ErrDevBoxLeaseLost
	}
	var box domain.DevBox
	err := tx.GetContext(ctx, &box, `SELECT b.* FROM dev_boxes b WHERE b.id=$1 AND b.token_hash=$2 AND `+devBoxAuthority+` FOR UPDATE OF b`, boxID, tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDevBoxLeaseLost
	}
	if err != nil {
		return nil, err
	}
	return &box, nil
}

func (r *DevBoxRepository) CreateJob(ctx context.Context, job *domain.DevBoxJob) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	box, err := lockDevBox(ctx, tx, job.BoxID)
	if err != nil {
		return err
	}
	if box.WorkspaceID != job.WorkspaceID || box.CreatedByUserID != job.RequestedByUserID {
		return ErrDevBoxNotFound
	}
	var queued int
	if err = tx.GetContext(ctx, &queued, `SELECT COUNT(*) FROM dev_box_jobs WHERE box_id=$1 AND status='queued'`, job.BoxID); err != nil {
		return err
	}
	if queued >= 100 {
		return ErrDevBoxConflict
	}
	if len(job.TestCommand) == 0 {
		job.TestCommand = json.RawMessage(`[]`)
	}
	if len(job.AcceptanceCriteria) == 0 {
		job.AcceptanceCriteria = json.RawMessage(`[]`)
	}
	job.Status = domain.DevBoxJobQueued
	err = tx.QueryRowContext(ctx, `INSERT INTO dev_box_jobs (id,workspace_id,box_id,requested_by_user_id,issue_id,issue_identifier,github_repo_id,
		provider,prompt,repository_url,repository_full_name,base_branch,working_branch,test_command,acceptance_criteria,max_runtime_seconds,status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) RETURNING created_at,updated_at`,
		job.ID, job.WorkspaceID, job.BoxID, job.RequestedByUserID, job.IssueID, job.IssueIdentifier, job.GitHubRepoID,
		job.Provider, job.Prompt, job.RepositoryURL, job.RepositoryFullName, job.BaseBranch, job.WorkingBranch, job.TestCommand, job.AcceptanceCriteria, job.MaxRuntimeSeconds, job.Status).Scan(&job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return devBoxError(err)
	}
	return tx.Commit()
}

func (r *DevBoxRepository) ListJobs(ctx context.Context, workspaceID, userID uuid.UUID, issueID, boxID *uuid.UUID, page, perPage int) ([]domain.DevBoxJob, int, error) {
	page = max(page, 1)
	perPage = min(max(perPage, 1), 100)
	const filter = ` FROM dev_box_jobs WHERE workspace_id=$1 AND requested_by_user_id=$2 AND ($3::uuid IS NULL OR issue_id=$3) AND ($4::uuid IS NULL OR box_id=$4)`
	var total int
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*)`+filter, workspaceID, userID, issueID, boxID); err != nil {
		return nil, 0, err
	}
	jobs := []domain.DevBoxJob{}
	err := r.db.SelectContext(ctx, &jobs, `SELECT *`+filter+` ORDER BY created_at DESC,id LIMIT $5 OFFSET $6`, workspaceID, userID, issueID, boxID, perPage, (page-1)*perPage)
	return jobs, total, err
}
func (r *DevBoxRepository) GetJob(ctx context.Context, workspaceID, jobID, userID uuid.UUID) (*domain.DevBoxJob, error) {
	var job domain.DevBoxJob
	err := r.db.GetContext(ctx, &job, `SELECT * FROM dev_box_jobs WHERE workspace_id=$1 AND id=$2 AND requested_by_user_id=$3`, workspaceID, jobID, userID)
	if err != nil {
		return nil, devBoxError(err)
	}
	return &job, nil
}
func (r *DevBoxRepository) CancelJob(ctx context.Context, workspaceID, jobID, userID uuid.UUID) (*domain.DevBoxJob, error) {
	var job domain.DevBoxJob
	err := r.db.GetContext(ctx, &job, `UPDATE dev_box_jobs SET cancel_requested=CASE WHEN status IN ('queued','leased','running') THEN true ELSE cancel_requested END,
		status=CASE WHEN status='queued' THEN 'cancelled' ELSE status END,
		completed_at=CASE WHEN status='queued' THEN clock_timestamp() ELSE completed_at END,
		updated_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2 AND requested_by_user_id=$3 RETURNING *`, workspaceID, jobID, userID)
	if err != nil {
		return nil, devBoxError(err)
	}
	return &job, nil
}

// Expiry never requeues work: even a leased job could have reached a disconnected
// executor, so an explicit new job is required before it may run again.
const expireDevBoxJobs = `UPDATE dev_box_jobs j SET
	status=CASE WHEN j.cancel_requested OR NOT (` + devBoxAuthority + `) THEN 'cancelled'
		WHEN j.deadline_at IS NOT NULL AND j.deadline_at<=clock_timestamp() THEN 'timeout' ELSE 'failed' END,
	completed_at=clock_timestamp(),lease_expires_at=NULL,updated_at=clock_timestamp(),
	result=jsonb_build_object('status',CASE WHEN j.cancel_requested OR NOT (` + devBoxAuthority + `) THEN 'cancelled'
		WHEN j.deadline_at IS NOT NULL AND j.deadline_at<=clock_timestamp() THEN 'timeout' ELSE 'failed' END,
		'summary','Worker authority, lease or runtime expired')
	FROM dev_boxes b WHERE b.id=j.box_id AND j.status IN ('queued','leased','running')
	AND (NOT (` + devBoxAuthority + `) OR (j.status IN ('leased','running') AND
		(j.lease_expires_at IS NULL OR j.lease_expires_at<=clock_timestamp()
			OR (j.deadline_at IS NOT NULL AND j.deadline_at<=clock_timestamp()))))`

func (r *DevBoxRepository) SweepExpiredJobs(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, expireDevBoxJobs)
	return err
}

func (r *DevBoxRepository) ClaimJob(ctx context.Context, boxID uuid.UUID, tokenHash, leaseHash string, leaseDuration time.Duration) (*domain.DevBoxJob, error) {
	if leaseHash == "" || leaseDuration <= 0 {
		return nil, ErrDevBoxConflict
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = lockAuthenticatedDevBox(ctx, tx, boxID, tokenHash); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, expireDevBoxJobs+` AND j.box_id=$1`, boxID); err != nil {
		return nil, err
	}
	var active bool
	if err = tx.GetContext(ctx, &active, `SELECT EXISTS(SELECT 1 FROM dev_box_jobs WHERE box_id=$1 AND status IN ('leased','running'))`, boxID); err != nil {
		return nil, err
	}
	if active {
		return nil, nil
	}
	var job domain.DevBoxJob
	err = tx.GetContext(ctx, &job, `SELECT j.* FROM dev_box_jobs j JOIN dev_boxes b ON b.id=j.box_id
		WHERE j.box_id=$1 AND j.status='queued' AND NOT j.cancel_requested
		AND b.providers @> jsonb_build_array(jsonb_build_object('id',j.provider))
		ORDER BY j.created_at,j.id FOR UPDATE OF j SKIP LOCKED LIMIT 1`, boxID)
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	err = tx.GetContext(ctx, &job, `UPDATE dev_box_jobs SET status='leased',lease_hash=$2,
		lease_expires_at=clock_timestamp()+($3::double precision*interval '1 second'),updated_at=clock_timestamp()
		WHERE id=$1 RETURNING *`, job.ID, leaseHash, leaseDuration.Seconds())
	if err != nil {
		return nil, devBoxError(err)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

func (r *DevBoxRepository) GetWorkerJob(ctx context.Context, boxID, jobID uuid.UUID, tokenHash, leaseHash string) (*domain.DevBoxJob, error) {
	// Completion validates the original immutable specification before atomically
	// applying expiry/cancellation in CompleteJob. This read does not renew a lease.
	var job domain.DevBoxJob
	err := r.db.GetContext(ctx, &job, `SELECT j.* FROM dev_box_jobs j JOIN dev_boxes b ON b.id=j.box_id
		WHERE j.box_id=$1 AND j.id=$2 AND j.lease_hash=$3 AND j.lease_hash<>''
		AND b.token_hash=$4 AND `+devBoxAuthority, boxID, jobID, leaseHash, tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDevBoxLeaseLost
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// Worker mutations lock the registration before the job, matching claim and
// token rotation. This makes revocation and mutation serialize predictably.
func lockDevBoxJob(ctx context.Context, tx *sqlx.Tx, boxID, jobID uuid.UUID, tokenHash, leaseHash string) (*domain.DevBoxJob, error) {
	if leaseHash == "" {
		return nil, ErrDevBoxLeaseLost
	}
	if _, err := lockAuthenticatedDevBox(ctx, tx, boxID, tokenHash); err != nil {
		if errors.Is(err, ErrDevBoxNotFound) {
			return nil, ErrDevBoxLeaseLost
		}
		return nil, err
	}
	var job domain.DevBoxJob
	err := tx.GetContext(ctx, &job, `SELECT * FROM dev_box_jobs WHERE box_id=$1 AND id=$2 AND lease_hash=$3 FOR UPDATE`, boxID, jobID, leaseHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDevBoxLeaseLost
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func aliveDevBoxLease(ctx context.Context, tx *sqlx.Tx, jobID uuid.UUID) error {
	var alive bool
	err := tx.GetContext(ctx, &alive, `SELECT status IN ('leased','running') AND lease_expires_at>clock_timestamp()
		AND (deadline_at IS NULL OR deadline_at>clock_timestamp()) FROM dev_box_jobs WHERE id=$1`, jobID)
	if err != nil {
		return err
	}
	if !alive {
		return ErrDevBoxLeaseLost
	}
	return nil
}

func (r *DevBoxRepository) StartJob(ctx context.Context, boxID, jobID uuid.UUID, tokenHash, leaseHash string, leaseDuration time.Duration) (*domain.DevBoxJob, error) {
	return r.renewDevBoxJob(ctx, boxID, jobID, tokenHash, leaseHash, leaseDuration, true)
}
func (r *DevBoxRepository) RenewJob(ctx context.Context, boxID, jobID uuid.UUID, tokenHash, leaseHash string, leaseDuration time.Duration) (*domain.DevBoxJob, error) {
	return r.renewDevBoxJob(ctx, boxID, jobID, tokenHash, leaseHash, leaseDuration, false)
}
func (r *DevBoxRepository) renewDevBoxJob(ctx context.Context, boxID, jobID uuid.UUID, tokenHash, leaseHash string, leaseDuration time.Duration, start bool) (*domain.DevBoxJob, error) {
	if leaseDuration <= 0 {
		return nil, ErrDevBoxConflict
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	job, err := lockDevBoxJob(ctx, tx, boxID, jobID, tokenHash, leaseHash)
	if err != nil {
		return nil, err
	}
	if err = aliveDevBoxLease(ctx, tx, jobID); err != nil {
		return nil, err
	}
	// A cancelled leased job must never receive permission to start. A running
	// job may renew briefly so it can receive the cancellation acknowledgement.
	if start && job.CancelRequested {
		return nil, ErrDevBoxLeaseLost
	}
	err = tx.GetContext(ctx, job, `UPDATE dev_box_jobs SET
		status=CASE WHEN $3 THEN 'running' ELSE status END,
		started_at=CASE WHEN $3 THEN COALESCE(started_at,clock_timestamp()) ELSE started_at END,
		deadline_at=CASE WHEN $3 THEN COALESCE(deadline_at,clock_timestamp()+(max_runtime_seconds*interval '1 second')) ELSE deadline_at END,
		lease_expires_at=LEAST(clock_timestamp()+($2::double precision*interval '1 second'),
			CASE WHEN $3 THEN COALESCE(deadline_at,clock_timestamp()+(max_runtime_seconds*interval '1 second')) ELSE deadline_at END),
		updated_at=clock_timestamp() WHERE id=$1 AND lease_expires_at>clock_timestamp()
		AND (deadline_at IS NULL OR deadline_at>clock_timestamp()) RETURNING *`, jobID, leaseDuration.Seconds(), start)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDevBoxLeaseLost
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return job, nil
}

func truncateDevBoxLog(content string, remaining int64) (string, bool) {
	if remaining >= int64(len(content)) {
		return content, false
	}
	if remaining <= 0 {
		return "", true
	}
	end := int(remaining)
	for end > 0 && !utf8.ValidString(content[:end]) {
		end--
	}
	return content[:end], true
}

func (r *DevBoxRepository) AppendLog(ctx context.Context, boxID, jobID uuid.UUID, tokenHash, leaseHash string, log *domain.DevBoxJobLog, maxBytes int64) (bool, bool, error) {
	if log == nil || log.Sequence <= 0 || len(log.Content) > 65536 || !utf8.ValidString(log.Content) {
		return false, false, ErrDevBoxConflict
	}
	maxBytes = min(max(maxBytes, 0), 4194304)
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback()
	job, err := lockDevBoxJob(ctx, tx, boxID, jobID, tokenHash, leaseHash)
	if err != nil {
		return false, false, err
	}
	if err = aliveDevBoxLease(ctx, tx, jobID); err != nil {
		return false, false, err
	}
	var duplicate bool
	if err = tx.GetContext(ctx, &duplicate, `SELECT EXISTS(SELECT 1 FROM dev_box_job_logs WHERE job_id=$1 AND sequence=$2)`, jobID, log.Sequence); err != nil {
		return false, false, err
	}
	if duplicate {
		return true, job.LogsTruncated, tx.Commit()
	}
	content, truncated := truncateDevBoxLog(log.Content, maxBytes-job.LogsBytes)
	if content != "" {
		err = tx.QueryRowContext(ctx, `INSERT INTO dev_box_job_logs (job_id,sequence,stream,content) VALUES ($1,$2,$3,$4) RETURNING id,created_at`, jobID, log.Sequence, log.Stream, content).Scan(&log.ID, &log.CreatedAt)
		if err != nil {
			return false, false, devBoxError(err)
		}
		log.JobID = jobID
		log.Content = content
	}
	_, err = tx.ExecContext(ctx, `UPDATE dev_box_jobs SET logs_bytes=logs_bytes+$2,logs_truncated=logs_truncated OR $3,updated_at=clock_timestamp() WHERE id=$1`, jobID, len(content), truncated)
	if err != nil {
		return false, false, err
	}
	if err = tx.Commit(); err != nil {
		return false, false, err
	}
	return content != "", job.LogsTruncated || truncated, nil
}

func (r *DevBoxRepository) CompleteJob(ctx context.Context, boxID, jobID uuid.UUID, tokenHash, leaseHash string, result *domain.DevBoxJobResult) (*domain.DevBoxJob, error) {
	if result == nil || !domain.DevBoxJobTerminal(result.Status) {
		return nil, ErrDevBoxConflict
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	job, err := lockDevBoxJob(ctx, tx, boxID, jobID, tokenHash, leaseHash)
	if err != nil {
		return nil, err
	}
	if domain.DevBoxJobTerminal(job.Status) {
		return job, nil
	}
	var leaseExpired, deadlineExpired bool
	err = tx.QueryRowContext(ctx, `SELECT lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp(),
		deadline_at IS NOT NULL AND deadline_at<=clock_timestamp() FROM dev_box_jobs WHERE id=$1`, jobID).Scan(&leaseExpired, &deadlineExpired)
	if err != nil {
		return nil, err
	}
	final := *result
	switch {
	case job.CancelRequested:
		final.Status = domain.DevBoxJobCancelled
	case deadlineExpired:
		final.Status = domain.DevBoxJobTimeout
	case leaseExpired:
		final.Status = domain.DevBoxJobFailed
		final.Summary = "Worker lease expired before completion"
	case job.Status != domain.DevBoxJobRunning:
		return nil, ErrDevBoxConflict
	}
	encoded, err := json.Marshal(final)
	if err != nil {
		return nil, err
	}
	err = tx.GetContext(ctx, job, `UPDATE dev_box_jobs SET status=$2,result=$3,completed_at=clock_timestamp(),
		lease_expires_at=NULL,updated_at=clock_timestamp() WHERE id=$1 RETURNING *`, jobID, final.Status, encoded)
	if err != nil {
		return nil, devBoxError(err)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return job, nil
}

func (r *DevBoxRepository) ListJobLogs(ctx context.Context, workspaceID, jobID, userID uuid.UUID, afterID int64, limit int) ([]domain.DevBoxJobLog, error) {
	if _, err := r.GetJob(ctx, workspaceID, jobID, userID); err != nil {
		return nil, err
	}
	logs := []domain.DevBoxJobLog{}
	err := r.db.SelectContext(ctx, &logs, `SELECT l.* FROM dev_box_job_logs l JOIN dev_box_jobs j ON j.id=l.job_id
		WHERE j.workspace_id=$1 AND j.id=$2 AND j.requested_by_user_id=$3 AND l.id>$4 ORDER BY l.id LIMIT $5`, workspaceID, jobID, userID, afterID, min(max(limit, 1), 500))
	return logs, err
}
