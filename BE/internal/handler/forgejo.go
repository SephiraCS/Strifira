package handler

import (
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/service"
	"github.com/ku/ku-backend/pkg/response"
	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"
)

type ForgejoHandler struct {
	fjSvc *service.ForgejoService
}

func NewForgejoHandler(fjSvc *service.ForgejoService) *ForgejoHandler {
	return &ForgejoHandler{fjSvc: fjSvc}
}

// Status returns the Forgejo integration status for the workspace.
func (h *ForgejoHandler) Status(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	status, err := h.fjSvc.GetStatus(c.Request().Context(), ws.ID)
	if err != nil {
		return response.InternalError(c)
	}
	return response.Success(c, http.StatusOK, status)
}

// Connect stores and validates instance URL + token for the workspace.
func (h *ForgejoHandler) Connect(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	var req dto.ConnectForgejoRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	account, err := h.fjSvc.Connect(c.Request().Context(), ws.ID, req.InstanceURL, req.Token)
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", err.Error())
	}
	return response.Success(c, http.StatusOK, account)
}

// Disconnect removes the connection and unlinks all repos.
func (h *ForgejoHandler) Disconnect(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	if err := h.fjSvc.Disconnect(c.Request().Context(), ws.ID); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", err.Error())
	}
	return response.Success(c, http.StatusOK, map[string]string{"status": "disconnected"})
}

// ListRepos lists repositories available to the connection token.
func (h *ForgejoHandler) ListRepos(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	repos, err := h.fjSvc.ListAvailableRepos(c.Request().Context(), ws.ID)
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", err.Error())
	}
	if repos == nil {
		repos = []dto.ForgejoAvailableRepoResponse{}
	}
	return response.Success(c, http.StatusOK, repos)
}

// LinkRepos links selected repos (creating webhooks on them).
func (h *ForgejoHandler) LinkRepos(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	var req dto.LinkForgejoReposRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := h.fjSvc.LinkRepos(c.Request().Context(), ws.ID, req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", err.Error())
	}
	return response.Success(c, http.StatusOK, map[string]string{"status": "linked"})
}

// UnlinkRepo removes a linked repo and its webhook.
func (h *ForgejoHandler) UnlinkRepo(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid repo ID")
	}
	if err := h.fjSvc.UnlinkRepo(c.Request().Context(), ws.ID, id); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", err.Error())
	}
	return response.Success(c, http.StatusOK, map[string]string{"status": "unlinked"})
}

// ListUserMappings returns the workspace's Forgejo login mappings.
func (h *ForgejoHandler) ListUserMappings(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	mappings, err := h.fjSvc.ListUserMappings(c.Request().Context(), ws.ID)
	if err != nil {
		return response.InternalError(c)
	}
	return response.Success(c, http.StatusOK, mappings)
}

func (h *ForgejoHandler) UpdateUserMappings(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	var req dto.UpdateForgejoUserMappingsRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := h.fjSvc.UpdateUserMappings(c.Request().Context(), ws.ID, req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", err.Error())
	}
	return response.Success(c, http.StatusOK, map[string]string{"status": "updated"})
}

// ListAutoTransitions returns the auto-transition rules.
func (h *ForgejoHandler) ListAutoTransitions(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	transitions, err := h.fjSvc.ListAutoTransitions(c.Request().Context(), ws.ID)
	if err != nil {
		return response.InternalError(c)
	}
	return response.Success(c, http.StatusOK, transitions)
}

// UpdateAutoTransitions updates the auto-transition rules.
func (h *ForgejoHandler) UpdateAutoTransitions(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	var req dto.UpdateAutoTransitionsRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := h.fjSvc.UpdateAutoTransitions(c.Request().Context(), ws.ID, req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", err.Error())
	}
	return response.Success(c, http.StatusOK, map[string]string{"status": "updated"})
}

// IssueForgejoActivity returns PRs, branches, commits linked to an issue.
func (h *ForgejoHandler) IssueForgejoActivity(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	identifier := c.Param("identifier")
	activity, err := h.fjSvc.GetIssueActivity(c.Request().Context(), ws.ID, identifier)
	if err != nil {
		return response.Error(c, http.StatusNotFound, "NOT_FOUND", err.Error())
	}
	return response.Success(c, http.StatusOK, activity)
}

// AgentIssueLinks returns issue-PR/branch/commit links for agent consumption.
func (h *ForgejoHandler) AgentIssueLinks(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	identifier := c.QueryParam("identifier")
	if identifier == "" {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "identifier query param required")
	}
	activity, err := h.fjSvc.GetIssueActivity(c.Request().Context(), ws.ID, identifier)
	if err != nil {
		return response.Error(c, http.StatusNotFound, "NOT_FOUND", err.Error())
	}
	return response.Success(c, http.StatusOK, activity)
}

// HandleWebhook receives incoming Forgejo webhook events (public endpoint).
// Forgejo signs deliveries with X-Forgejo-Signature (hex HMAC-SHA256 of the
// raw body); Gitea/X-Gitea-Signature variants are accepted for compat.
func (h *ForgejoHandler) HandleWebhook(c echo.Context) error {
	signature := firstHeader(c, "X-Forgejo-Signature", "X-Gitea-Signature", "X-Gogs-Signature")
	eventType := firstHeader(c, "X-Forgejo-Event", "X-Gitea-Event", "X-Gogs-Event")

	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return c.NoContent(http.StatusBadRequest)
	}

	repo, ok := h.fjSvc.VerifyWebhookSignature(c.Request().Context(), body, signature)
	if !ok {
		return c.NoContent(http.StatusUnauthorized)
	}

	if err := h.fjSvc.HandleWebhookEvent(c.Request().Context(), eventType, body, repo); err != nil {
		log.WithError(err).WithFields(log.Fields{"event": eventType, "repo": repo.FullName}).Error("forgejo webhook processing failed")
		return c.NoContent(http.StatusInternalServerError)
	}

	return c.NoContent(http.StatusOK)
}

func firstHeader(c echo.Context, names ...string) string {
	for _, name := range names {
		if value := c.Request().Header.Get(name); value != "" {
			return value
		}
	}
	return ""
}
