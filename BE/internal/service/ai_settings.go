package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/pkg/crypto"
	"github.com/ku/ku-backend/pkg/sanitize"
)

const DefaultDescriptionExpandPrompt = "Write TipTap-compatible HTML for a Linear-style issue description. Use only an HTML fragment that TipTap StarterKit can parse, such as <p>, <strong>, <em>, <code>, <ul>, <ol>, <li>, <blockquote>, <h2>, and <h3>. Do not return Markdown, code fences, <html>, <body>, scripts, styles, or explanations. Expand or rewrite the given text into clear, actionable issue content with context, expected behavior, acceptance criteria, and relevant edge cases. Keep it concise and practical. Preserve existing meaning and avoid inventing facts."
const DefaultIssueCopyPrompt = "Work on issue {{issue_identifier}}:\n\n{{issue_xml}}"

var (
	ErrAISettingsNotConfigured = errors.New("AI settings are not configured")
	ErrAIProviderRequestFailed = errors.New("AI provider request failed")
	ErrAIInvalidText           = errors.New("text to rewrite must not be blank")
	ErrIssueNotFound           = errors.New("issue not found")
)

type AISettingsService struct {
	aiRepo        repository.AISettingsRepo
	workspaceRepo repository.WorkspaceRepo
	issueRepo     repository.IssueRepo
	encryptionKey []byte
	httpClient    *http.Client
}

func NewAISettingsService(aiRepo repository.AISettingsRepo, workspaceRepo repository.WorkspaceRepo, issueRepo repository.IssueRepo, encryptionKey []byte) *AISettingsService {
	return &AISettingsService{
		aiRepo:        aiRepo,
		workspaceRepo: workspaceRepo,
		issueRepo:     issueRepo,
		encryptionKey: encryptionKey,
		httpClient:    &http.Client{Timeout: 45 * time.Second},
	}
}

func (s *AISettingsService) Get(ctx context.Context, workspaceID uuid.UUID) (*domain.AISettings, error) {
	settings, err := s.aiRepo.GetByWorkspaceID(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if settings == nil {
		return defaultAISettings(workspaceID), nil
	}
	if strings.TrimSpace(settings.Provider) == "" {
		settings.Provider = domain.AIProviderOpenAICompatible
	}
	if strings.TrimSpace(settings.DescriptionExpandPrompt) == "" {
		settings.DescriptionExpandPrompt = DefaultDescriptionExpandPrompt
	}
	if strings.TrimSpace(settings.IssueCopyPrompt) == "" {
		settings.IssueCopyPrompt = DefaultIssueCopyPrompt
	}
	return settings, nil
}

func (s *AISettingsService) Update(ctx context.Context, workspaceID, userID uuid.UUID, req dto.UpdateAISettingsRequest) (*domain.AISettings, error) {
	ws, err := s.workspaceRepo.GetByID(ctx, workspaceID)
	if err != nil || ws == nil {
		return nil, ErrWorkspaceNotFound
	}
	if ws.OwnerID != userID {
		return nil, ErrNotWorkspaceOwner
	}

	settings, err := s.Get(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if req.Provider != nil {
		settings.Provider = strings.TrimSpace(*req.Provider)
	}
	if req.BaseURL != nil {
		settings.BaseURL = strings.TrimRight(strings.TrimSpace(*req.BaseURL), "/")
	}
	if req.Model != nil {
		settings.Model = strings.TrimSpace(*req.Model)
	}
	if req.DescriptionExpandPrompt != nil {
		prompt := strings.TrimSpace(*req.DescriptionExpandPrompt)
		if prompt == "" {
			prompt = DefaultDescriptionExpandPrompt
		}
		settings.DescriptionExpandPrompt = prompt
	}
	if req.IssueCopyPrompt != nil {
		prompt := strings.TrimSpace(*req.IssueCopyPrompt)
		if prompt == "" {
			prompt = DefaultIssueCopyPrompt
		}
		settings.IssueCopyPrompt = prompt
	}
	if req.APIKey.Set {
		if req.APIKey.Value == nil || strings.TrimSpace(*req.APIKey.Value) == "" {
			settings.APIKeyEncrypted = nil
		} else {
			encrypted, err := crypto.Encrypt(strings.TrimSpace(*req.APIKey.Value), s.encryptionKey)
			if err != nil {
				return nil, err
			}
			settings.APIKeyEncrypted = new(encrypted)
		}
	}

	if settings.Provider == "" {
		settings.Provider = domain.AIProviderOpenAICompatible
	}
	if settings.Provider != domain.AIProviderOpenAICompatible {
		return nil, fmt.Errorf("unsupported AI provider")
	}
	if settings.BaseURL != "" {
		if err := validateAIBaseURL(settings.BaseURL); err != nil {
			return nil, err
		}
	}

	if err := s.aiRepo.Upsert(ctx, settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func (s *AISettingsService) ExpandIssueDescription(ctx context.Context, workspaceID uuid.UUID, identifier string, selectedText *string) (string, error) {
	settings, apiKey, err := s.configuredProvider(ctx, workspaceID)
	if err != nil {
		return "", err
	}

	issue, err := s.issueRepo.GetByIdentifier(ctx, workspaceID, identifier)
	if err != nil || issue == nil {
		return "", ErrIssueNotFound
	}

	currentDescription := ""
	if issue.Description != nil {
		currentDescription = *issue.Description
	}

	selected := ""
	if selectedText != nil {
		selected = strings.TrimSpace(*selectedText)
	}

	userPrompt := fmt.Sprintf("%s\n\nIssue title:\n%s\n\nCurrent description:\n%s", settings.DescriptionExpandPrompt, issue.Title, currentDescription)
	if selected != "" {
		userPrompt += fmt.Sprintf("\n\nSelected text to rewrite. Return only the replacement HTML for this selected text, not the full issue description:\n%s", selected)
	}
	content, err := s.complete(ctx, settings.BaseURL, settings.Model, apiKey, userPrompt)
	if err != nil {
		return "", err
	}
	return sanitizeAIContent(content)
}

// ReworkText rewrites an arbitrary editor selection (comment text, new-issue
// description, ...) with the workspace AI settings. It is the workspace-scoped
// sibling of ExpandIssueDescription, which keeps issue context.
func (s *AISettingsService) ReworkText(ctx context.Context, workspaceID uuid.UUID, text string, context *string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ErrAIInvalidText
	}
	settings, apiKey, err := s.configuredProvider(ctx, workspaceID)
	if err != nil {
		return "", err
	}

	userPrompt := settings.DescriptionExpandPrompt
	if context != nil && strings.TrimSpace(*context) != "" {
		userPrompt += fmt.Sprintf("\n\nContext:\n%s", strings.TrimSpace(*context))
	}
	userPrompt += fmt.Sprintf("\n\nText to rewrite. Return only the replacement HTML for this text:\n%s", text)
	content, err := s.complete(ctx, settings.BaseURL, settings.Model, apiKey, userPrompt)
	if err != nil {
		return "", err
	}
	return sanitizeAIContent(content)
}

// TestConnection checks the saved configuration with the same HTML completion
// used by the editor. It never accepts or returns provider credentials.
func (s *AISettingsService) TestConnection(ctx context.Context, workspaceID, userID uuid.UUID) (dto.AIConnectionTestResponse, error) {
	ws, err := s.workspaceRepo.GetByID(ctx, workspaceID)
	if err != nil {
		return dto.AIConnectionTestResponse{}, err
	}
	if ws == nil {
		return dto.AIConnectionTestResponse{}, ErrWorkspaceNotFound
	}
	if ws.OwnerID == uuid.Nil || ws.OwnerID != userID {
		return dto.AIConnectionTestResponse{}, ErrNotWorkspaceOwner
	}
	settings, apiKey, err := s.configuredProvider(ctx, workspaceID)
	if err != nil {
		return dto.AIConnectionTestResponse{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	start := time.Now()
	content, err := s.complete(ctx, settings.BaseURL, settings.Model, apiKey, "Reply with only <p>Connection successful.</p>. Keep the response to one short paragraph.")
	if err != nil {
		return dto.AIConnectionTestResponse{}, err
	}
	if _, err := sanitizeAIContent(content); err != nil {
		return dto.AIConnectionTestResponse{}, err
	}
	return dto.AIConnectionTestResponse{Model: settings.Model, LatencyMS: time.Since(start).Milliseconds()}, nil
}

func (s *AISettingsService) configuredProvider(ctx context.Context, workspaceID uuid.UUID) (*domain.AISettings, string, error) {
	settings, err := s.Get(ctx, workspaceID)
	if err != nil {
		return nil, "", err
	}
	if settings.Provider != domain.AIProviderOpenAICompatible || strings.TrimSpace(settings.Model) == "" || validateAIBaseURL(settings.BaseURL) != nil || settings.APIKeyEncrypted == nil || *settings.APIKeyEncrypted == "" {
		return nil, "", ErrAISettingsNotConfigured
	}
	apiKey, err := crypto.Decrypt(*settings.APIKeyEncrypted, s.encryptionKey)
	if err != nil || strings.TrimSpace(apiKey) == "" {
		return nil, "", ErrAISettingsNotConfigured
	}
	return settings, apiKey, nil
}

func sanitizeAIContent(content string) (string, error) {
	content = strings.TrimSpace(stripCodeFence(content))
	// Reject unsafe-only output before markdown normalization can escape its
	// tags as visible text. Normalize the original once to preserve entities in
	// plain text, Markdown, and already-escaped HTML.
	if sanitize.PlainText(sanitize.StripHTML(content)) == "" {
		return "", fmt.Errorf("%w: provider returned empty content", ErrAIProviderRequestFailed)
	}
	content = sanitize.SanitizeEditorContent(content)
	if sanitize.PlainText(sanitize.StripHTML(content)) == "" {
		return "", fmt.Errorf("%w: provider returned empty content", ErrAIProviderRequestFailed)
	}
	return content, nil
}

func defaultAISettings(workspaceID uuid.UUID) *domain.AISettings {
	return &domain.AISettings{
		WorkspaceID:             workspaceID,
		Provider:                domain.AIProviderOpenAICompatible,
		DescriptionExpandPrompt: DefaultDescriptionExpandPrompt,
		IssueCopyPrompt:         DefaultIssueCopyPrompt,
	}
}

func ToAISettingsResponse(settings *domain.AISettings) dto.AISettingsResponse {
	return dto.AISettingsResponse{
		Provider:                settings.Provider,
		BaseURL:                 settings.BaseURL,
		Model:                   settings.Model,
		HasAPIKey:               settings.APIKeyEncrypted != nil && *settings.APIKeyEncrypted != "",
		DescriptionExpandPrompt: settings.DescriptionExpandPrompt,
		DefaultPrompt:           DefaultDescriptionExpandPrompt,
		IssueCopyPrompt:         settings.IssueCopyPrompt,
		DefaultIssueCopyPrompt:  DefaultIssueCopyPrompt,
		CreatedAt:               settings.CreatedAt,
		UpdatedAt:               settings.UpdatedAt,
	}
}

func validateAIBaseURL(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("base URL must be a valid URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("base URL must be an http or https URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(rawURL, "#") {
		return fmt.Errorf("base URL must not contain credentials, query parameters, or a fragment")
	}
	return nil
}

type chatCompletionRequest struct {
	Model    string                  `json:"model"`
	Messages []chatCompletionMessage `json:"messages"`
}

type chatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message      chatCompletionMessage `json:"message"`
		FinishReason string                `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *AISettingsService) complete(ctx context.Context, baseURL, model, apiKey, prompt string) (string, error) {
	if err := validateAIBaseURL(baseURL); err != nil {
		return "", fmt.Errorf("%w: invalid base URL", ErrAIProviderRequestFailed)
	}
	payload := chatCompletionRequest{
		Model: model,
		Messages: []chatCompletionMessage{
			{Role: "system", Content: "You produce safe TipTap/ProseMirror-compatible HTML fragments. Return only parseable HTML nodes, never Markdown fences or surrounding prose."},
			{Role: "user", Content: prompt},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	endpoint := strings.TrimRight(baseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("%w: invalid provider request", ErrAIProviderRequestFailed)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	// Do not follow redirects: the bearer token belongs only to the configured
	// endpoint, and a redirect must never carry it to another endpoint or origin.
	client := *s.httpClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", fmt.Errorf("%w: %w", ErrAIProviderRequestFailed, context.Canceled)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("%w: %w", ErrAIProviderRequestFailed, context.DeadlineExceeded)
		}
		return "", fmt.Errorf("%w: unable to reach provider", ErrAIProviderRequestFailed)
	}
	defer resp.Body.Close()

	const maxResponseBytes = 1 << 20
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: unable to read provider response", ErrAIProviderRequestFailed)
	}
	if len(respBody) > maxResponseBytes {
		return "", fmt.Errorf("%w: provider response is too large", ErrAIProviderRequestFailed)
	}
	// Upstream error text can echo credentials, URLs, or request content. Report
	// only the HTTP status and controlled diagnostics to callers.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%w: provider returned HTTP status %d", ErrAIProviderRequestFailed, resp.StatusCode)
	}
	var parsed chatCompletionResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("%w: invalid provider response", ErrAIProviderRequestFailed)
	}
	if parsed.Error != nil || len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("%w: provider returned no completion", ErrAIProviderRequestFailed)
	}
	if reason := parsed.Choices[0].FinishReason; reason != "" && reason != "stop" {
		return "", fmt.Errorf("%w: provider did not finish the completion", ErrAIProviderRequestFailed)
	}
	return parsed.Choices[0].Message.Content, nil
}

func stripCodeFence(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "```") {
		return content
	}
	lines := strings.Split(content, "\n")
	if len(lines) >= 3 {
		lines = lines[1:]
		if strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
			lines = lines[:len(lines)-1]
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
