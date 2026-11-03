<template>
  <div class="personal-center">
    <header class="personal-center__header">
      <div>
        <h2 class="personal-center__title">{{ t('personalCenter.title') }}</h2>
        <p class="personal-center__desc">{{ t('personalCenter.description') }}</p>
      </div>
      <t-button variant="outline" size="medium" :loading="loading" @click="load">
        <template #icon><t-icon name="refresh" /></template>
        {{ t('common.refresh') }}
      </t-button>
    </header>

    <div class="personal-center__metrics">
      <div class="personal-center__metric">
        <span class="personal-center__label">{{ t('personalCenter.favoriteCount') }}</span>
        <span class="personal-center__value">{{ center?.overview.favorite_count ?? 0 }}</span>
      </div>
      <div class="personal-center__metric">
        <span class="personal-center__label">{{ t('personalCenter.pendingCount') }}</span>
        <span class="personal-center__value">{{ center?.overview.pending_review_count ?? 0 }}</span>
      </div>
    </div>

    <t-tabs v-model="activeTab">
      <t-tab-panel value="reviews" :label="t('personalCenter.tabReviews')">
        <t-table
          row-key="task_id"
          :data="center?.reviews || []"
          :columns="reviewColumns"
          size="small"
          :loading="loading"
          :empty="t('personalCenter.emptyReviews')"
        >
          <template #status="{ row }">
            <t-tag size="small" variant="light" :theme="statusTheme(row.status)">
              {{ row.status }}
            </t-tag>
          </template>
        </t-table>
      </t-tab-panel>

      <t-tab-panel value="favorites" :label="t('personalCenter.tabFavorites')">
        <t-table
          row-key="resource_id"
          :data="center?.favorites || []"
          :columns="favoriteColumns"
          size="small"
          :loading="loading"
          :empty="t('personalCenter.emptyFavorites')"
        >
          <template #label="{ row }">
            <span :class="{ 'is-hidden': !row.visible }">
              {{ row.label || row.resource_id }}
            </span>
          </template>
        </t-table>
      </t-tab-panel>

      <t-tab-panel value="knowledge" :label="t('personalCenter.tabKnowledge')">
        <t-table
          row-key="id"
          :data="center?.knowledge || []"
          :columns="knowledgeColumns"
          size="small"
          :loading="loading"
          :empty="t('personalCenter.emptyKnowledge')"
        />
      </t-tab-panel>

      <t-tab-panel value="tasks" :label="t('personalCenter.tabTasks')">
        <t-table
          row-key="id"
          :data="learningTasks"
          :columns="taskColumns"
          size="small"
          :loading="loading"
          :empty="t('personalCenter.emptyTasks')"
        >
          <template #op="{ row }">
            <t-button v-if="row.status === 'open'" theme="primary" variant="text" size="small"
              :loading="busyId === row.id" @click="finishTask(row)">
              {{ t('personalCenter.markDone') }}
            </t-button>
            <span v-else>{{ row.status }}</span>
          </template>
        </t-table>
      </t-tab-panel>

      <t-tab-panel value="certificates" :label="t('personalCenter.tabCertificates')">
        <t-table
          row-key="id"
          :data="certificates"
          :columns="certificateColumns"
          size="small"
          :loading="loading"
          :empty="t('personalCenter.emptyCertificates')"
        >
          <template #status="{ row }">
            <t-tag size="small" variant="light"
              :theme="row.status === 'active' ? 'success' : 'default'">
              {{ row.status }}
            </t-tag>
          </template>
        </t-table>
      </t-tab-panel>

      <t-tab-panel value="rewards" :label="t('personalCenter.tabRewards')">
        <div class="personal-center__metrics">
          <div class="personal-center__metric">
            <span class="personal-center__label">{{ t('personalCenter.pointsBalance') }}</span>
            <span class="personal-center__value">{{ points?.balance ?? 0 }}</span>
          </div>
          <div class="personal-center__metric">
            <span class="personal-center__label">{{ t('personalCenter.pointsLevel') }}</span>
            <span class="personal-center__value">{{ levelLabel(points?.level || 'bronze') }}</span>
          </div>
        </div>

        <div class="personal-center__switch-row">
          <div>
            <span class="personal-center__label">{{ t('personalCenter.pushTitle') }}</span>
            <p class="personal-center__switch-desc">{{ t('personalCenter.pushDesc') }}</p>
          </div>
          <t-switch
            :value="pushEnabled"
            :loading="pushBusy"
            @change="(value: any) => togglePush(Boolean(value))"
          />
        </div>

        <h4 class="personal-center__subtitle">{{ t('personalCenter.pointsLedger') }}</h4>
        <t-table
          row-key="id"
          :data="points?.recent || []"
          :columns="ledgerColumns"
          size="small"
          :empty="t('personalCenter.emptyLedger')"
        >
          <template #action="{ row }">{{ actionLabel(row.action) }}</template>
          <template #points="{ row }">
            <span :class="{ 'is-negative': row.points < 0 }">
              {{ row.points > 0 ? `+${row.points}` : row.points }}
            </span>
          </template>
          <template #created_at="{ row }">{{ formatTime(row.created_at) }}</template>
        </t-table>

        <h4 class="personal-center__subtitle">{{ t('personalCenter.catalogTitle') }}</h4>
        <t-table
          row-key="id"
          :data="catalog"
          :columns="catalogColumns"
          size="small"
          :empty="t('personalCenter.emptyCatalog')"
        >
          <template #stock="{ row }">
            {{ row.stock < 0 ? t('personalCenter.unlimited') : row.stock }}
          </template>
          <template #op="{ row }">
            <t-button
              theme="primary"
              variant="text"
              size="small"
              :disabled="!row.enabled || row.stock === 0"
              :loading="busyId === row.id"
              @click="redeem(row)"
            >
              {{ t('personalCenter.redeem') }}
            </t-button>
          </template>
        </t-table>

        <h4 class="personal-center__subtitle">{{ t('personalCenter.myRedemptions') }}</h4>
        <t-table
          row-key="id"
          :data="redemptions"
          :columns="redemptionColumns"
          size="small"
          :empty="t('personalCenter.emptyRedemptions')"
        >
          <template #status="{ row }">
            <t-tag size="small" variant="light" :theme="redemptionTheme(row.status)">
              {{ t(`personalCenter.redemption_${row.status}`) }}
            </t-tag>
          </template>
          <template #op="{ row }">
            <t-button v-if="row.status === 'pending'" theme="danger" variant="text" size="small"
              :loading="busyId === row.id" @click="cancelRow(row)">
              {{ t('personalCenter.cancelRedemption') }}
            </t-button>
          </template>
        </t-table>

        <h4 class="personal-center__subtitle">{{ t('personalCenter.starsTitle') }}</h4>
        <t-table
          row-key="id"
          :data="stars"
          :columns="starColumns"
          size="small"
          :empty="t('personalCenter.emptyStars')"
        >
          <template #rank="{ row }">#{{ row.rank }}</template>
        </t-table>
      </t-tab-panel>
    </t-tabs>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { useI18n } from 'vue-i18n';
import { MessagePlugin } from 'tdesign-vue-next';
import {
  cancelRedemption,
  completeLearningTask,
  getEmbedPushPreference,
  getMyPoints,
  getPersonalCenter,
  listKnowledgeStars,
  listLearningCertificates,
  listLearningTasks,
  listMyRedemptions,
  listRewardCatalog,
  redeemReward,
  setEmbedPushPreference,
  type KnowledgeStarAward,
  type LearningCertificate,
  type LearningTask,
  type PersonalCenter,
  type PointsSummary,
  type RewardCatalogItem,
  type RewardRedemption,
} from '@/api/governance-console';

/**
 * 个人中心（KnowAxis V3.0 §15.3）。
 *
 * 平台此前没有"以我为中心"的视图：读者看不到自己收藏了什么、提交了哪些
 * 待审、拿到了什么认证。页面只读当前登录者——后端接口固定使用调用者身份，
 * 不接受 user_id 参数。
 */

const { t } = useI18n();

const center = ref<PersonalCenter | null>(null);
const certificates = ref<LearningCertificate[]>([]);
const learningTasks = ref<LearningTask[]>([]);
const busyId = ref<string | null>(null);
const loading = ref(false);
const activeTab = ref<
  'reviews' | 'favorites' | 'knowledge' | 'tasks' | 'certificates' | 'rewards'
>('reviews');

// ---- 积分与激励（§15.4） ----
const points = ref<PointsSummary | null>(null);
const catalog = ref<RewardCatalogItem[]>([]);
const redemptions = ref<RewardRedemption[]>([]);
const stars = ref<KnowledgeStarAward[]>([]);
const pushEnabled = ref(true);
const pushBusy = ref(false);

const statusTheme = (status: string): 'default' | 'success' | 'warning' | 'danger' => {
  switch (status) {
    case 'approved':
      return 'success';
    case 'rejected':
      return 'danger';
    case 'pending':
      return 'warning';
    default:
      return 'default';
  }
};

const reviewColumns = computed(() => [
  { colKey: 'title', title: t('personalCenter.colTitle'), ellipsis: true, minWidth: 220 },
  { colKey: 'status', title: t('personalCenter.colStatus'), width: 110 },
  { colKey: 'risk_level', title: t('personalCenter.colRisk'), width: 90 },
  { colKey: 'sla_due', title: t('personalCenter.colSla'), width: 170 },
]);

const favoriteColumns = computed(() => [
  { colKey: 'label', title: t('personalCenter.colTitle'), ellipsis: true, minWidth: 220 },
  { colKey: 'resource_type', title: t('personalCenter.colType'), width: 120 },
]);

const knowledgeColumns = computed(() => [
  { colKey: 'title', title: t('personalCenter.colTitle'), ellipsis: true, minWidth: 220 },
  { colKey: 'lifecycle_status', title: t('personalCenter.colStatus'), width: 130 },
  { colKey: 'updated_at', title: t('personalCenter.colUpdated'), width: 180 },
]);

const ledgerColumns = computed(() => [
  { colKey: 'action', title: t('personalCenter.colAction'), width: 160 },
  { colKey: 'points', title: t('personalCenter.colPoints'), width: 100 },
  { colKey: 'created_at', title: t('personalCenter.colUpdated'), width: 180 },
]);

const catalogColumns = computed(() => [
  { colKey: 'name', title: t('personalCenter.colTitle'), ellipsis: true, minWidth: 200 },
  { colKey: 'cost_points', title: t('personalCenter.colPoints'), width: 100 },
  { colKey: 'stock', title: t('personalCenter.colStock'), width: 110 },
  { colKey: 'op', title: t('personalCenter.colStatus'), width: 120 },
]);

const redemptionColumns = computed(() => [
  { colKey: 'item_name', title: t('personalCenter.colTitle'), ellipsis: true, minWidth: 180 },
  { colKey: 'points_cost', title: t('personalCenter.colPoints'), width: 100 },
  { colKey: 'status', title: t('personalCenter.colStatus'), width: 120 },
  { colKey: 'op', title: t('personalCenter.colAction'), width: 120 },
]);

const starColumns = computed(() => [
  { colKey: 'period', title: t('personalCenter.colPeriod'), width: 130 },
  { colKey: 'rank', title: t('personalCenter.colRank'), width: 90 },
  { colKey: 'user_id', title: t('personalCenter.colWinner'), ellipsis: true, minWidth: 160 },
  { colKey: 'score', title: t('personalCenter.colPoints'), width: 90 },
  { colKey: 'reason', title: t('personalCenter.colReason'), ellipsis: true, minWidth: 180 },
]);

const certificateColumns = computed(() => [
  { colKey: 'serial', title: t('personalCenter.colSerial'), width: 160 },
  { colKey: 'stage', title: t('personalCenter.colStage'), width: 110 },
  { colKey: 'status', title: t('personalCenter.colStatus'), width: 110 },
  { colKey: 'issued_at', title: t('personalCenter.colIssued'), width: 180 },
]);

const taskColumns = computed(() => [
  { colKey: 'title', title: t('personalCenter.colTitle'), ellipsis: true, minWidth: 220 },
  { colKey: 'source', title: t('personalCenter.colSource'), width: 130 },
  { colKey: 'due_at', title: t('personalCenter.colDue'), width: 150 },
  { colKey: 'op', title: t('personalCenter.colStatus'), width: 120 },
]);

// finishTask marks one study item done; the server enforces that only the
// assignee may complete it.
const finishTask = async (row: LearningTask) => {
  busyId.value = row.id;
  try {
    await completeLearningTask(row.id);
    const response = await listLearningTasks();
    learningTasks.value = response?.data || [];
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('personalCenter.loadFailed'));
  } finally {
    busyId.value = null;
  }
};

const load = async () => {
  loading.value = true;
  try {
    const [centerResponse, certificateResponse, taskResponse, pointsResponse,
      catalogResponse, redemptionResponse, starResponse, pushResponse] = await Promise.all([
      getPersonalCenter(),
      // 认证与学习任务是辅助信息：取不到时页面其余部分仍然可用。
      listLearningCertificates().catch(() => ({ data: [] as LearningCertificate[] })),
      listLearningTasks().catch(() => ({ data: [] as LearningTask[] })),
      getMyPoints().catch(() => null),
      listRewardCatalog().catch(() => ({ data: [] as RewardCatalogItem[] })),
      listMyRedemptions().catch(() => ({ data: [] as RewardRedemption[] })),
      listKnowledgeStars().catch(() => ({ data: [] as KnowledgeStarAward[] })),
      getEmbedPushPreference().catch(() => null),
    ]);
    center.value = centerResponse?.data || null;
    certificates.value = (certificateResponse as any)?.data || [];
    learningTasks.value = (taskResponse as any)?.data || [];
    points.value = (pointsResponse as any)?.data || null;
    catalog.value = (catalogResponse as any)?.data || [];
    redemptions.value = (redemptionResponse as any)?.data || [];
    stars.value = (starResponse as any)?.data || [];
    pushEnabled.value = (pushResponse as any)?.data?.enabled ?? true;
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('personalCenter.loadFailed'));
  } finally {
    loading.value = false;
  }
};

// redeem spends points on one catalog item. The server does the balance and
// stock checks inside a transaction; the UI only refreshes after success.
const redeem = async (row: RewardCatalogItem) => {
  busyId.value = row.id;
  try {
    await redeemReward(row.id);
    MessagePlugin.success(t('personalCenter.redeemed'));
    const [pointsResponse, redemptionResponse, catalogResponse] = await Promise.all([
      getMyPoints(),
      listMyRedemptions(),
      listRewardCatalog(),
    ]);
    points.value = (pointsResponse as any)?.data || points.value;
    redemptions.value = (redemptionResponse as any)?.data || redemptions.value;
    catalog.value = (catalogResponse as any)?.data || catalog.value;
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('personalCenter.loadFailed'));
  } finally {
    busyId.value = null;
  }
};

const cancelRow = async (row: RewardRedemption) => {
  busyId.value = row.id;
  try {
    await cancelRedemption(row.id);
    MessagePlugin.success(t('personalCenter.canceled'));
    const [pointsResponse, redemptionResponse, catalogResponse] = await Promise.all([
      getMyPoints(),
      listMyRedemptions(),
      listRewardCatalog(),
    ]);
    points.value = (pointsResponse as any)?.data || points.value;
    redemptions.value = (redemptionResponse as any)?.data || redemptions.value;
    catalog.value = (catalogResponse as any)?.data || catalog.value;
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('personalCenter.loadFailed'));
  } finally {
    busyId.value = null;
  }
};

const togglePush = async (enabled: boolean) => {
  pushBusy.value = true;
  try {
    const response = await setEmbedPushPreference(enabled);
    pushEnabled.value = (response as any)?.data?.enabled ?? enabled;
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('personalCenter.loadFailed'));
  } finally {
    pushBusy.value = false;
  }
};

const levelLabel = (level: string) => t(`personalCenter.level_${level}`);
const actionLabel = (action: string) => t(`personalCenter.pointsAction_${action}`);

const redemptionTheme = (status: string): 'default' | 'success' | 'warning' | 'danger' => {
  switch (status) {
    case 'fulfilled':
      return 'success';
    case 'pending':
      return 'warning';
    case 'rejected':
      return 'danger';
    default:
      return 'default';
  }
};

const formatTime = (value?: string | null) => {
  if (!value) return '--';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '--';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
};

onMounted(load);
</script>

<style scoped>
.personal-center {
  padding: 24px 32px 48px;
  max-width: 1200px;
  margin: 0 auto;
}

.personal-center__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}

.personal-center__title {
  font-size: var(--app-text-3xl);
  font-weight: 600;
  margin: 0 0 4px;
}

.personal-center__desc {
  margin: 0;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-md);
}

.personal-center__metrics {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}

.personal-center__metric {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 16px;
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-md);
}

.personal-center__label {
  font-size: var(--app-text-sm);
  color: var(--td-text-color-secondary);
}

.personal-center__value {
  font-size: var(--app-text-4xl);
  font-weight: 600;
}

.personal-center__switch-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 16px;
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-md);
  margin-bottom: 8px;
}

.personal-center__switch-desc {
  margin: 4px 0 0;
  font-size: var(--app-text-sm);
  color: var(--td-text-color-secondary);
}

.personal-center__subtitle {
  font-size: var(--app-text-md);
  font-weight: 500;
  margin: 20px 0 8px;
  color: var(--td-text-color-secondary);
}

.is-negative {
  color: var(--td-error-color);
}

.is-hidden {
  color: var(--td-text-color-placeholder);
  text-decoration: line-through;
}
</style>
