package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

// rewardStarTestDDL is the minimal schema for redemption and knowledge-star
// tests: the reward tables plus the shared points ledger/balance.
const rewardStarTestDDL = `
CREATE TABLE reward_catalog_items (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    name VARCHAR(128) NOT NULL,
    description VARCHAR(1024) NOT NULL DEFAULT '',
    cost_points INTEGER NOT NULL,
    stock INTEGER NOT NULL DEFAULT -1,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at DATETIME,
    updated_at DATETIME
);
CREATE INDEX idx_reward_items_tenant ON reward_catalog_items (tenant_id, enabled, cost_points);

CREATE TABLE reward_redemptions (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    item_id VARCHAR(36) NOT NULL,
    item_name VARCHAR(128) NOT NULL,
    points_cost INTEGER NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    note VARCHAR(512) NOT NULL DEFAULT '',
    handled_by VARCHAR(36) NOT NULL DEFAULT '',
    handled_at DATETIME,
    created_at DATETIME,
    updated_at DATETIME
);

CREATE TABLE user_point_balance (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    balance INTEGER NOT NULL DEFAULT 0,
    level VARCHAR(16) DEFAULT 'bronze',
    updated_at DATETIME
);

CREATE TABLE user_points_ledger (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    action VARCHAR(32) NOT NULL,
    points INTEGER NOT NULL,
    source_type VARCHAR(24) DEFAULT '',
    source_id VARCHAR(36) DEFAULT '',
    created_at DATETIME
);

CREATE TABLE knowledge_star_awards (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    period_type VARCHAR(16) NOT NULL,
    period VARCHAR(16) NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    rank INTEGER NOT NULL DEFAULT 0,
    score INTEGER NOT NULL DEFAULT 0,
    reason VARCHAR(512) NOT NULL DEFAULT '',
    published BOOLEAN NOT NULL DEFAULT FALSE,
    awarded_by VARCHAR(36) NOT NULL DEFAULT '',
    awarded_at DATETIME,
    created_at DATETIME,
    updated_at DATETIME
);
CREATE UNIQUE INDEX uq_knowledge_star_period_user
    ON knowledge_star_awards (tenant_id, period_type, period, user_id);
`

func setupRewardStarDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(rewardStarTestDDL).Error)
	return db
}

func seedBalance(t *testing.T, db *gorm.DB, tenantID uint64, userID string, balance int) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO user_point_balance (id, tenant_id, user_id, balance, level)
		VALUES (?, ?, ?, ?, ?)`,
		"bal-"+userID, tenantID, userID, balance, string(types.PointLevelForBalance(balance))).Error)
}

func currentBalanceOf(t *testing.T, db *gorm.DB, tenantID uint64, userID string) int {
	t.Helper()
	var balance int
	require.NoError(t, db.Raw(
		`SELECT balance FROM user_point_balance WHERE tenant_id = ? AND user_id = ?`,
		tenantID, userID).Scan(&balance).Error)
	return balance
}

func countRows(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Table(table).Count(&count).Error)
	return count
}

// TestRedeemDebitsBalanceAndStock covers the happy path: one transaction moves
// the points, the stock and the order together.
func TestRedeemDebitsBalanceAndStock(t *testing.T) {
	db := setupRewardStarDB(t)
	ctx := context.Background()
	seedBalance(t, db, 7, "u1", 100)

	repo := repository.NewRewardRepository(db)
	svc := &rewardService{repo: repo, now: time.Now}
	item, err := svc.CreateItem(ctx, 7, "admin", types.RewardCatalogItemInput{
		Name: "定制水杯", CostPoints: 60, Stock: func() *int { v := 2; return &v }(),
	})
	require.NoError(t, err)

	redemption, err := svc.Redeem(ctx, 7, "u1", item.ID, "要蓝色")
	require.NoError(t, err)
	assert.Equal(t, string(types.RewardRedemptionPending), redemption.Status)
	assert.Equal(t, "定制水杯", redemption.ItemName)
	assert.Equal(t, 60, redemption.PointsCost)

	assert.Equal(t, 40, currentBalanceOf(t, db, 7, "u1"))
	var level string
	require.NoError(t, db.Raw(
		`SELECT level FROM user_point_balance WHERE tenant_id = 7 AND user_id = 'u1'`).Scan(&level).Error)
	assert.Equal(t, string(types.PointLevelBronze), level, "spending drops the tier")

	var stock int
	require.NoError(t, db.Raw(`SELECT stock FROM reward_catalog_items WHERE id = ?`, item.ID).Scan(&stock).Error)
	assert.Equal(t, 1, stock)

	var ledger struct {
		Action string
		Points int
	}
	require.NoError(t, db.Raw(
		`SELECT action, points FROM user_points_ledger WHERE source_id = ?`, redemption.ID).Scan(&ledger).Error)
	assert.Equal(t, string(types.PointsActionRedeemed), ledger.Action)
	assert.Equal(t, -60, ledger.Points)
}

// TestRedeemInsufficientPointsChangesNothing is the fail-closed property: a
// rejected redemption must not leave a stock reservation or a ledger row.
func TestRedeemInsufficientPointsChangesNothing(t *testing.T) {
	db := setupRewardStarDB(t)
	ctx := context.Background()
	seedBalance(t, db, 7, "u1", 30)

	svc := &rewardService{repo: repository.NewRewardRepository(db), now: time.Now}
	item, err := svc.CreateItem(ctx, 7, "admin", types.RewardCatalogItemInput{
		Name: "蓝牙耳机", CostPoints: 60,
	})
	require.NoError(t, err)

	_, err = svc.Redeem(ctx, 7, "u1", item.ID, "")
	require.Error(t, err)
	assert.Equal(t, 30, currentBalanceOf(t, db, 7, "u1"))
	assert.EqualValues(t, 1, countRows(t, db, "reward_catalog_items"))
	assert.Zero(t, countRows(t, db, "reward_redemptions"))
	assert.Zero(t, countRows(t, db, "user_points_ledger"))
	assert.Equal(t, -1, currentStock(t, db, item.ID), "unlimited stock is untouched")
}

// TestRedeemOutOfStockChangesNothing covers the other precondition.
func TestRedeemOutOfStockChangesNothing(t *testing.T) {
	db := setupRewardStarDB(t)
	ctx := context.Background()
	seedBalance(t, db, 7, "u1", 1000)

	svc := &rewardService{repo: repository.NewRewardRepository(db), now: time.Now}
	item, err := svc.CreateItem(ctx, 7, "admin", types.RewardCatalogItemInput{
		Name: "限量徽章", CostPoints: 50, Stock: func() *int { v := 0; return &v }(),
	})
	require.NoError(t, err)

	_, err = svc.Redeem(ctx, 7, "u1", item.ID, "")
	require.Error(t, err)
	assert.Equal(t, 1000, currentBalanceOf(t, db, 7, "u1"), "points must not be debited")
	assert.Zero(t, countRows(t, db, "reward_redemptions"))
}

// TestCancelRefundsAndRestocks covers the user-cancel path.
func TestCancelRefundsAndRestocks(t *testing.T) {
	db := setupRewardStarDB(t)
	ctx := context.Background()
	seedBalance(t, db, 7, "u1", 100)
	svc := &rewardService{repo: repository.NewRewardRepository(db), now: time.Now}
	item, err := svc.CreateItem(ctx, 7, "admin", types.RewardCatalogItemInput{
		Name: "定制水杯", CostPoints: 60, Stock: func() *int { v := 1; return &v }(),
	})
	require.NoError(t, err)
	redemption, err := svc.Redeem(ctx, 7, "u1", item.ID, "")
	require.NoError(t, err)

	canceled, err := svc.Cancel(ctx, 7, "u1", redemption.ID)
	require.NoError(t, err)
	assert.Equal(t, string(types.RewardRedemptionCanceled), canceled.Status)
	assert.Equal(t, 100, currentBalanceOf(t, db, 7, "u1"), "points are refunded")
	assert.Equal(t, 1, currentStock(t, db, item.ID), "stock is restored")

	var refund struct {
		Action string
		Points int
	}
	require.NoError(t, db.Raw(
		`SELECT action, points FROM user_points_ledger WHERE action = ?`,
		string(types.PointsActionRefunded)).Scan(&refund).Error)
	assert.Equal(t, 60, refund.Points)
}

// TestCancelRejectsOtherUsersOrder keeps one user from canceling another's.
func TestCancelRejectsOtherUsersOrder(t *testing.T) {
	db := setupRewardStarDB(t)
	ctx := context.Background()
	seedBalance(t, db, 7, "u1", 100)
	svc := &rewardService{repo: repository.NewRewardRepository(db), now: time.Now}
	item, err := svc.CreateItem(ctx, 7, "admin", types.RewardCatalogItemInput{
		Name: "水杯", CostPoints: 10,
	})
	require.NoError(t, err)
	redemption, err := svc.Redeem(ctx, 7, "u1", item.ID, "")
	require.NoError(t, err)

	_, err = svc.Cancel(ctx, 7, "u2", redemption.ID)
	require.Error(t, err)
	assert.Equal(t, string(types.RewardRedemptionPending), currentRedemptionStatus(t, db, redemption.ID))
}

// TestResolveFulfilledKeepsPoints covers delivery: points stay spent.
func TestResolveFulfilledKeepsPoints(t *testing.T) {
	db := setupRewardStarDB(t)
	ctx := context.Background()
	seedBalance(t, db, 7, "u1", 100)
	svc := &rewardService{repo: repository.NewRewardRepository(db), now: time.Now}
	item, err := svc.CreateItem(ctx, 7, "admin", types.RewardCatalogItemInput{
		Name: "水杯", CostPoints: 60,
	})
	require.NoError(t, err)
	redemption, err := svc.Redeem(ctx, 7, "u1", item.ID, "")
	require.NoError(t, err)

	resolved, err := svc.Resolve(ctx, 7, "admin", redemption.ID, types.RewardRedemptionFulfilled)
	require.NoError(t, err)
	assert.Equal(t, string(types.RewardRedemptionFulfilled), resolved.Status)
	assert.Equal(t, 40, currentBalanceOf(t, db, 7, "u1"), "fulfilment does not refund")

	_, err = svc.Resolve(ctx, 7, "admin", redemption.ID, types.RewardRedemptionRejected)
	require.Error(t, err, "an already-resolved order cannot transition again")
}

// TestResolveRejectedRefunds covers the operator-rejection path.
func TestResolveRejectedRefunds(t *testing.T) {
	db := setupRewardStarDB(t)
	ctx := context.Background()
	seedBalance(t, db, 7, "u1", 100)
	svc := &rewardService{repo: repository.NewRewardRepository(db), now: time.Now}
	item, err := svc.CreateItem(ctx, 7, "admin", types.RewardCatalogItemInput{
		Name: "水杯", CostPoints: 60,
	})
	require.NoError(t, err)
	redemption, err := svc.Redeem(ctx, 7, "u1", item.ID, "")
	require.NoError(t, err)

	resolved, err := svc.Resolve(ctx, 7, "admin", redemption.ID, types.RewardRedemptionRejected)
	require.NoError(t, err)
	assert.Equal(t, string(types.RewardRedemptionRejected), resolved.Status)
	assert.Equal(t, 100, currentBalanceOf(t, db, 7, "u1"))
}

// TestRedeemRejectsDisabledItem keeps a retired gift out of circulation.
func TestRedeemRejectsDisabledItem(t *testing.T) {
	db := setupRewardStarDB(t)
	ctx := context.Background()
	seedBalance(t, db, 7, "u1", 1000)
	svc := &rewardService{repo: repository.NewRewardRepository(db), now: time.Now}
	item, err := svc.CreateItem(ctx, 7, "admin", types.RewardCatalogItemInput{
		Name: "旧版礼品", CostPoints: 10,
	})
	require.NoError(t, err)
	disabled := false
	_, err = svc.UpdateItem(ctx, 7, item.ID, "admin", types.RewardCatalogItemInput{Enabled: &disabled})
	require.NoError(t, err)

	_, err = svc.Redeem(ctx, 7, "u1", item.ID, "")
	require.Error(t, err)
	assert.Equal(t, 1000, currentBalanceOf(t, db, 7, "u1"))
}

// TestKnowledgeStarPeriodParsing covers the month/quarter boundaries.
func TestKnowledgeStarPeriodParsing(t *testing.T) {
	start, end, canonical, err := types.ParseKnowledgeStarPeriod(
		types.KnowledgeStarPeriodMonth, "2026-11")
	require.NoError(t, err)
	assert.Equal(t, "2026-11", canonical)
	assert.Equal(t, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), start)
	assert.Equal(t, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), end)

	start, end, canonical, err = types.ParseKnowledgeStarPeriod(
		types.KnowledgeStarPeriodQuarter, "2026-Q4")
	require.NoError(t, err)
	assert.Equal(t, "2026-Q4", canonical)
	assert.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), start)
	assert.Equal(t, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), end)

	_, _, _, err = types.ParseKnowledgeStarPeriod(types.KnowledgeStarPeriodMonth, "2026-13")
	require.Error(t, err)
	_, _, _, err = types.ParseKnowledgeStarPeriod(types.KnowledgeStarPeriodQuarter, "2026-Q5")
	require.Error(t, err)
	assert.Equal(t, "2026-11", types.KnowledgeStarPeriodOf(types.KnowledgeStarPeriodMonth,
		time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)))
	assert.Equal(t, "2026-Q4", types.KnowledgeStarPeriodOf(types.KnowledgeStarPeriodQuarter,
		time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)))
}

// TestStarCandidatesIgnoreRedemptionsAndOtherPeriods pins the scoring rule:
// contribution is positive points inside the period; spending gifts and last
// month's work do not count.
func TestStarCandidatesIgnoreRedemptionsAndOtherPeriods(t *testing.T) {
	db := setupRewardStarDB(t)
	ctx := context.Background()
	seedBalance(t, db, 7, "u1", 300)
	seedBalance(t, db, 7, "u2", 120)
	ledger := func(userID, action string, points int, createdAt time.Time) {
		require.NoError(t, db.Create(&types.UserPointsLedger{
			TenantID: 7, UserID: userID, Action: action, Points: points,
			SourceType: "test", SourceID: action + userID, CreatedAt: createdAt,
		}).Error)
	}
	nov := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	ledger("u1", string(types.PointsActionKnowledgeCreated), 10, nov)
	ledger("u1", string(types.PointsActionLikeGiven), 1, nov)
	ledger("u1", string(types.PointsActionRedeemed), -50, nov) // spending is not contribution
	ledger("u2", string(types.PointsActionReviewDone), 3, nov)
	ledger("u2", string(types.PointsActionAnswerAdopted), 5, nov)
	ledger("u2", string(types.PointsActionKnowledgeCreated), 99,
		time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) // previous period

	repo := repository.NewKnowledgeStarRepository(db)
	candidates, err := repo.ListCandidates(ctx, 7,
		time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), 50)
	require.NoError(t, err)
	require.Len(t, candidates, 2)
	assert.Equal(t, "u1", candidates[0].UserID)
	assert.Equal(t, 11, candidates[0].Score)
	assert.Equal(t, 1, candidates[0].KnowledgeCreated)
	assert.Zero(t, candidates[0].ReviewsDone)
	assert.Equal(t, string(types.PointLevelGold), candidates[0].Level)
	assert.Equal(t, "u2", candidates[1].UserID)
	assert.Equal(t, 8, candidates[1].Score)
	assert.Zero(t, candidates[1].KnowledgeCreated, "October's work belongs to October")
	assert.Equal(t, 1, candidates[1].ReviewsDone)
	assert.Equal(t, 1, candidates[1].AnswersAdopted)
}

// TestSelectReplaceAndPublish covers the selection lifecycle: rank by score,
// replacement on re-selection, and publication gating.
func TestSelectReplaceAndPublish(t *testing.T) {
	db := setupRewardStarDB(t)
	ctx := context.Background()
	seedBalance(t, db, 7, "u1", 300)
	seedBalance(t, db, 7, "u2", 120)
	seedBalance(t, db, 7, "u3", 0)
	nov := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		user   string
		points int
	}{{"u1", 30}, {"u2", 10}} {
		require.NoError(t, db.Create(&types.UserPointsLedger{
			TenantID: 7, UserID: row.user, Action: string(types.PointsActionKnowledgeCreated),
			Points: row.points, SourceType: "test", SourceID: "k-" + row.user, CreatedAt: nov,
		}).Error)
	}
	svc := &knowledgeStarService{
		repo: repository.NewKnowledgeStarRepository(db),
		now:  func() time.Time { return time.Date(2026, 11, 30, 12, 0, 0, 0, time.UTC) },
	}

	awards, err := svc.Select(ctx, 7, "admin", types.KnowledgeStarSelection{
		PeriodType: types.KnowledgeStarPeriodMonth,
		Period:     "2026-11",
		UserIDs:    []string{"u2", "u1"},
		Reason:     "本月贡献突出",
	})
	require.NoError(t, err)
	require.Len(t, awards, 2)
	assert.Equal(t, "u1", awards[0].UserID, "rank follows score, not submission order")
	assert.Equal(t, 1, awards[0].Rank)
	assert.Equal(t, "u2", awards[1].UserID)
	assert.Equal(t, 2, awards[1].Rank)

	public, err := svc.List(ctx, 7, "month", "2026-11", true, 50)
	require.NoError(t, err)
	assert.Empty(t, public, "an unpublished list is not public")

	published, err := svc.Publish(ctx, 7, "admin",
		types.KnowledgeStarPeriodMonth, "2026-11")
	require.NoError(t, err)
	assert.EqualValues(t, 2, published)
	public, err = svc.List(ctx, 7, "month", "2026-11", true, 50)
	require.NoError(t, err)
	require.Len(t, public, 2)

	// Re-selecting replaces the period's list rather than appending.
	replaced, err := svc.Select(ctx, 7, "admin", types.KnowledgeStarSelection{
		PeriodType: types.KnowledgeStarPeriodMonth,
		Period:     "2026-11",
		UserIDs:    []string{"u2"},
	})
	require.NoError(t, err)
	require.Len(t, replaced, 1)
	admin, err := svc.List(ctx, 7, "month", "2026-11", false, 50)
	require.NoError(t, err)
	require.Len(t, admin, 1)
	assert.Equal(t, "u2", admin[0].UserID)
}

// TestSelectRejectsZeroContribution keeps the award list from being filled by
// name rather than by contribution.
func TestSelectRejectsZeroContribution(t *testing.T) {
	db := setupRewardStarDB(t)
	ctx := context.Background()
	seedBalance(t, db, 7, "u3", 0)
	svc := &knowledgeStarService{
		repo: repository.NewKnowledgeStarRepository(db),
		now:  func() time.Time { return time.Date(2026, 11, 30, 12, 0, 0, 0, time.UTC) },
	}
	_, err := svc.Select(ctx, 7, "admin", types.KnowledgeStarSelection{
		PeriodType: types.KnowledgeStarPeriodMonth,
		Period:     "2026-11",
		UserIDs:    []string{"u3"},
	})
	require.Error(t, err)

	_, err = svc.Publish(ctx, 7, "admin", types.KnowledgeStarPeriodMonth, "2026-11")
	require.Error(t, err, "publishing an empty list must fail")
}

// currentStock reads an item's stock.
func currentStock(t *testing.T, db *gorm.DB, itemID string) int {
	t.Helper()
	var stock int
	require.NoError(t, db.Raw(
		`SELECT stock FROM reward_catalog_items WHERE id = ?`, itemID).Scan(&stock).Error)
	return stock
}

// currentRedemptionStatus reads a redemption's status.
func currentRedemptionStatus(t *testing.T, db *gorm.DB, redemptionID string) string {
	t.Helper()
	var status string
	require.NoError(t, db.Raw(
		`SELECT status FROM reward_redemptions WHERE id = ?`, redemptionID).Scan(&status).Error)
	return status
}
