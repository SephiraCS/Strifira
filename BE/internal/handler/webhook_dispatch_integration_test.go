package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
