package main

import (
	"context"
	"database/sql"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ku/ku-backend/internal/realtime"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/internal/service"
	"github.com/ku/ku-backend/pkg/crypto"
	"github.com/ku/ku-backend/pkg/safego"
)

const shutdownTestSecret = "shutdown-test-secret-at-least-32-characters"

func shutdownTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		baseURL = os.Getenv("DATABASE_URL")
	}
	if baseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", baseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	name := "ku_shutdown_it_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(`CREATE DATABASE "` + name + `"`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`) })
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
	return db
}

// Shutdown must not close the webhook pool while work that feeds it is
// still finishing: a background producer's last iteration (cancelled at
// shutdown start) and an HTTP request being drained both emit events, and
// both must be delivered rather than dropped by a closed pool.
func TestGracefulShutdownDeliversEventsFromProducersAndDrainedRequests(t *testing.T) {
	db := shutdownTestDB(t)
	workspaceID, userID := uuid.New(), uuid.New()
	_, err := db.Exec(`INSERT INTO users (id,email,name,password_hash) VALUES ($1,'shutdown@example.test','Owner','x')`, userID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO workspaces (id,name,slug,owner_id) VALUES ($1,'Shutdown','shutdown',$2)`, workspaceID, userID)
	require.NoError(t, err)

	var mu sync.Mutex
	var received []string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)
	secret, err := crypto.Encrypt("hook-secret", crypto.DeriveKey(shutdownTestSecret+":webhook"))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO webhooks (id,workspace_id,url,secret,events,is_active) VALUES ($1,$2,$3,$4,$5,true)`,
		uuid.New(), workspaceID, receiver.URL, secret, pq.StringArray{"issue.updated"})
	require.NoError(t, err)

	hub := realtime.NewHub()
	webhookSvc := service.NewWebhookService(repository.NewWebhookRepository(db), shutdownTestSecret).
		WithAllowedAddresses(func(net.IP) bool { return true })
	hub.OnBroadcast = webhookSvc.HubListener()
	broadcast := func(source string) {
		hub.Broadcast(workspaceID, realtime.Event{Type: "issue.updated", Payload: map[string]string{"source": source}})
	}

	// A producer whose in-flight iteration outlives both the cancellation
	// and HTTP draining.
	bgCtx, stopBackground := context.WithCancel(context.Background())
	var background safego.Group
	background.Go("test producer", func() {
		<-bgCtx.Done()
		time.Sleep(time.Second)
		broadcast("producer")
	})

	// An HTTP request that is still running when shutdown starts.
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	inRequest := make(chan struct{})
	e.POST("/slow", func(c echo.Context) error {
		close(inRequest)
		time.Sleep(300 * time.Millisecond)
		broadcast("request")
		return c.NoContent(http.StatusOK)
	})
	go func() { _ = e.Start("127.0.0.1:0") }()
	require.Eventually(t, func() bool { return e.ListenerAddr() != nil }, 5*time.Second, 10*time.Millisecond)
	requestDone := make(chan int, 1)
	go func() {
		resp, err := http.Post("http://"+e.ListenerAddr().String()+"/slow", "application/json", nil)
		if err != nil {
			requestDone <- 0
			return
		}
		_ = resp.Body.Close()
		requestDone <- resp.StatusCode
	}()
	<-inRequest

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	gracefulShutdown(ctx, shutdownSteps{
		stopBackground: stopBackground,
		waitBackground: background.Wait,
		drainHTTP:      e.Shutdown,
		drainWebSocket: hub.Shutdown,
		closeWebhooks:  webhookSvc.Close,
	})

	assert.Equal(t, http.StatusOK, <-requestDone, "the in-flight request is drained, not cut")
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, received, 2, "both events are delivered before the pool closes")
	joined := strings.Join(received, "\n")
	assert.Contains(t, joined, `"source":"producer"`)
	assert.Contains(t, joined, `"source":"request"`)
	assert.False(t, background.Go("late", func() {}), "no producer starts after shutdown")
}
