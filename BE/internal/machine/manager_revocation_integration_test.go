package machine

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/repository"
)

// revocationEnv runs the manager against the real DevMachineRepository on a
// freshly migrated database; only the container runtime is faked.
type revocationEnv struct {
	db          *sqlx.DB
	manager     *Manager
	runtime     *runtimeFake
	workspaceID uuid.UUID
	machineID   uuid.UUID
}

func newRevocationEnv(t *testing.T, status domain.DevMachineStatus, expired bool) *revocationEnv {
	t.Helper()
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		baseURL = os.Getenv("DATABASE_URL")
	}
	if baseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", baseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	name := "strifira_machine_it_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(ctx, `CREATE DATABASE "`+name+`"`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`) })
	parsed, err := url.Parse(baseURL)
	require.NoError(t, err)
	parsed.Path, parsed.RawPath = "/"+name, ""
	migrator, err := migrate.New("file://../../migrations", parsed.String())
	require.NoError(t, err)
	require.NoError(t, migrator.Up())
	_, _ = migrator.Close()
	db, err := sqlx.Connect("pgx", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	env := &revocationEnv{db: db, runtime: &runtimeFake{}, workspaceID: uuid.New(), machineID: uuid.New()}
	userID := uuid.New()
	suffix := strings.ReplaceAll(env.machineID.String(), "-", "")
	_, err = db.Exec(`INSERT INTO users (id,email,name,password_hash) VALUES ($1,$2,'Machine Owner','x')`, userID, suffix+"@example.test")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO workspaces (id,name,slug,owner_id) VALUES ($1,'Machines',$2,$3)`, env.workspaceID, "machines-"+suffix, userID)
	require.NoError(t, err)
	expiry := "NOW()+INTERVAL '1 hour'"
	if expired {
		expiry = "NOW()-INTERVAL '1 minute'"
	}
	_, err = db.Exec(`INSERT INTO dev_machines
		(id,workspace_id,created_by_user_id,routing_key,name,status,desired_status,generation,
		 repo_url,repo_provider,repo_owner,repo_name,base_branch,working_branch,
		 machine_size,cpu_millis,memory_mb,disk_gb,max_runtime_minutes,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$6,7,'','github','','','','',
		 'small',1000,2048,20,480,`+expiry+`)`,
		env.machineID, env.workspaceID, userID, suffix[:16], "machine-"+suffix, status)
	require.NoError(t, err)
	env.manager = NewManager(repository.NewDevMachineRepository(db), env.runtime, nil, nil, nil, make([]byte, 32), nil, "revocation-test")
	return env
}

// failStatement makes every matching statement on table fail inside
// Postgres (statement-level, so it fires even when no rows match).
func (env *revocationEnv) failStatement(t *testing.T, table, event string) func() {
	t.Helper()
	trigger := "fail_" + table
	_, err := env.db.Exec(`CREATE OR REPLACE FUNCTION injected_failure() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected failure'; END $$`)
	require.NoError(t, err)
	_, err = env.db.Exec(`CREATE TRIGGER ` + trigger + ` BEFORE ` + event + ` ON ` + table + ` FOR EACH STATEMENT EXECUTE FUNCTION injected_failure()`)
	require.NoError(t, err)
	return func() {
		_, err := env.db.Exec(`DROP TRIGGER ` + trigger + ` ON ` + table)
		require.NoError(t, err)
	}
}

func (env *revocationEnv) status(t *testing.T) domain.DevMachineStatus {
	t.Helper()
	var status domain.DevMachineStatus
	require.NoError(t, env.db.Get(&status, `SELECT status FROM dev_machines WHERE id=$1`, env.machineID))
	return status
}

func (env *revocationEnv) operation(action domain.DevMachineOperationAction) *domain.DevMachineOperation {
	return &domain.DevMachineOperation{ID: uuid.New(), MachineID: env.machineID, WorkspaceID: env.workspaceID, Action: action, Generation: 7}
}

func TestManagerStopFailsWhenAccessRevocationFails(t *testing.T) {
	env := newRevocationEnv(t, domain.DevMachineStatusRunning, false)
	ctx := context.Background()
	restore := env.failStatement(t, "dev_machine_access_tickets", "UPDATE")

	err := env.manager.processOperation(ctx, env.operation(domain.DevMachineOpStop))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revoke machine access on stop")
	assert.NotEqual(t, domain.DevMachineStatusStopped, env.status(t), "a machine whose access could not be revoked must not report stopped")

	// The retried operation completes once revocation works.
	restore()
	require.NoError(t, env.manager.processOperation(ctx, env.operation(domain.DevMachineOpStop)))
	assert.Equal(t, domain.DevMachineStatusStopped, env.status(t))
}

func TestManagerTeardownFailsBeforeRuntimeWhenAccessRevocationFails(t *testing.T) {
	env := newRevocationEnv(t, domain.DevMachineStatusRunning, false)
	ctx := context.Background()
	restore := env.failStatement(t, "dev_machine_access_sessions", "UPDATE")

	err := env.manager.processOperation(ctx, env.operation(domain.DevMachineOpTeardown))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "revoke machine access on teardown")
	assert.Zero(t, env.runtime.tornDown, "containers must not be destroyed while access is still live")
	assert.NotEqual(t, domain.DevMachineStatusDestroyed, env.status(t))

	restore()
	require.NoError(t, env.manager.processOperation(ctx, env.operation(domain.DevMachineOpTeardown)))
	assert.Equal(t, 1, env.runtime.tornDown)
	assert.Equal(t, domain.DevMachineStatusDestroyed, env.status(t))
}

func TestManagerTTLExpiryLogsAndRetriesFailedTeardownEnqueue(t *testing.T) {
	env := newRevocationEnv(t, domain.DevMachineStatusRunning, true)
	ctx := context.Background()
	hook := logtest.NewGlobal()
	t.Cleanup(hook.Reset)
	restore := env.failStatement(t, "dev_machine_operations", "INSERT")

	_ = env.manager.reconcile(ctx)
	assert.Equal(t, domain.DevMachineStatusExpired, env.status(t))
	var logged bool
	for _, entry := range hook.AllEntries() {
		if entry.Message == "expired machine teardown enqueue failed" && entry.Level == log.ErrorLevel {
			logged = entry.Data["machine_id"] == env.machineID
		}
	}
	assert.True(t, logged, "a failed TTL teardown enqueue must be logged with the machine id")
	var events int
	require.NoError(t, env.db.Get(&events, `SELECT count(*) FROM dev_machine_events WHERE machine_id=$1 AND event_type='machine.expired'`, env.machineID))
	assert.Zero(t, events, "the expiry is not announced until the teardown is queued")

	// The machine stays eligible, so the next pass queues the teardown.
	restore()
	_ = env.manager.reconcile(ctx)
	var queued int
	require.NoError(t, env.db.Get(&queued, `SELECT count(*) FROM dev_machine_operations WHERE machine_id=$1 AND action='teardown'`, env.machineID))
	assert.Equal(t, 1, queued)
	require.NoError(t, env.db.Get(&events, `SELECT count(*) FROM dev_machine_events WHERE machine_id=$1 AND event_type='machine.expired'`, env.machineID))
	assert.Equal(t, 1, events)
}

func TestManagerStopReconciliationRetriesWhenAccessRevocationFails(t *testing.T) {
	env := newRevocationEnv(t, domain.DevMachineStatusFailed, false)
	ctx := context.Background()
	_, err := env.db.Exec(`UPDATE dev_machines SET desired_status='stopped' WHERE id=$1`, env.machineID)
	require.NoError(t, err)
	restore := env.failStatement(t, "dev_machine_access_tickets", "UPDATE")

	_ = env.manager.reconcile(ctx)
	assert.Equal(t, domain.DevMachineStatusFailed, env.status(t), "a machine whose access could not be revoked must not be recorded stopped")

	// The machine stays in runtime reconciliation, so the next pass finishes.
	restore()
	_ = env.manager.reconcile(ctx)
	assert.Equal(t, domain.DevMachineStatusStopped, env.status(t))
}
