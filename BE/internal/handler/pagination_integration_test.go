package handler

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPublicShareList_ClampsPagination(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("pager")
	teamID := env.createTeam(owner, "Pager", "PAG")
	for _, title := range []string{"one", "two", "three"} {
		env.createIssue(owner, teamID, title)
	}
	link := env.mustJSON(http.MethodPost, owner.path("/shared-links"), owner.Token, map[string]any{"scope": "workspace"}, http.StatusCreated)
	base := "/api/public/share/" + link["token"].(string) + "/issues"

	huge := env.mustJSON(http.MethodGet, base+"?per_page=1000000", "", nil, http.StatusOK)
	assert.EqualValues(t, 100, huge["per_page"], "per_page must be capped at 100")
	assert.EqualValues(t, 1, huge["page"])
	assert.Len(t, huge["data"], 3)

	negative := env.mustJSON(http.MethodGet, base+"?page=-5&per_page=-3", "", nil, http.StatusOK)
	assert.EqualValues(t, 1, negative["page"], "page must be at least 1")
	assert.EqualValues(t, 1, negative["per_page"], "per_page must be at least 1")
	assert.Len(t, negative["data"], 1)
	assert.Equal(t, true, negative["has_more"])
	assert.EqualValues(t, 3, negative["total_count"])

	far := env.mustJSON(http.MethodGet, base+"?page=9223372036854775807&per_page=100", "", nil, http.StatusOK)
	assert.Empty(t, far["data"], "an enormous page must not overflow the offset")
}
