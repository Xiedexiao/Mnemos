package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/Tencent/WeKnora/internal/types"
)

// TestShareLinkStillPermitted covers the §2.5 rule that a live link never
// outlives the source document's own ceilings: raising classification,
// turning a secret document into a download link, or taking the document out
// of service must all invalidate the link at resolve time.
func TestShareLinkStillPermitted(t *testing.T) {
	past := time.Now().Add(-24 * time.Hour)
	future := time.Now().Add(24 * time.Hour)

	cases := []struct {
		name        string
		knowledge   *types.Knowledge
		link        *types.ShareLink
		wantAllowed bool
	}{
		{
			name: "internal view link stays valid",
			knowledge: &types.Knowledge{
				ID: "k1", Classification: string(types.KnowledgeClassificationInternal),
				LifecycleStatus: string(types.KnowledgeLifecycleValid),
			},
			link:        &types.ShareLink{KnowledgeID: "k1", Permission: string(types.ShareLinkPermissionView)},
			wantAllowed: true,
		},
		{
			name: "document raised to confidential invalidates the link",
			knowledge: &types.Knowledge{
				ID: "k1", Classification: string(types.KnowledgeClassificationConfidential),
				LifecycleStatus: string(types.KnowledgeLifecycleValid),
			},
			link:        &types.ShareLink{KnowledgeID: "k1", Permission: string(types.ShareLinkPermissionView)},
			wantAllowed: false,
		},
		{
			name: "document raised to secret keeps a view link",
			knowledge: &types.Knowledge{
				ID: "k1", Classification: string(types.KnowledgeClassificationSecret),
				LifecycleStatus: string(types.KnowledgeLifecycleValid),
			},
			link:        &types.ShareLink{KnowledgeID: "k1", Permission: string(types.ShareLinkPermissionView)},
			wantAllowed: true,
		},
		{
			name: "document raised to secret kills a download link",
			knowledge: &types.Knowledge{
				ID: "k1", Classification: string(types.KnowledgeClassificationSecret),
				LifecycleStatus: string(types.KnowledgeLifecycleValid),
			},
			link:        &types.ShareLink{KnowledgeID: "k1", Permission: string(types.ShareLinkPermissionDownload)},
			wantAllowed: false,
		},
		{
			name: "delisted document invalidates the link",
			knowledge: &types.Knowledge{
				ID: "k1", Classification: string(types.KnowledgeClassificationInternal),
				LifecycleStatus: string(types.KnowledgeLifecycleDelisted),
			},
			link:        &types.ShareLink{KnowledgeID: "k1", Permission: string(types.ShareLinkPermissionView)},
			wantAllowed: false,
		},
		{
			name: "expired document invalidates the link",
			knowledge: &types.Knowledge{
				ID: "k1", Classification: string(types.KnowledgeClassificationInternal),
				LifecycleStatus: string(types.KnowledgeLifecycleValid), ExpiresAt: &past,
			},
			link:        &types.ShareLink{KnowledgeID: "k1", Permission: string(types.ShareLinkPermissionView)},
			wantAllowed: false,
		},
		{
			name: "not-yet-effective document invalidates the link",
			knowledge: &types.Knowledge{
				ID: "k1", Classification: string(types.KnowledgeClassificationInternal),
				LifecycleStatus: string(types.KnowledgeLifecyclePendingEffective), EffectiveAt: &future,
			},
			link:        &types.ShareLink{KnowledgeID: "k1", Permission: string(types.ShareLinkPermissionView)},
			wantAllowed: false,
		},
		{
			name: "archived document invalidates the link",
			knowledge: &types.Knowledge{
				ID: "k1", Classification: string(types.KnowledgeClassificationInternal),
				LifecycleStatus: string(types.KnowledgeLifecycleArchived),
			},
			link:        &types.ShareLink{KnowledgeID: "k1", Permission: string(types.ShareLinkPermissionView)},
			wantAllowed: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantAllowed, shareLinkStillPermitted(tc.knowledge, tc.link))
		})
	}
}
