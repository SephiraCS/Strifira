package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/aiegress"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type aiSettingsTestRepo struct {
	settings *domain.AISettings
	err      error
	gets     int
	saves    int
}

func (r *aiSettingsTestRepo) GetByWorkspaceID(context.Context, uuid.UUID) (*domain.AISettings, error) {
	r.gets++
	if r.settings == nil {
		return nil, r.err
	}
	settings := *r.settings
	return &settings, r.err
}

func (r *aiSettingsTestRepo) Upsert(_ context.Context, settings *domain.AISettings) error {
	r.saves++
	r.settings = settings
	return r.err
}

type aiWorkspaceTestRepo struct {
	repository.WorkspaceRepo
	workspace *domain.Workspace
	err       error
}

func (r *aiWorkspaceTestRepo) GetByID(context.Context, uuid.UUID) (*domain.Workspace, error) {
	return r.workspace, r.err
}

type aiIssueTestRepo struct {
	repository.IssueRepo
	issue *domain.Issue
}

func (r *aiIssueTestRepo) GetByIdentifier(context.Context, uuid.UUID, string) (*domain.Issue, error) {
	return r.issue, nil
}

func newAIProviderTestPolicy(t *testing.T, baseURL string) aiegress.Policy {
	t.Helper()
	parsed, err := url.Parse(baseURL)
	require.NoError(t, err)
	policy, err := aiegress.NewPolicy(parsed.Scheme + "://" + parsed.Host)
	require.NoError(t, err)
	return policy
}

func newAISettingsTestService(t *testing.T, baseURL string) (*AISettingsService, *aiSettingsTestRepo, *domain.Workspace) {
	t.Helper()
	key := crypto.DeriveKey("ai-test-encryption")
	encrypted, err := crypto.Encrypt("test-provider-secret", key)
	require.NoError(t, err)
	workspace := &domain.Workspace{ID: uuid.New(), OwnerID: uuid.New()}
	repo := &aiSettingsTestRepo{settings: &domain.AISettings{
		WorkspaceID:     workspace.ID,
		Provider:        domain.AIProviderOpenAICompatible,
		BaseURL:         baseURL,
		Model:           "reasoning-model",
		APIKeyEncrypted: new(encrypted),
	}}
	service := NewAISettingsService(repo, &aiWorkspaceTestRepo{workspace: workspace}, &aiIssueTestRepo{issue: &domain.Issue{Title: "Fix saving issue"}}, key, newAIProviderTestPolicy(t, baseURL))
	return service, repo, workspace
}

func TestAISettingsHTMLCompletionUsesSavedProvider(t *testing.T) {
	requests := make(chan chatCompletionRequest, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer test-provider-secret", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}
		var fields map[string]json.RawMessage
		assert.NoError(t, json.Unmarshal(body, &fields))
		assert.NotContains(t, fields, "temperature", "optional parameters must not break reasoning-model providers")
		var request chatCompletionRequest
		assert.NoError(t, json.Unmarshal(body, &request))
		assert.Equal(t, "reasoning-model", request.Model)
		assert.Len(t, request.Messages, 2)
		requests <- request
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "```html\n<p onclick=\"alert(1)\">Clear <strong>steps</strong></p><script>alert(1)</script>\n```"}}}})
	}))
	t.Cleanup(server.Close)
	svc, _, workspace := newAISettingsTestService(t, server.URL+"/v1")

	// An empty issue description can still be generated from its title.
	html, err := svc.ExpandIssueDescription(t.Context(), workspace.ID, "KU-1", nil)
	require.NoError(t, err)
	assert.Equal(t, "<p>Clear <strong>steps</strong></p>", html)
	assert.Contains(t, (<-requests).Messages[1].Content, "Issue title:\nFix saving issue")

	html, err = svc.ExpandIssueDescription(t.Context(), workspace.ID, "KU-1", new("selected passage"))
	require.NoError(t, err)
	assert.Equal(t, "<p>Clear <strong>steps</strong></p>", html)
	selectionRequest := <-requests
	assert.Contains(t, selectionRequest.Messages[1].Content, "Selected text to rewrite")
	assert.Contains(t, selectionRequest.Messages[1].Content, "selected passage")

	html, err = svc.ReworkText(t.Context(), workspace.ID, " draft ", new(" comment on KU-1 "))
	require.NoError(t, err)
	assert.Equal(t, "<p>Clear <strong>steps</strong></p>", html)
	reworkRequest := <-requests
	assert.Contains(t, reworkRequest.Messages[1].Content, "Context:\ncomment on KU-1")
	assert.Contains(t, reworkRequest.Messages[1].Content, "text:\ndraft")
}

func TestAISettingsPreservesGeneratedTextEntities(t *testing.T) {
	for _, test := range []struct {
		content string
		html    string
	}{
		{content: "A & B", html: "<p>A &amp; B</p>"},
		{content: "A < B > C", html: "<p>A &lt; B &gt; C</p>"},
		{content: "**A & B**", html: "<p><strong>A &amp; B</strong></p>"},
		{content: "`A < B & C`", html: "<p><code>A &lt; B &amp; C</code></p>"},
		{content: "<p>A &amp; B &lt; C</p>", html: "<p>A &amp; B &lt; C</p>"},
	} {
		t.Run(test.content, func(t *testing.T) {
			html, err := sanitizeAIContent(test.content)
			require.NoError(t, err)
			assert.Equal(t, test.html, html)
		})
	}
}

func TestAISettingsRejectsEmptySanitizedCompletion(t *testing.T) {
	for _, content := range []string{"", "  ", "<script>alert(1)</script>", "<p><script>alert(1)</script></p>", "<p>&nbsp; <br></p>", "```html\n<p></p>\n```"} {
		t.Run(content, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
			}))
			t.Cleanup(server.Close)
			svc, _, workspace := newAISettingsTestService(t, server.URL)
			result, err := svc.ReworkText(t.Context(), workspace.ID, "draft", nil)
			assert.Empty(t, result)
			assert.ErrorIs(t, err, ErrAIProviderRequestFailed)
		})
	}
}

func TestAISettingsRejectsInvalidCompletionResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "authentication", status: http.StatusUnauthorized, body: `{"error":{"message":"test-provider-secret http://private.example/secret"}}`},
		{name: "server error", status: http.StatusBadGateway, body: `<html>provider is down</html>`},
		{name: "malformed JSON", status: http.StatusOK, body: `{"choices":`},
		{name: "missing completion", status: http.StatusOK, body: `{}`},
		{name: "empty choices", status: http.StatusOK, body: `{"choices":[]}`},
		{name: "provider error in success", status: http.StatusOK, body: `{"error":{"message":"test-provider-secret"},"choices":[{"message":{"content":"<p>Ignored</p>"}}]}`},
		{name: "truncated completion", status: http.StatusOK, body: `{"choices":[{"message":{"content":"<p>Partial paragraph"},"finish_reason":"length"}]}`},
		{name: "filtered completion", status: http.StatusOK, body: `{"choices":[{"message":{"content":"<p>Partial paragraph"},"finish_reason":"content_filter"}]}`},
		{name: "oversized", status: http.StatusOK, body: `{"padding":"` + strings.Repeat("x", 1<<20) + `","choices":[{"message":{"content":"<p>Ignored</p>"}}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			t.Cleanup(server.Close)
			svc := NewAISettingsService(nil, nil, nil, nil, newAIProviderTestPolicy(t, server.URL))
			content, err := svc.complete(t.Context(), server.URL, "model", "test-provider-secret", "prompt")
			assert.Empty(t, content)
			require.ErrorIs(t, err, ErrAIProviderRequestFailed)
			assert.NotContains(t, err.Error(), "test-provider-secret")
			assert.NotContains(t, err.Error(), server.URL)
			assert.NotContains(t, err.Error(), "private.example")
		})
	}
}

type aiRoundTripFunc func(*http.Request) (*http.Response, error)

func (f aiRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type aiErrorReader struct{}

func (aiErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("test-provider-secret http://private.example/secret")
}

func TestAISettingsRejectsReadAndTransportFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		transport aiRoundTripFunc
	}{
		{name: "partial read", transport: func(*http.Request) (*http.Response, error) {
			valid := `{"choices":[{"message":{"content":"<p>Partial</p>"}}]}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(io.MultiReader(strings.NewReader(valid), aiErrorReader{}))}, nil
		}},
		{name: "transport", transport: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("test-provider-secret http://private.example/secret")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := NewAISettingsService(nil, nil, nil, nil, aiegress.Policy{})
			svc.httpClient.Transport = test.transport
			content, err := svc.complete(t.Context(), "http://private.example/secret", "model", "test-provider-secret", "prompt")
			assert.Empty(t, content)
			require.ErrorIs(t, err, ErrAIProviderRequestFailed)
			assert.NotContains(t, err.Error(), "test-provider-secret")
			assert.NotContains(t, err.Error(), "private.example")
		})
	}
}

func TestAISettingsDoesNotForwardCredentialsOnRedirect(t *testing.T) {
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectedRequests.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"<p>Unexpected</p>"}}]}`)
	}))
	t.Cleanup(target.Close)
	for _, code := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer test-provider-secret", r.Header.Get("Authorization"))
				http.Redirect(w, r, target.URL, code)
			}))
			t.Cleanup(server.Close)
			svc := NewAISettingsService(nil, nil, nil, nil, newAIProviderTestPolicy(t, server.URL))
			_, err := svc.complete(t.Context(), server.URL, "model", "test-provider-secret", "prompt")
			assert.ErrorIs(t, err, ErrAIProviderRequestFailed)
		})
	}
	assert.Zero(t, redirectedRequests.Load())
}

func TestAISettingsCompletionCancellation(t *testing.T) {
	received := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(received)
		select {
		case <-r.Context().Done():
		case <-t.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	svc := NewAISettingsService(nil, nil, nil, nil, newAIProviderTestPolicy(t, server.URL))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := svc.complete(ctx, server.URL, "model", "test-provider-secret", "prompt")
		result <- err
	}()
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("provider request was not received")
	}
	cancel()
	select {
	case err := <-result:
		assert.ErrorIs(t, err, ErrAIProviderRequestFailed)
		assert.ErrorIs(t, err, context.Canceled)
		assert.NotContains(t, err.Error(), server.URL)
	case <-time.After(2 * time.Second):
		t.Fatal("provider request did not stop after cancellation")
	}
}

func TestAISettingsBaseURLValidation(t *testing.T) {
	for _, baseURL := range []string{"https://api.example/v1", "http://127.0.0.1:11434/v1", "http://localhost:8080/openai", "http://[::1]:1234/v1"} {
		assert.NoError(t, validateAIBaseURL(baseURL), baseURL)
	}
	for _, baseURL := range []string{"", "relative/v1", "ftp://api.example/v1", "https://user:password@api.example/v1", "https://api.example/v1?api_key=secret", "https://api.example/v1?", "https://api.example/v1#fragment", "https://api.example/v1#"} {
		t.Run(baseURL, func(t *testing.T) {
			svc, repo, workspace := newAISettingsTestService(t, "https://api.example/v1")
			assert.Error(t, validateAIBaseURL(baseURL))
			_, err := svc.complete(t.Context(), baseURL, "model", "secret", "prompt")
			assert.ErrorIs(t, err, ErrAIProviderRequestFailed)
			if baseURL != "" { // Clearing the endpoint is allowed when saving settings.
				_, err = svc.Update(t.Context(), workspace.ID, workspace.OwnerID, dto.UpdateAISettingsRequest{BaseURL: new(baseURL)})
				assert.Error(t, err)
				assert.Zero(t, repo.saves)
			}
		})
	}
}

func TestAISettingsConnectionTestUsesSavedSettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatCompletionRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		assert.Equal(t, "reasoning-model", request.Model)
		assert.Contains(t, request.Messages[1].Content, "Connection successful")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"<p>Connection successful.</p>"}}]}`)
	}))
	t.Cleanup(server.Close)
	svc, repo, workspace := newAISettingsTestService(t, server.URL)
	transport := svc.httpClient.Transport
	svc.httpClient.Transport = aiRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		assert.True(t, ok, "connection testing must have a deadline")
		assert.LessOrEqual(t, time.Until(deadline), 15*time.Second)
		return transport.RoundTrip(r)
	})
	result, err := svc.TestConnection(t.Context(), workspace.ID, workspace.OwnerID)
	require.NoError(t, err)
	assert.Equal(t, "reasoning-model", result.Model)
	assert.GreaterOrEqual(t, result.LatencyMS, int64(0))
	assert.Zero(t, repo.saves)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "test-provider-secret")
}

func TestAISettingsConnectionTestAuthorizationAndConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		modify func(*AISettingsService, *aiSettingsTestRepo, *domain.Workspace)
		user   func(*domain.Workspace) uuid.UUID
		err    error
	}{
		{name: "nonowner", user: func(*domain.Workspace) uuid.UUID { return uuid.New() }, err: ErrNotWorkspaceOwner},
		{name: "missing workspace", modify: func(s *AISettingsService, _ *aiSettingsTestRepo, _ *domain.Workspace) {
			s.workspaceRepo = &aiWorkspaceTestRepo{}
		}, err: ErrWorkspaceNotFound},
		{name: "no settings", modify: func(_ *AISettingsService, r *aiSettingsTestRepo, _ *domain.Workspace) { r.settings = nil }, err: ErrAISettingsNotConfigured},
		{name: "no model", modify: func(_ *AISettingsService, r *aiSettingsTestRepo, _ *domain.Workspace) { r.settings.Model = " " }, err: ErrAISettingsNotConfigured},
		{name: "no key", modify: func(_ *AISettingsService, r *aiSettingsTestRepo, _ *domain.Workspace) {
			r.settings.APIKeyEncrypted = nil
		}, err: ErrAISettingsNotConfigured},
		{name: "bad ciphertext", modify: func(_ *AISettingsService, r *aiSettingsTestRepo, _ *domain.Workspace) {
			r.settings.APIKeyEncrypted = new("broken")
		}, err: ErrAISettingsNotConfigured},
		{name: "unsafe saved URL", modify: func(_ *AISettingsService, r *aiSettingsTestRepo, _ *domain.Workspace) {
			r.settings.BaseURL = "https://user:password@api.example"
		}, err: ErrAISettingsNotConfigured},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, repo, workspace := newAISettingsTestService(t, "http://127.0.0.1:1")
			if test.modify != nil {
				test.modify(svc, repo, workspace)
			}
			userID := workspace.OwnerID
			if test.user != nil {
				userID = test.user(workspace)
			}
			_, err := svc.TestConnection(t.Context(), workspace.ID, userID)
			assert.ErrorIs(t, err, test.err)
			assert.Zero(t, repo.saves)
			if errors.Is(test.err, ErrNotWorkspaceOwner) {
				assert.Zero(t, repo.gets, "authorization must happen before loading a provider key")
			}
		})
	}
}

func TestAISettingsNoConfigurationAndBlankRework(t *testing.T) {
	svc := NewAISettingsService(&aiSettingsTestRepo{}, nil, nil, nil, aiegress.Policy{})
	_, err := svc.ExpandIssueDescription(t.Context(), uuid.New(), "KU-1", nil)
	assert.ErrorIs(t, err, ErrAISettingsNotConfigured)
	_, err = svc.ReworkText(t.Context(), uuid.New(), "draft", nil)
	assert.ErrorIs(t, err, ErrAISettingsNotConfigured)
	_, err = svc.ReworkText(t.Context(), uuid.New(), " \n\t ", nil)
	assert.ErrorIs(t, err, ErrAIInvalidText)
}

func TestAISettingsBlocksUnapprovedPrivateProviderWithoutLeakingDetails(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(provider.Close)
	svc := NewAISettingsService(nil, nil, nil, nil, aiegress.Policy{})
	content, err := svc.complete(t.Context(), provider.URL+"/v1", "model", "private-provider-secret", "prompt")
	assert.Empty(t, content)
	require.ErrorIs(t, err, ErrAIProviderRequestFailed)
	assert.Contains(t, err.Error(), "server network policy")
	assert.NotContains(t, err.Error(), "private-provider-secret")
	assert.NotContains(t, err.Error(), provider.URL)
	assert.Zero(t, calls.Load())
}
