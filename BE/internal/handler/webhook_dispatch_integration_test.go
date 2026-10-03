package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	promdto "github.com/prometheus/client_model/go"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/metrics"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/internal/service"
	"github.com/ku/ku-backend/pkg/crypto"
)

type webhookDelivery struct {
	event     string
	signature string
	body      []byte
}

// webhookReceiver is a real HTTP endpoint collecting signed deliveries.
func webhookReceiver(t *testing.T) (*httptest.Server, <-chan webhookDelivery) {
	t.Helper()
	deliveries := make(chan webhookDelivery, 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		deliveries <- webhookDelivery{event: r.Header.Get("X-Webhook-Event"), signature: r.Header.Get("X-Webhook-Signature"), body: body}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, deliveries
}

// registerWebhook stores a webhook the way WebhookService.Create does
// (encrypted secret). It is inserted directly because URL validation
// rightly refuses loopback receivers.
func registerWebhook(t *testing.T, env *integrationEnv, workspaceID uuid.UUID, url, secret string, events ...string) {
	t.Helper()
	encrypted, err := crypto.Encrypt(secret, crypto.DeriveKey(integrationJWTSecret+":webhook"))
	require.NoError(t, err)
	_, err = env.db.Exec(`INSERT INTO webhooks (id,workspace_id,url,secret,events,is_active) VALUES ($1,$2,$3,$4,$5,true)`,
		uuid.New(), workspaceID, url, encrypted, pq.StringArray(events))
	require.NoError(t, err)
}

func nextDelivery(t *testing.T, deliveries <-chan webhookDelivery, secret string) (webhookDelivery, map[string]any) {
	t.Helper()
	select {
	case d := <-deliveries:
		assert.Equal(t, "sha256="+crypto.HMACSha256(d.body, secret), d.signature, "delivery must be signed with the webhook secret")
		var envelope map[string]any
		require.NoError(t, json.Unmarshal(d.body, &envelope))
		assert.Equal(t, d.event, envelope["event"])
		return d, envelope
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for webhook delivery")
		return webhookDelivery{}, nil
	}
}

// Real HTTP requests to the API (whose request contexts are cancelled as
// soon as each response is written) trigger signed deliveries for issue
// created/updated/deleted and comment created.
func TestOutgoingWebhooks_FireForIssueAndCommentEvents(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("hooks")
	other := env.seedOwner("quiet")
	teamID := env.createTeam(owner, "Hooks", "HOK")
	otherTeam := env.createTeam(other, "Quiet", "QUI")

	receiver, deliveries := webhookReceiver(t)
	const secret = "s3cret-signing-key"
	registerWebhook(t, env, owner.WorkspaceID, receiver.URL, secret, "issue.created", "issue.updated", "issue.deleted", "comment.created")

	api := httptest.NewServer(env.server)
	t.Cleanup(api.Close)
	call := func(method, path string, body any, want int) map[string]any {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		req, err := http.NewRequest(method, api.URL+path, bytes.NewReader(raw))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+owner.Token)
		resp, err := api.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		out := map[string]any{}
		data, _ := io.ReadAll(resp.Body)
		require.Equalf(t, want, resp.StatusCode, "%s %s: %s", method, path, data)
		_ = json.Unmarshal(data, &out)
		return out
	}

	issue := call(http.MethodPost, owner.path("/issues"), map[string]any{"title": "Ship it", "team_id": teamID}, http.StatusCreated)
	identifier := issue["identifier"].(string)
	d, envelope := nextDelivery(t, deliveries, secret)
	assert.Equal(t, "issue.created", d.event)
	assert.Equal(t, identifier, envelope["payload"].(map[string]any)["identifier"])

	call(http.MethodPatch, owner.path("/issues/"+identifier), map[string]any{"title": "Ship it now"}, http.StatusOK)
	d, envelope = nextDelivery(t, deliveries, secret)
	assert.Equal(t, "issue.updated", d.event)
	assert.Equal(t, "Ship it now", envelope["payload"].(map[string]any)["title"])

	call(http.MethodPost, owner.path("/issues/"+identifier+"/comments"), map[string]any{"body": "looks good"}, http.StatusCreated)
	d, envelope = nextDelivery(t, deliveries, secret)
	assert.Equal(t, "comment.created", d.event)
	assert.Equal(t, identifier, envelope["payload"].(map[string]any)["identifier"])

	call(http.MethodDelete, owner.path("/issues/"+identifier), nil, http.StatusOK)
	d, envelope = nextDelivery(t, deliveries, secret)
	assert.Equal(t, "issue.deleted", d.event)
	assert.Equal(t, identifier, envelope["payload"].(map[string]any)["identifier"])

	// Events of another workspace never reach this webhook.
	env.createIssue(other, otherTeam, "unrelated")
	select {
	case d := <-deliveries:
		t.Fatalf("unexpected delivery %q from another workspace", d.event)
	case <-time.After(500 * time.Millisecond):
	}
}

// Dispatch is detached from the caller's context: a context that is
// already cancelled (like a finished request) still delivers.
func TestOutgoingWebhooks_DispatchIgnoresCallerCancellation(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("detached")
	receiver, deliveries := webhookReceiver(t)
	const secret = "detached-secret"
	registerWebhook(t, env, owner.WorkspaceID, receiver.URL, secret, "issue.updated")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env.webhookSvc.Dispatch(ctx, owner.WorkspaceID, "issue.updated", map[string]string{"identifier": "DET-1"})

	d, envelope := nextDelivery(t, deliveries, secret)
	assert.Equal(t, "issue.updated", d.event)
	assert.Equal(t, "DET-1", envelope["payload"].(map[string]any)["identifier"])

	// Unsubscribed events are not delivered.
	env.webhookSvc.Dispatch(context.Background(), owner.WorkspaceID, "issue.created", map[string]string{})
	select {
	case d := <-deliveries:
		t.Fatalf("unexpected delivery for unsubscribed event %q", d.event)
	case <-time.After(500 * time.Millisecond):
	}
}

// project.updated is advertised to webhooks, so updating a project through
// either the HTTP API or MCP must deliver it.
func TestOutgoingWebhooks_FireForProjectUpdates(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("projhooks")
	projectID := uuid.New()
	_, err := env.db.Exec(`INSERT INTO projects (id,workspace_id,name) VALUES ($1,$2,'Launch')`, projectID, owner.WorkspaceID)
	require.NoError(t, err)

	receiver, deliveries := webhookReceiver(t)
	const secret = "project-secret"
	registerWebhook(t, env, owner.WorkspaceID, receiver.URL, secret, "project.updated")

	env.mustJSON(http.MethodPatch, owner.path("/projects/"+projectID.String()), owner.Token,
		map[string]any{"name": "Launch v2"}, http.StatusOK)
	d, envelope := nextDelivery(t, deliveries, secret)
	assert.Equal(t, "project.updated", d.event)
	assert.Equal(t, "Launch v2", envelope["payload"].(map[string]any)["name"])

	_, key, err := env.apiKeySvc.Create(t.Context(), owner.ID, dto.CreateAPIKeyRequest{Name: "projects"})
	require.NoError(t, err)
	server := httptest.NewServer(env.server)
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "project-hooks", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{key}, Timeout: 10 * time.Second},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "update_project",
		Arguments: map[string]any{"workspace": owner.Slug, "project": projectID.String(), "status": "in_progress"}})
	require.NoError(t, err)
	require.False(t, res.IsError)
	d, envelope = nextDelivery(t, deliveries, secret)
	assert.Equal(t, "project.updated", d.event)
	assert.Equal(t, "in_progress", envelope["payload"].(map[string]any)["status"])
}

// A receiver answering with a redirect must not make the delivery follow it
// (it could point at an internal service).
func TestOutgoingWebhooks_DoNotFollowRedirects(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("redirect")
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)
	var hits atomic.Int32
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, target.URL+"/internal", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	registerWebhook(t, env, owner.WorkspaceID, redirector.URL, "redirect-secret", "issue.updated")

	env.webhookSvc.Dispatch(context.Background(), owner.WorkspaceID, "issue.updated", map[string]string{"identifier": "RED-1"})
	require.Eventually(t, func() bool { return hits.Load() == 1 }, 10*time.Second, 20*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.Zero(t, followed.Load(), "the redirect target must never be contacted")
}

// With the production guard, a delivery whose destination resolves to a
// loopback address is refused at connect time, even though the URL was
// stored (as after a DNS change).
func TestOutgoingWebhooks_ProductionGuardRefusesLoopback(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("guard")
	receiver, deliveries := webhookReceiver(t)
	registerWebhook(t, env, owner.WorkspaceID, receiver.URL, "guard-secret", "issue.updated")
	hook := logtest.NewGlobal()
	t.Cleanup(hook.Reset)

	production := service.NewWebhookService(repository.NewWebhookRepository(env.db), integrationJWTSecret)
	production.Dispatch(context.Background(), owner.WorkspaceID, "issue.updated", map[string]string{"identifier": "GRD-1"})

	require.Eventually(t, func() bool {
		for _, entry := range hook.AllEntries() {
			if err, ok := entry.Data["error"].(error); ok && entry.Message == "webhook delivery failed" &&
				strings.Contains(err.Error(), "webhook destination address is not allowed") {
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond, "the refused delivery must be logged")
	select {
	case d := <-deliveries:
		t.Fatalf("loopback receiver was reached with %q", d.event)
	default:
	}
}

// Update reports URL validation problems as 400 with their message, and
// storage failures as a generic, logged 500.
func TestWebhookUpdate_SeparatesValidationFromStorageErrors(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("hookupd")
	registerWebhook(t, env, owner.WorkspaceID, "https://hooks.example.com/in", "upd-secret", "issue.updated")
	var id string
	require.NoError(t, env.db.Get(&id, `SELECT id FROM webhooks WHERE workspace_id=$1`, owner.WorkspaceID))

	rec := env.do(http.MethodPatch, owner.path("/webhooks/"+id), owner.Token, map[string]any{"url": "ftp://example.com/in"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid webhook URL")

	_, err := env.db.Exec(`ALTER TABLE webhooks RENAME TO webhooks_away`)
	require.NoError(t, err)
	rec = env.do(http.MethodPatch, owner.path("/webhooks/"+id), owner.Token, map[string]any{"is_active": false})
	_, err = env.db.Exec(`ALTER TABLE webhooks_away RENAME TO webhooks`)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "webhooks", "storage details must not leak")
}

// blockingReceiver counts deliveries and holds each one until released.
func blockingReceiver(t *testing.T) (*httptest.Server, chan struct{}, *atomic.Int32, chan struct{}) {
	t.Helper()
	arrived := make(chan struct{}, 16)
	release := make(chan struct{})
	var delivered atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Consuming the body lets the server notice an aborted client.
		_, _ = io.ReadAll(r.Body)
		arrived <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		delivered.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, arrived, &delivered, release
}

// releaseOnce closes release exactly once.
func releaseOnce(t *testing.T, release chan struct{}) func() {
	t.Helper()
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock) // runs before the receiver's Close
	return unblock
}

// The delivery pool is bounded: with every worker busy and the queue full,
// further events are dropped (logged and counted) instead of piling up
// goroutines; Close then drains what was accepted.
func TestOutgoingWebhooks_BoundedQueueDropsOverflow(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("overload")
	receiver, arrived, delivered, release := blockingReceiver(t)
	unblock := releaseOnce(t, release)
	registerWebhook(t, env, owner.WorkspaceID, receiver.URL, "overload-secret", "issue.updated")

	svc := service.NewWebhookService(repository.NewWebhookRepository(env.db), integrationJWTSecret).
		WithAllowedAddresses(func(net.IP) bool { return true }).
		WithDeliveryPool(1, 1)
	dispatch := func() {
		svc.Dispatch(context.Background(), owner.WorkspaceID, "issue.updated", map[string]string{"identifier": "OVR-1"})
	}
	droppedBefore := droppedWebhookEvents(t)

	dispatch() // taken by the only worker, which then blocks in the receiver
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("first delivery never arrived")
	}
	dispatch() // waits in the queue
	dispatch() // queue full: dropped
	assert.Equal(t, droppedBefore+1, droppedWebhookEvents(t))

	unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, svc.Close(ctx), "accepted events drain on Close")
	assert.Equal(t, int32(2), delivered.Load(), "both accepted events are delivered, the dropped one is not")

	// After Close nothing is accepted any more.
	dispatch()
	assert.Equal(t, droppedBefore+2, droppedWebhookEvents(t))
}

// Close is bounded by its context: a stuck receiver cannot hold shutdown
// past the deadline; the in-flight delivery is aborted.
func TestOutgoingWebhooks_CloseRespectsDeadline(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("deadline")
	receiver, arrived, delivered, release := blockingReceiver(t)
	releaseOnce(t, release)
	registerWebhook(t, env, owner.WorkspaceID, receiver.URL, "deadline-secret", "issue.updated")

	svc := service.NewWebhookService(repository.NewWebhookRepository(env.db), integrationJWTSecret).
		WithAllowedAddresses(func(net.IP) bool { return true })
	svc.Dispatch(context.Background(), owner.WorkspaceID, "issue.updated", map[string]string{"identifier": "DL-1"})
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("delivery never arrived")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	assert.ErrorIs(t, svc.Close(ctx), context.DeadlineExceeded)
	assert.Less(t, time.Since(started), 5*time.Second)
	assert.Zero(t, delivered.Load())
}

func droppedWebhookEvents(t *testing.T) float64 {
	t.Helper()
	var m promdto.Metric
	require.NoError(t, metrics.WebhookEventsDropped.Write(&m))
	return m.GetCounter().GetValue()
}

// A shutdown whose deadline has already passed discards the backlog: queued
// deliveries, queued dispatches and the rest of an in-progress subscriber
// fan-out are never run (no request, no signing, no failure log).
func TestOutgoingWebhooks_CloseAfterDeadlineDiscardsBacklog(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("backlog")
	receiver, arrived, delivered, release := blockingReceiver(t)
	releaseOnce(t, release)
	for i := range 3 {
		registerWebhook(t, env, owner.WorkspaceID, receiver.URL, "backlog-secret-"+strconv.Itoa(i), "issue.updated")
	}
	hook := logtest.NewGlobal()
	t.Cleanup(hook.Reset)

	// One worker, a queue of one: the dispatch queues the first delivery,
	// delivers the second itself (queue full) and blocks in the receiver
	// with the third subscriber still pending in its fan-out.
	svc := service.NewWebhookService(repository.NewWebhookRepository(env.db), integrationJWTSecret).
		WithAllowedAddresses(func(net.IP) bool { return true }).
		WithDeliveryPool(1, 1)
	svc.Dispatch(context.Background(), owner.WorkspaceID, "issue.updated", map[string]string{"identifier": "BL-1"})
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("first delivery never arrived")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, svc.Close(ctx), context.Canceled)

	select {
	case <-arrived:
		t.Fatal("a discarded delivery reached the receiver")
	case <-time.After(300 * time.Millisecond):
	}
	assert.Zero(t, delivered.Load())
	failures := 0
	for _, entry := range hook.AllEntries() {
		if entry.Message == "webhook delivery failed" {
			failures++
		}
	}
	assert.Equal(t, 1, failures, "only the aborted in-flight delivery is attempted")
}
