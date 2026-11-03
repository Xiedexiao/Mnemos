package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

// TestExplicitFalseSurvivesCreate guards a GORM trap that hit four boolean
// columns with `default:true`: GORM omits zero-valued fields that carry a
// default tag, so an explicit `false` is replaced by the database default
// `true` on INSERT. For FolderACL.InheritChildren that is a permission
// widening bug, not a cosmetic one.
func TestExplicitFalseSurvivesCreate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.FolderACL{},
		&types.ExternalSubscriptionFeed{},
		&types.EmbedPushRule{},
		&types.ModelRoutingRule{},
	))
	// The model tags do not declare the composite uniqueness that the real
	// migration creates; UpsertGrant's ON CONFLICT target needs it.
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_folder_acl
		ON folder_acl (knowledge_base_id, folder_path, subject_type, subject_id, action)`).Error)
	ctx := context.Background()

	// Folder ACL: an explicit non-inheriting grant must not cover descendants.
	folderRepo := NewFolderACLRepository(db)
	require.NoError(t, folderRepo.UpsertGrant(ctx, &types.FolderACL{
		TenantID: 7, KnowledgeBaseID: "kb-1", FolderPath: "supplier",
		SubjectType: "user", SubjectID: "u-1", Action: "view", Effect: "deny",
		InheritChildren: false,
	}))
	grants, err := folderRepo.ListByKnowledgeBase(ctx, 7, "kb-1")
	require.NoError(t, err)
	require.Len(t, grants, 1)
	assert.False(t, grants[0].InheritChildren,
		"a non-inheriting folder deny must not silently inherit to descendants")

	// External subscription: a feed subscribed in the disabled state stays off.
	feedRepo := NewExternalSubscriptionRepository(db)
	feed := &types.ExternalSubscriptionFeed{
		TenantID: 7, Name: "国标", FeedURL: "https://example.com/feed.xml",
		Enabled: false, CheckIntervalHours: 24,
	}
	require.NoError(t, feedRepo.CreateFeed(ctx, feed))
	storedFeed, err := feedRepo.GetFeed(ctx, 7, feed.ID)
	require.NoError(t, err)
	assert.False(t, storedFeed.Enabled)

	// Embedded push rule: created disabled, stays disabled.
	pushRepo := NewEmbedPushRepository(db)
	rule := &types.EmbedPushRule{
		TenantID: 7, Name: "E45 SOP", Enabled: false,
		KnowledgeIDs: types.JSON(`["k-1"]`),
	}
	require.NoError(t, pushRepo.CreateRule(ctx, rule))
	storedRule, err := pushRepo.GetRule(ctx, 7, rule.ID)
	require.NoError(t, err)
	assert.False(t, storedRule.Enabled)

	// Model routing: a paused route must stay paused.
	gatewayRepo := NewModelGatewayRepository(db)
	routing := &types.ModelRoutingRule{
		TenantID: 7, Scope: "task", ScopeValue: "chat", ModelID: "model-1",
		Enabled: false, CreatedBy: "admin",
	}
	require.NoError(t, gatewayRepo.CreateRule(ctx, routing))
	storedRouting, err := gatewayRepo.GetRule(ctx, 7, routing.ID)
	require.NoError(t, err)
	assert.False(t, storedRouting.Enabled)
}
