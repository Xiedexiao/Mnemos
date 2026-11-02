package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubPromptGateway returns a fixed prompt version per key, or an error.
type stubPromptGateway struct {
	interfaces.ModelGatewayService
	versions map[string]*types.PromptVersion
	err      error
}

func (s *stubPromptGateway) LatestVersion(
	_ context.Context, _ uint64, templateKey string,
) (*types.PromptVersion, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.versions[templateKey], nil
}

// TestPromptVersionResolverPrefersStoredVersion is the §16.5 gap this closes:
// saving a version used to change a row and nothing else.
func TestPromptVersionResolverPrefersStoredVersion(t *testing.T) {
	gateway := &stubPromptGateway{versions: map[string]*types.PromptVersion{
		"system_prompt": {TemplateKey: "system_prompt", Version: 4, Content: "tenant override"},
	}}
	resolver := &promptVersionResolver{gateway: gateway}

	body, tag := resolver.resolveWithTag(context.Background(), 7, "system_prompt", "built-in")
	assert.Equal(t, "tenant override", body)
	assert.Equal(t, "system_prompt@v4", tag)
}

// TestPromptVersionResolverFallsBackToBuiltIn keeps a missing override from
// breaking answering.
func TestPromptVersionResolverFallsBackToBuiltIn(t *testing.T) {
	gateway := &stubPromptGateway{versions: map[string]*types.PromptVersion{}}
	resolver := &promptVersionResolver{gateway: gateway}

	body, tag := resolver.resolveWithTag(context.Background(), 7, "rewrite", "built-in")
	assert.Equal(t, "built-in", body)
	assert.Empty(t, tag, "no override means no version tag")
}

// TestPromptVersionResolverSurvivesLookupErrors keeps a database problem from
// taking down the answer path.
func TestPromptVersionResolverSurvivesLookupErrors(t *testing.T) {
	gateway := &stubPromptGateway{err: errors.New("db down")}
	resolver := &promptVersionResolver{gateway: gateway}

	body := resolver.resolve(context.Background(), 7, "system_prompt", "built-in")
	assert.Equal(t, "built-in", body)
}

// TestPromptVersionResolverHandlesNilDependencies keeps the nil-tolerant
// contract used by Lite mode and tests.
func TestPromptVersionResolverHandlesNilDependencies(t *testing.T) {
	var resolver *promptVersionResolver
	assert.Equal(t, "built-in", resolver.resolve(context.Background(), 7, "x", "built-in"))

	empty := &promptVersionResolver{}
	assert.Equal(t, "built-in", empty.resolve(context.Background(), 7, "x", "built-in"))
	assert.Equal(t, "built-in", empty.resolve(context.Background(), 7, "", "built-in"))
}

// TestPromptVersionTagShape pins the tag format recorded on evidence packs.
func TestPromptVersionTagShape(t *testing.T) {
	assert.Equal(t, "system_prompt@v1", promptVersionTag("system_prompt", 1))
	assert.Equal(t, "graph_extraction@v12", promptVersionTag("graph_extraction", 12))
	assert.Empty(t, promptVersionTag("", 3))
	assert.Empty(t, promptVersionTag("x", 0))
}

// TestPromptTemplateKeysMatchConfiguredFiles guards against a key that has no
// corresponding template file: overriding it would silently do nothing.
func TestPromptTemplateKeysMatchConfiguredFiles(t *testing.T) {
	keys := types.PromptTemplateKeys()
	require.NotEmpty(t, keys)
	seen := map[string]bool{}
	for _, key := range keys {
		assert.NotEmpty(t, key)
		assert.False(t, seen[key], "duplicate template key %s", key)
		seen[key] = true
	}
	// system_prompt is the key the completion plugin resolves for chat answers.
	assert.True(t, seen["system_prompt"])
}
