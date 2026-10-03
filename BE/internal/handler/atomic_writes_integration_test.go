package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTeamCreate_WritesTeamMemberAndStatusesTogether(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("teams")

	teamID := env.createTeam(owner, "Core", "CORE")
	assert.Equal(t, 1, env.count(`SELECT count(*) FROM team_members WHERE team_id=$1 AND user_id=$2`, teamID, owner.ID))
	assert.Equal(t, 6, env.count(`SELECT count(*) FROM team_statuses WHERE team_id=$1 AND is_default`, teamID))

	// Make the fifth default status fail inside the database. The whole
	// creation must roll back and report the failure.
	_, err := env.db.Exec(`
		CREATE FUNCTION fail_doomed_status() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.slug = 'done' AND EXISTS (SELECT 1 FROM teams WHERE id = NEW.team_id AND name = 'Doomed') THEN
				RAISE EXCEPTION 'injected status failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER fail_doomed_status BEFORE INSERT ON team_statuses
			FOR EACH ROW EXECUTE FUNCTION fail_doomed_status();`)
	require.NoError(t, err)

	rec := env.do(http.MethodPost, owner.path("/teams"), owner.Token, map[string]any{"name": "Doomed", "key": "DOOM"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "injected", "database errors must not leak to clients")
	assert.Zero(t, env.count(`SELECT count(*) FROM teams WHERE name='Doomed'`), "no half-created team may remain")
	assert.Equal(t, 1, env.count(`SELECT count(*) FROM team_members`), "no orphan membership may remain")
	assert.Equal(t, 6, env.count(`SELECT count(*) FROM team_statuses`), "no orphan statuses may remain")

	// The key was not consumed by the failed attempt.
	_, err = env.db.Exec(`DROP TRIGGER fail_doomed_status ON team_statuses`)
	require.NoError(t, err)
	env.createTeam(owner, "Doomed", "DOOM")
}

func TestIssueRelationCreate_InverseIsAtomic(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("relations")
	teamID := env.createTeam(owner, "Rel", "REL")
	first := env.createIssue(owner, teamID, "first")
	second := env.createIssue(owner, teamID, "second")

	// Fail every inverse ("blocked_by") row at the database.
	_, err := env.db.Exec(`
		CREATE FUNCTION fail_blocked_by() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.type = 'blocked_by' THEN
				RAISE EXCEPTION 'injected inverse failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER fail_blocked_by BEFORE INSERT ON issue_relations
			FOR EACH ROW EXECUTE FUNCTION fail_blocked_by();`)
	require.NoError(t, err)

	rec := env.do(http.MethodPost, owner.path("/issues/"+first+"/relations"), owner.Token,
		map[string]any{"related_identifier": second, "type": "blocking"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Zero(t, env.count(`SELECT count(*) FROM issue_relations`), "the blocking row must roll back with its inverse")

	// Symmetric relations have no inverse row and are unaffected.
	env.mustJSON(http.MethodPost, owner.path("/issues/"+first+"/relations"), owner.Token,
		map[string]any{"related_identifier": second, "type": "related"}, http.StatusCreated)
	assert.Equal(t, 1, env.count(`SELECT count(*) FROM issue_relations`))

	// With the database healthy, both directions are stored and listed.
	_, err = env.db.Exec(`DROP TRIGGER fail_blocked_by ON issue_relations`)
	require.NoError(t, err)
	env.mustJSON(http.MethodPost, owner.path("/issues/"+first+"/relations"), owner.Token,
		map[string]any{"related_identifier": second, "type": "blocking"}, http.StatusCreated)
	assert.Equal(t, 1, env.count(`SELECT count(*) FROM issue_relations WHERE type='blocking'`))
	assert.Equal(t, 1, env.count(`SELECT count(*) FROM issue_relations WHERE type='blocked_by'`))

	// Input errors stay client errors.
	rec = env.do(http.MethodPost, owner.path("/issues/"+first+"/relations"), owner.Token,
		map[string]any{"related_identifier": first, "type": "related"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "cannot relate an issue to itself")
}

func TestTeamStatusWrites_StatusAndVisibilityAreAtomic(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("statusvis")
	teamID := env.createTeam(owner, "Vis", "VIS")
	open, doomed := uuid.New(), uuid.New()
	for id, name := range map[uuid.UUID]string{open: "Open", doomed: "Doomed"} {
		_, err := env.db.Exec(`INSERT INTO projects (id,workspace_id,name) VALUES ($1,$2,$3)`, id, owner.WorkspaceID, name)
		require.NoError(t, err)
	}
	// Visibility rows for the "Doomed" project fail inside the database.
	_, err := env.db.Exec(`
		CREATE FUNCTION fail_doomed_visibility() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id AND name = 'Doomed') THEN
				RAISE EXCEPTION 'injected visibility failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER fail_doomed_visibility BEFORE INSERT ON project_status_visibility
			FOR EACH ROW EXECUTE FUNCTION fail_doomed_visibility();`)
	require.NoError(t, err)
	statuses := owner.path("/teams/" + teamID + "/statuses")
	before := env.count(`SELECT count(*) FROM team_statuses`)

	// Create: a failure on the second project leaves no status and no
	// visibility for the first.
	rec := env.do(http.MethodPost, statuses, owner.Token, map[string]any{
		"name": "QA", "category": "started", "project_ids": []string{open.String(), doomed.String()}})
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "injected")
	assert.Equal(t, before, env.count(`SELECT count(*) FROM team_statuses`), "the status must roll back")
	assert.Zero(t, env.count(`SELECT count(*) FROM project_status_visibility`))

	// Update: a failure while adding visibility keeps the old name and the
	// old visibility.
	status := env.mustJSON(http.MethodPost, statuses, owner.Token, map[string]any{
		"name": "Review", "category": "started", "project_ids": []string{open.String()}}, http.StatusCreated)
	statusID := status["id"].(string)
	rec = env.do(http.MethodPatch, statuses+"/"+statusID, owner.Token, map[string]any{
		"name": "Renamed", "project_ids": []string{doomed.String()}})
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	var name string
	require.NoError(t, env.db.Get(&name, `SELECT name FROM team_statuses WHERE id=$1`, statusID))
	assert.Equal(t, "Review", name, "the status edit must roll back")
	assert.Equal(t, 1, env.count(`SELECT count(*) FROM project_status_visibility WHERE status_id=$1 AND project_id=$2`, statusID, open),
		"the previous visibility must survive")

	// Healthy writes replace the visibility set.
	_, err = env.db.Exec(`DROP TRIGGER fail_doomed_visibility ON project_status_visibility`)
	require.NoError(t, err)
	env.mustJSON(http.MethodPatch, statuses+"/"+statusID, owner.Token, map[string]any{
		"name": "Renamed", "project_ids": []string{doomed.String()}}, http.StatusOK)
	assert.Equal(t, 0, env.count(`SELECT count(*) FROM project_status_visibility WHERE status_id=$1 AND project_id=$2`, statusID, open))
	assert.Equal(t, 1, env.count(`SELECT count(*) FROM project_status_visibility WHERE status_id=$1 AND project_id=$2`, statusID, doomed))

	// Unknown projects are still a scoped 404, not a server error.
	rec = env.do(http.MethodPatch, statuses+"/"+statusID, owner.Token, map[string]any{"project_ids": []string{uuid.NewString()}})
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
