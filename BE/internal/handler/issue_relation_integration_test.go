package handler

import (
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIssueRelationCreate_DuplicateIsConflict(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("reldup")
	teamID := env.createTeam(owner, "Rel", "REL")
	first := env.createIssue(owner, teamID, "first")
	second := env.createIssue(owner, teamID, "second")

	env.mustJSON(http.MethodPost, owner.path("/issues/"+first+"/relations"), owner.Token,
		map[string]any{"related_identifier": second, "type": "blocking"}, http.StatusCreated)

	// The same relation again, and the same pair stated from the other side.
	for _, attempt := range []struct{ from, to, kind string }{{first, second, "blocking"}, {second, first, "blocked_by"}} {
		rec := env.do(http.MethodPost, owner.path("/issues/"+attempt.from+"/relations"), owner.Token,
			map[string]any{"related_identifier": attempt.to, "type": attempt.kind})
		assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "This relation already exists")
	}
	assert.Equal(t, 2, env.count(`SELECT count(*) FROM issue_relations`))
}

func TestIssueRelationCreate_LookupFailureIsServerError(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("rellookup")
	teamID := env.createTeam(owner, "Rel", "REL")
	first := env.createIssue(owner, teamID, "first")
	second := env.createIssue(owner, teamID, "second")

	// Make the identifier lookup fail inside the database.
	_, err := env.db.Exec(`ALTER TABLE issue_identifier_history RENAME TO issue_identifier_history_away`)
	require.NoError(t, err)
	rec := env.do(http.MethodPost, owner.path("/issues/"+first+"/relations"), owner.Token,
		map[string]any{"related_identifier": second, "type": "related"})
	_, err = env.db.Exec(`ALTER TABLE issue_identifier_history_away RENAME TO issue_identifier_history`)
	require.NoError(t, err)

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "issue_identifier_history", "storage errors must not leak")
	assert.NotContains(t, rec.Body.String(), "not found", "a storage failure is not a missing issue")

	// Missing issues are still caller errors.
	rec = env.do(http.MethodPost, owner.path("/issues/"+first+"/relations"), owner.Token,
		map[string]any{"related_identifier": "REL-999", "type": "related"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "related issue not found")
}

// Deleting the two directions of a blocking pair at the same time must not
// deadlock: one delete removes the pair, the other finds nothing.
func TestIssueRelationDelete_OppositeRowsConcurrently(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("reldel")
	teamID := env.createTeam(owner, "Rel", "REL")
	first := env.createIssue(owner, teamID, "first")
	second := env.createIssue(owner, teamID, "second")

	for range 10 {
		env.mustJSON(http.MethodPost, owner.path("/issues/"+first+"/relations"), owner.Token,
			map[string]any{"related_identifier": second, "type": "blocking"}, http.StatusCreated)
		var blocking, blockedBy string
		require.NoError(t, env.db.Get(&blocking, `SELECT id FROM issue_relations WHERE type='blocking'`))
		require.NoError(t, env.db.Get(&blockedBy, `SELECT id FROM issue_relations WHERE type='blocked_by'`))

		codes := make([]int, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, target := range []struct{ issue, id string }{{first, blocking}, {second, blockedBy}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i] = env.do(http.MethodDelete, owner.path("/issues/"+target.issue+"/relations/"+target.id), owner.Token, nil).Code
			}()
		}
		close(start)
		wg.Wait()

		assert.ElementsMatch(t, []int{http.StatusOK, http.StatusNotFound}, codes, "exactly one delete wins; none may fail")
		require.Zero(t, env.count(`SELECT count(*) FROM issue_relations`), "the whole pair is removed")
	}
}

// "A blocking B" and "B blocked_by A" describe the same pair. Created at the
// same time, one must win and the other get a 409, never a deadlock (500).
func TestIssueRelationCreate_OppositeDirectionsConcurrently(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("relrace")
	teamID := env.createTeam(owner, "Rel", "REL")
	first := env.createIssue(owner, teamID, "first")
	second := env.createIssue(owner, teamID, "second")

	for range 10 {
		codes := make([]int, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, req := range []struct{ from, to, kind string }{{first, second, "blocking"}, {second, first, "blocked_by"}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i] = env.do(http.MethodPost, owner.path("/issues/"+req.from+"/relations"), owner.Token,
					map[string]any{"related_identifier": req.to, "type": req.kind}).Code
			}()
		}
		close(start)
		wg.Wait()

		assert.ElementsMatch(t, []int{http.StatusCreated, http.StatusConflict}, codes)
		require.Equal(t, 2, env.count(`SELECT count(*) FROM issue_relations`), "exactly one pair is stored")
		_, err := env.db.Exec(`DELETE FROM issue_relations`)
		require.NoError(t, err)
	}
}
