package mcpserver

import (
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubItem struct {
	id    string
	names []string
}

func TestMatchOneFriendlyResolution(t *testing.T) {
	aID, bID := uuid.NewString(), uuid.NewString()
	items := []stubItem{
		{id: aID, names: []string{"Bug", "bug"}},
		{id: bID, names: []string{"Feature"}},
	}
	at := func(i int) (stubItem, string) { return items[i], items[i].id }
	namesOf := func(s stubItem) []string { return s.names }

	// exact UUID
	got, err := matchOne(len(items), aID, at, namesOf, "label")
	require.NoError(t, err)
	assert.Equal(t, aID, got.id)

	// case-insensitive name (unique)
	got, err = matchOne(len(items), "bug", at, namesOf, "label")
	require.NoError(t, err)
	assert.Equal(t, aID, got.id)

	// miss reports available options
	_, err = matchOne(len(items), "nope", at, namesOf, "label")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "available")
	assert.Contains(t, err.Error(), "Bug")
	assert.Contains(t, err.Error(), "Feature")

	// empty ref
	_, err = matchOne(len(items), "", at, namesOf, "label")
	require.Error(t, err)

	// ambiguity
	dup := []stubItem{
		{id: uuid.NewString(), names: []string{"Same"}},
		{id: uuid.NewString(), names: []string{"same"}},
	}
	_, err = matchOne(len(dup), "Same", func(i int) (stubItem, string) { return dup[i], dup[i].id },
		func(s stubItem) []string { return s.names }, "label")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
}

func TestSlugify(t *testing.T) {
	assert.Equal(t, "my-workspace", slugify("My Workspace"))
	assert.Equal(t, "eng-ops", slugify("  ENG & OPS  "))
	assert.Empty(t, slugify("!!!"))
}

func TestTeamOutCarriesKey(t *testing.T) {
	team := domain.Team{ID: uuid.New(), Key: "ENG", Name: "Engineering"}
	out := teamOut{ID: team.ID.String(), Key: team.Key, Name: team.Name}
	assert.Equal(t, "ENG", out.Key)
}
