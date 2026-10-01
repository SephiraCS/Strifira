package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/stretchr/testify/require"
)

var errEventReplyLost = errors.New("event insert committed but its reply was lost")
var errEventBeforeCommit = errors.New("event failed before commit")

type eventReplyLossConnector struct {
	driver.Connector
	loseReply      atomic.Bool
	failInsert     atomic.Bool
	rollbackCommit atomic.Bool
	holdBoundary   atomic.Bool
	held           chan struct{}
	release        chan struct{}
	backendPID     atomic.Uint32
}

func (c *eventReplyLossConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	c.backendPID.Store(conn.(*stdlib.Conn).Conn().PgConn().PID())
	return &eventReplyLossConn{Conn: conn, connector: c}, nil
}

type eventReplyLossConn struct {
	driver.Conn
	connector *eventReplyLossConnector
	inTx      bool
}

func (c *eventReplyLossConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	c.inTx = true
	return &eventReplyLossTx{Tx: tx, conn: c, ctx: ctx}, nil
}

type eventReplyLossTx struct {
	driver.Tx
	conn *eventReplyLossConn
	ctx  context.Context
}

func (c *eventReplyLossConnector) waitBoundary(ctx context.Context) error {
	if !c.holdBoundary.CompareAndSwap(true, false) {
		return nil
	}
	close(c.held)
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (tx *eventReplyLossTx) Commit() error {
	defer func() { tx.conn.inTx = false }()
	c := tx.conn.connector
	if err := c.waitBoundary(tx.ctx); err != nil {
		return errors.Join(err, tx.Tx.Rollback())
	}
	if c.rollbackCommit.CompareAndSwap(true, false) {
		return errors.Join(errEventBeforeCommit, tx.Tx.Rollback())
	}
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	if c.loseReply.CompareAndSwap(true, false) {
		return errEventReplyLost
	}
	return nil
}

func (tx *eventReplyLossTx) Rollback() error {
	defer func() { tx.conn.inTx = false }()
	return tx.Tx.Rollback()
}

func (c *eventReplyLossConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	insert := strings.Contains(query, "INSERT INTO dev_machine_events")
	if insert {
		if c.connector.failInsert.CompareAndSwap(true, false) {
			return nil, errEventBeforeCommit
		}
		// The rejected reservation implementation is autocommit. Holding its
		// INSERT here exposes the same allocated-cursor gap as an uncommitted row.
		if !c.inTx {
			if err := c.connector.waitBoundary(ctx); err != nil {
				return nil, err
			}
		}
	}
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil || !insert || c.inTx || !c.connector.loseReply.CompareAndSwap(true, false) {
		return rows, err
	}
	// Preserve the autocommit fault for the exact old-code duplicate baseline.
	// The transactional implementation loses the real COMMIT reply above.
	values := make([]driver.Value, len(rows.Columns()))
	for {
		err = rows.Next(values)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.Join(err, rows.Close())
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return nil, errEventReplyLost
}

func TestCreateEventRetriesCommittedInsertWithoutDuplicatingOccurrences(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sqlx.ConnectContext(t.Context(), "pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	fixture := newEnvironmentRaceFixture(t, db)
	machineID := fixture.insertMachine(t, domain.DevMachineStatusRunning, domain.DevMachineStatusRunning, false)
	runID := uuid.New()
	_, err = db.ExecContext(t.Context(), `INSERT INTO dev_machine_agent_runs
		(id,machine_id,workspace_id,requested_by_user_id,provider_id,mode,status,prompt,command_argv,max_runtime_seconds)
		VALUES ($1,$2,$3,$4,'opencode','autonomous','running','event retry','["true"]',60)`,
		runID, machineID, fixture.workspaceID, fixture.userID)
	require.NoError(t, err)

	config, err := pgx.ParseConfig(databaseURL)
	require.NoError(t, err)
	connector := &eventReplyLossConnector{Connector: stdlib.GetConnector(*config)}
	faultDB := sql.OpenDB(connector)
	t.Cleanup(func() { _ = faultDB.Close() })
	repo := NewDevMachineRepository(sqlx.NewDb(faultDB, "pgx"))

	for _, test := range []struct {
		name   string
		linked bool
	}{{"machine event", false}, {"run and actor event", true}} {
		t.Run(test.name, func(t *testing.T) {
			base := domain.DevMachineEvent{
				WorkspaceID: fixture.workspaceID, MachineID: machineID,
				Source: "agent", EventType: "message", Payload: []byte(`{"message":"repeat"}`),
				OccurredAt: time.Now().UTC(),
			}
			if test.linked {
				base.AgentRunID, base.ActorUserID = new(runID), new(fixture.userID)
			}
			var ids []int64
			for range 2 {
				event := base
				connector.loseReply.Store(true)
				require.ErrorIs(t, repo.CreateEvent(t.Context(), &event), errEventReplyLost)
				require.False(t, event.OccurredAt.IsZero())
				require.True(t, event.CreatedAt.IsZero(), "the RETURNING response was discarded")
				retainedOccurrence := event.OccurrenceID
				var id int64
				var createdAt time.Time
				require.NoError(t, db.QueryRowContext(t.Context(),
					`SELECT id,created_at FROM dev_machine_events WHERE machine_id=$1 ORDER BY id DESC LIMIT 1`, machineID).Scan(&id, &createdAt))
				occurredAt := event.OccurredAt
				require.NoError(t, repo.CreateEvent(t.Context(), &event))
				var count int
				require.NoError(t, db.GetContext(t.Context(), &count,
					`SELECT count(*) FROM dev_machine_events WHERE machine_id=$1 AND agent_run_id IS NOT DISTINCT FROM $2::uuid`, machineID, base.AgentRunID))
				require.Equal(t, len(ids)+1, count, "one row per occurrence, including identical occurrences")
				require.NotEqual(t, uuid.Nil, retainedOccurrence, "occurrence identity must be known before commit")
				require.Equal(t, retainedOccurrence, event.OccurrenceID)
				require.Equal(t, id, event.ID)
				require.Equal(t, occurredAt, event.OccurredAt)
				require.True(t, createdAt.Equal(event.CreatedAt), "retry must preserve the original creation time")
				require.NoError(t, repo.CreateEvent(t.Context(), &event), "additional retry remains idempotent")
				require.NoError(t, db.GetContext(t.Context(), &count,
					`SELECT count(*) FROM dev_machine_events WHERE machine_id=$1 AND agent_run_id IS NOT DISTINCT FROM $2::uuid`, machineID, base.AgentRunID))
				require.Equal(t, len(ids)+1, count)
				ids = append(ids, id)
			}
			require.Less(t, ids[0], ids[1], "identical occurrences must receive distinct cursor identities")
		})
	}
}

func TestCreateEventRejectsIdentityReuseForDifferentOccurrence(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sqlx.ConnectContext(t.Context(), "pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	fixture := newEnvironmentRaceFixture(t, db)
	machineID := fixture.insertMachine(t, domain.DevMachineStatusRunning, domain.DevMachineStatusRunning, false)
	event := domain.DevMachineEvent{
		WorkspaceID: fixture.workspaceID, MachineID: machineID, Source: "agent", EventType: "message",
	}
	require.NoError(t, fixture.repository.CreateEvent(t.Context(), &event))
	for _, test := range []struct {
		name   string
		change func(*domain.DevMachineEvent)
	}{
		{"workspace", func(e *domain.DevMachineEvent) { e.WorkspaceID = uuid.New() }},
		{"machine", func(e *domain.DevMachineEvent) { e.MachineID = uuid.New() }},
		{"run", func(e *domain.DevMachineEvent) { e.AgentRunID = new(uuid.New()) }},
		{"actor", func(e *domain.DevMachineEvent) { e.ActorUserID = new(fixture.userID) }},
		{"source", func(e *domain.DevMachineEvent) { e.Source = "manager" }},
		{"type", func(e *domain.DevMachineEvent) { e.EventType = "diagnostic" }},
		{"payload", func(e *domain.DevMachineEvent) { e.Payload = []byte(`{"message":"different"}`) }},
		{"time", func(e *domain.DevMachineEvent) { e.OccurredAt = e.OccurredAt.Add(time.Second) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := event
			test.change(&changed)
			require.ErrorIs(t, fixture.repository.CreateEvent(t.Context(), &changed), sql.ErrNoRows)
		})
	}
	var count int
	require.NoError(t, db.GetContext(t.Context(), &count, `SELECT count(*) FROM dev_machine_events WHERE machine_id=$1`, machineID))
	require.Equal(t, 1, count)
	require.NoError(t, fixture.repository.CreateEvent(t.Context(), &event))
}

func eventTestFixture(t *testing.T) (*sqlx.DB, environmentRaceFixture, uuid.UUID, string) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sqlx.ConnectContext(t.Context(), "pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	fixture := newEnvironmentRaceFixture(t, db)
	machineID := fixture.insertMachine(t, domain.DevMachineStatusRunning, domain.DevMachineStatusRunning, false)
	return db, fixture, machineID, databaseURL
}

func eventFaultRepository(t *testing.T, databaseURL string) (*DevMachineRepository, *eventReplyLossConnector) {
	t.Helper()
	config, err := pgx.ParseConfig(databaseURL)
	require.NoError(t, err)
	connector := &eventReplyLossConnector{Connector: stdlib.GetConnector(*config)}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	return NewDevMachineRepository(sqlx.NewDb(db, "pgx")), connector
}

func TestCreateEventCrossProducerRetryDoesNotFallBehindCursor(t *testing.T) {
	for _, failure := range []string{"insert error", "rolled back commit"} {
		t.Run(failure, func(t *testing.T) {
			db, fixture, machineID, databaseURL := eventTestFixture(t)
			repo, fault := eventFaultRepository(t, databaseURL)
			provider := domain.DevMachineEvent{WorkspaceID: fixture.workspaceID, MachineID: machineID, Source: "agent", EventType: "message"}
			if failure == "insert error" {
				fault.failInsert.Store(true)
			} else {
				fault.rollbackCommit.Store(true)
			}
			require.ErrorIs(t, repo.CreateEvent(t.Context(), &provider), errEventBeforeCommit)
			var count int
			require.NoError(t, db.GetContext(t.Context(), &count, `SELECT count(*) FROM dev_machine_events WHERE machine_id=$1`, machineID))
			require.Zero(t, count, "the failed provider occurrence has not committed")
			collector := domain.DevMachineEvent{WorkspaceID: fixture.workspaceID, MachineID: machineID, Source: "collector", EventType: "diagnostic"}
			require.NoError(t, fixture.repository.CreateEvent(t.Context(), &collector))
			seen, err := fixture.repository.ListEvents(t.Context(), fixture.workspaceID, machineID, 0, 100)
			require.NoError(t, err)
			require.Len(t, seen, 1)
			cursor := seen[0].ID
			require.Equal(t, collector.ID, cursor)
			require.NoError(t, repo.CreateEvent(t.Context(), &provider))
			tail, err := fixture.repository.ListEvents(t.Context(), fixture.workspaceID, machineID, cursor, 100)
			require.NoError(t, err)
			require.Len(t, tail, 1, "retried provider event must remain visible after collector cursor")
			require.Equal(t, provider.ID, tail[0].ID)
			require.Less(t, cursor, provider.ID)
			require.NotEqual(t, uuid.Nil, provider.OccurrenceID)
			require.NoError(t, repo.CreateEvent(t.Context(), &provider))
			require.NoError(t, db.GetContext(t.Context(), &count, `SELECT count(*) FROM dev_machine_events WHERE machine_id=$1`, machineID))
			require.Equal(t, 2, count)
		})
	}
}

func TestCreateEventLostCommitReplyWithAnotherProducer(t *testing.T) {
	db, fixture, machineID, databaseURL := eventTestFixture(t)
	repo, fault := eventFaultRepository(t, databaseURL)
	provider := domain.DevMachineEvent{WorkspaceID: fixture.workspaceID, MachineID: machineID, Source: "agent", EventType: "message"}
	fault.loseReply.Store(true)
	require.ErrorIs(t, repo.CreateEvent(t.Context(), &provider), errEventReplyLost)
	var committedID int64
	require.NoError(t, db.GetContext(t.Context(), &committedID, `SELECT id FROM dev_machine_events WHERE machine_id=$1`, machineID))
	collector := domain.DevMachineEvent{WorkspaceID: fixture.workspaceID, MachineID: machineID, Source: "collector", EventType: "diagnostic"}
	require.NoError(t, fixture.repository.CreateEvent(t.Context(), &collector))
	seen, err := fixture.repository.ListEvents(t.Context(), fixture.workspaceID, machineID, 0, 100)
	require.NoError(t, err)
	require.Len(t, seen, 2, "the committed provider occurrence was visible before collector cursor")
	require.Equal(t, committedID, seen[0].ID)
	require.Equal(t, collector.ID, seen[1].ID)
	require.NoError(t, repo.CreateEvent(t.Context(), &provider))
	tail, err := fixture.repository.ListEvents(t.Context(), fixture.workspaceID, machineID, collector.ID, 100)
	require.NoError(t, err)
	require.Empty(t, tail, "recovering a lost commit reply must not create a new occurrence")
	require.Equal(t, committedID, provider.ID)
}

func TestCreateEventSerializesProducersThroughCommit(t *testing.T) {
	for _, cancelWaiting := range []bool{false, true} {
		name := "commit order"
		if cancelWaiting {
			name = "cancel waiting producer"
		}
		t.Run(name, func(t *testing.T) {
			db, fixture, machineID, databaseURL := eventTestFixture(t)
			otherMachineID := fixture.insertMachine(t, domain.DevMachineStatusRunning, domain.DevMachineStatusRunning, false)
			firstRepo, firstFault := eventFaultRepository(t, databaseURL)
			firstFault.held, firstFault.release = make(chan struct{}), make(chan struct{})
			firstFault.holdBoundary.Store(true)
			release := sync.OnceFunc(func() { close(firstFault.release) })
			defer release()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			first := domain.DevMachineEvent{WorkspaceID: fixture.workspaceID, MachineID: machineID, Source: "agent", EventType: "message"}
			firstDone := make(chan error, 1)
			go func() { firstDone <- firstRepo.CreateEvent(ctx, &first) }()
			select {
			case <-firstFault.held:
			case err := <-firstDone:
				t.Fatalf("first producer failed before the commit barrier: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var count int
			require.NoError(t, db.GetContext(ctx, &count, `SELECT count(*) FROM dev_machine_events WHERE machine_id=$1`, machineID))
			require.Zero(t, count, "first cursor is not yet committed")
			secondRepo, secondFault := eventFaultRepository(t, databaseURL)
			second := domain.DevMachineEvent{WorkspaceID: fixture.workspaceID, MachineID: machineID, Source: "collector", EventType: "diagnostic"}
			secondCtx, cancelSecond := context.WithCancel(ctx)
			defer cancelSecond()
			secondDone := make(chan error, 1)
			go func() { secondDone <- secondRepo.CreateEvent(secondCtx, &second) }()
			require.Eventually(t, func() bool {
				var waiting bool
				err := db.GetContext(ctx, &waiting, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, secondFault.backendPID.Load())
				return err == nil && waiting
			}, 5*time.Second, 10*time.Millisecond, "second producer must wait behind the uncommitted first cursor")
			other := domain.DevMachineEvent{WorkspaceID: fixture.workspaceID, MachineID: otherMachineID, Source: "collector", EventType: "diagnostic"}
			require.NoError(t, fixture.repository.CreateEvent(ctx, &other), "serialization must not block other machines")
			if cancelWaiting {
				cancelSecond()
				select {
				case err := <-secondDone:
					require.ErrorIs(t, err, context.Canceled)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			release()
			require.NoError(t, <-firstDone)
			if cancelWaiting {
				require.NoError(t, secondRepo.CreateEvent(ctx, &second), "a cancelled wait can retry its occurrence")
			} else {
				require.NoError(t, <-secondDone)
			}
			seen, err := fixture.repository.ListEvents(ctx, fixture.workspaceID, machineID, 0, 100)
			require.NoError(t, err)
			require.Len(t, seen, 2)
			require.Equal(t, first.ID, seen[0].ID)
			require.Equal(t, second.ID, seen[1].ID)
			require.Less(t, first.ID, second.ID)
		})
	}
}

func TestCreateEventHandlesDeletedMachine(t *testing.T) {
	db, fixture, machineID, _ := eventTestFixture(t)
	_, err := db.ExecContext(t.Context(), `DELETE FROM dev_machines WHERE id=$1`, machineID)
	require.NoError(t, err)
	event := domain.DevMachineEvent{WorkspaceID: fixture.workspaceID, MachineID: machineID, Source: "agent", EventType: "message"}
	require.ErrorIs(t, fixture.repository.CreateEvent(t.Context(), &event), sql.ErrNoRows)
	require.Zero(t, event.ID)
}

func TestEventOccurrenceIdentityIsPrivate(t *testing.T) {
	event := domain.DevMachineEvent{ID: 123, OccurrenceID: uuid.New()}
	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"id":123`)
	require.NotContains(t, string(encoded), "occurrence_id")
	require.NotContains(t, string(encoded), event.OccurrenceID.String())
}
