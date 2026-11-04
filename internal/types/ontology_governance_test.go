package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOntologyGovernanceLevelParsing covers the fail-closed default.
func TestOntologyGovernanceLevelParsing(t *testing.T) {
	assert.Equal(t, OntologyGovernanceL2, ParseOntologyGovernanceLevel("l2"))
	assert.Equal(t, OntologyGovernanceL2, ParseOntologyGovernanceLevel(" L2 "))
	assert.Equal(t, OntologyGovernanceL0, ParseOntologyGovernanceLevel(""),
		"an empty level must not be trusted")
	assert.Equal(t, OntologyGovernanceL0, ParseOntologyGovernanceLevel("L9"),
		"an unknown level falls back to the least trusted one")
}

// TestOntologyGovernanceRank covers the ladder.
func TestOntologyGovernanceRank(t *testing.T) {
	assert.Equal(t, 0, OntologyGovernanceL0.Rank())
	assert.Equal(t, 4, OntologyGovernanceL4.Rank())
	for index, level := range []OntologyGovernanceLevel{
		OntologyGovernanceL0, OntologyGovernanceL1, OntologyGovernanceL2,
		OntologyGovernanceL3, OntologyGovernanceL4,
	} {
		assert.Equal(t, index, level.Rank())
		assert.True(t, level.Valid())
	}
	assert.False(t, OntologyGovernanceLevel("L5").Valid())
}

// TestOntologyGovernanceRequirements covers the three mandatory actions.
func TestOntologyGovernanceRequirements(t *testing.T) {
	assert.False(t, OntologyGovernanceL1.RequiresDualApproval())
	assert.True(t, OntologyGovernanceL2.RequiresDualApproval())
	assert.True(t, OntologyGovernanceL4.RequiresDualApproval())

	assert.False(t, OntologyGovernanceL2.RequiresMigrationRehearsal())
	assert.True(t, OntologyGovernanceL3.RequiresMigrationRehearsal())
	assert.True(t, OntologyGovernanceL4.RequiresMigrationRehearsal())

	assert.False(t, OntologyGovernanceL3.RequiresGoldenRegression())
	assert.True(t, OntologyGovernanceL4.RequiresGoldenRegression())
}

// TestOntologyDomainFloor is the §8.2 rule: strong-governance domains cannot be
// declared below their floor.
func TestOntologyDomainFloor(t *testing.T) {
	assert.Equal(t, OntologyGovernanceL0, MinOntologyGovernanceLevelForDomain("general"))
	assert.Equal(t, OntologyGovernanceL2, MinOntologyGovernanceLevelForDomain("price"))
	assert.Equal(t, OntologyGovernanceL2, MinOntologyGovernanceLevelForDomain("Contract"))
	assert.Equal(t, OntologyGovernanceL3, MinOntologyGovernanceLevelForDomain("drawing"))
	assert.Equal(t, OntologyGovernanceL3, MinOntologyGovernanceLevelForDomain("HR"))
}

// TestEffectiveOntologyGovernanceLevelOnlyRaises is the "只能抬高" rule.
func TestEffectiveOntologyGovernanceLevelOnlyRaises(t *testing.T) {
	// Asking for L0 on a price ontology yields L2.
	assert.Equal(t, OntologyGovernanceL2,
		EffectiveOntologyGovernanceLevel(OntologyGovernanceL0, "price"))
	// Asking for L4 on a drawing ontology keeps L4.
	assert.Equal(t, OntologyGovernanceL4,
		EffectiveOntologyGovernanceLevel(OntologyGovernanceL4, "drawing"))
	// A general domain keeps whatever was asked.
	assert.Equal(t, OntologyGovernanceL1,
		EffectiveOntologyGovernanceLevel(OntologyGovernanceL1, "general"))
}

// TestOntologyGovernanceLevelsAreOrdered pins the ladder order itself.
func TestOntologyGovernanceLevelsAreOrdered(t *testing.T) {
	require.Len(t, allOntologyGovernanceLevels, 5)
	for index := 1; index < len(allOntologyGovernanceLevels); index++ {
		assert.Less(t,
			allOntologyGovernanceLevels[index-1].Rank(),
			allOntologyGovernanceLevels[index].Rank())
	}
}
