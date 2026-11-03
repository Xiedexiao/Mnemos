<template>
  <div class="ops-dashboard">
    <header class="ops-dashboard__header">
      <div>
        <h2 class="ops-dashboard__title">{{ t('opsDashboard.title') }}</h2>
        <p class="ops-dashboard__desc">{{ t('opsDashboard.description') }}</p>
      </div>
      <div class="ops-dashboard__actions">
        <t-input-number
          v-model="windowDays"
          theme="normal"
          :min="1"
          :max="365"
          :label="t('opsDashboard.window')"
          @change="load"
        />
        <t-button variant="outline" size="medium" :loading="loading" @click="load">
          <template #icon><t-icon name="refresh" /></template>
          {{ t('common.refresh') }}
        </t-button>
      </div>
    </header>

    <div v-if="forbidden" class="ops-dashboard__state">
      <t-empty :description="t('opsDashboard.noPermission')" />
    </div>

    <template v-else>
      <!-- 知识资产 -->
      <section class="ops-dashboard__section">
        <h3 class="ops-dashboard__section-title">{{ t('opsDashboard.sectionAssets') }}</h3>
        <div class="ops-dashboard__metrics">
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.knowledgeTotal') }}</span>
            <span class="ops-dashboard__value">{{ data?.overview.knowledge_total ?? 0 }}</span>
          </div>
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.spaces') }}</span>
            <span class="ops-dashboard__value">{{ data?.overview.knowledge_base_count ?? 0 }}</span>
          </div>
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.chunks') }}</span>
            <span class="ops-dashboard__value">{{ data?.overview.chunk_total ?? 0 }}</span>
          </div>
        </div>
        <div v-if="statusRows.length" class="ops-dashboard__chips">
          <t-tag v-for="row in statusRows" :key="row.status" variant="light" size="small">
            {{ row.status }}: {{ row.total }}
          </t-tag>
        </div>
      </section>

      <!-- 治理待办 -->
      <section class="ops-dashboard__section">
        <h3 class="ops-dashboard__section-title">{{ t('opsDashboard.sectionGovernance') }}</h3>
        <div class="ops-dashboard__metrics">
          <div v-for="metric in governanceMetrics" :key="metric.key" class="ops-dashboard__metric"
            :class="{ 'is-alert': metric.value > 0 }">
            <span class="ops-dashboard__label">{{ metric.label }}</span>
            <span class="ops-dashboard__value">{{ metric.value }}</span>
          </div>
        </div>
      </section>

      <!-- 审核效率 -->
      <section class="ops-dashboard__section">
        <h3 class="ops-dashboard__section-title">{{ t('opsDashboard.sectionDelivery') }}</h3>
        <div class="ops-dashboard__metrics">
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.approved') }}</span>
            <span class="ops-dashboard__value">{{ data?.delivery.approved_last_30_days ?? 0 }}</span>
          </div>
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.rejected') }}</span>
            <span class="ops-dashboard__value">{{ data?.delivery.rejected_last_30_days ?? 0 }}</span>
          </div>
          <div class="ops-dashboard__metric" :class="{ 'is-alert': (data?.delivery.overdue_pending ?? 0) > 0 }">
            <span class="ops-dashboard__label">{{ t('opsDashboard.overdue') }}</span>
            <span class="ops-dashboard__value">{{ data?.delivery.overdue_pending ?? 0 }}</span>
          </div>
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.avgDecision') }}</span>
            <span class="ops-dashboard__value">
              {{ (data?.delivery.average_decision_hours ?? 0).toFixed(1) }}
            </span>
          </div>
        </div>
      </section>

      <!-- 搜索质量 -->
      <section class="ops-dashboard__section">
        <h3 class="ops-dashboard__section-title">{{ t('opsDashboard.sectionSearch') }}</h3>
        <div class="ops-dashboard__metrics">
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.queries') }}</span>
            <span class="ops-dashboard__value">{{ data?.search_quality.total_queries ?? 0 }}</span>
          </div>
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.zeroResult') }}</span>
            <span class="ops-dashboard__value">{{ data?.search_quality.zero_result_queries ?? 0 }}</span>
          </div>
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.zeroRate') }}</span>
            <span class="ops-dashboard__value">
              {{ ((data?.search_quality.zero_result_rate ?? 0) * 100).toFixed(1) }}%
            </span>
          </div>
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.latency') }}</span>
            <span class="ops-dashboard__value">
              {{ Math.round(data?.search_quality.average_duration_ms ?? 0) }}
            </span>
          </div>
        </div>
      </section>

      <!-- 趋势（§16.1） -->
      <section class="ops-dashboard__section">
        <h3 class="ops-dashboard__section-title">{{ t('opsDashboard.sectionTrends') }}</h3>
        <t-table
          row-key="day"
          :data="data?.trends || []"
          :columns="trendColumns"
          size="small"
          :loading="loading"
          :empty="t('opsDashboard.emptyTrends')"
        />
      </section>

      <!-- 贡献排行（§16.2） -->
      <section class="ops-dashboard__section">
        <h3 class="ops-dashboard__section-title">{{ t('opsDashboard.sectionContributors') }}</h3>
        <t-table
          row-key="user_id"
          :data="data?.contributors || []"
          :columns="contributorColumns"
          size="small"
          :loading="loading"
          :empty="t('opsDashboard.emptyContributors')"
        />
      </section>

      <!-- 运行时可观测（§17.2） -->
      <section class="ops-dashboard__section">
        <h3 class="ops-dashboard__section-title">{{ t('opsDashboard.sectionObservability') }}</h3>
        <div class="ops-dashboard__metrics">
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.refusalRate') }}</span>
            <span class="ops-dashboard__value">
              {{ ((data?.observability.refusal_rate ?? 0) * 100).toFixed(1) }}%
            </span>
          </div>
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.permissionBlocked') }}</span>
            <span class="ops-dashboard__value">{{ data?.observability.permission_blocked ?? 0 }}</span>
          </div>
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.avgLatency') }}</span>
            <span class="ops-dashboard__value">
              {{ Math.round(data?.observability.average_latency_ms ?? 0) }}
            </span>
          </div>
          <div class="ops-dashboard__metric" :class="{ 'is-alert': (data?.observability.slow_queries ?? 0) > 0 }">
            <span class="ops-dashboard__label">{{ t('opsDashboard.slowQueries') }}</span>
            <span class="ops-dashboard__value">{{ data?.observability.slow_queries ?? 0 }}</span>
          </div>
        </div>
      </section>

      <!-- 成本（§16.1） -->
      <section class="ops-dashboard__section">
        <h3 class="ops-dashboard__section-title">{{ t('opsDashboard.sectionCost') }}</h3>
        <div class="ops-dashboard__metrics">
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.promptTokens') }}</span>
            <span class="ops-dashboard__value">{{ data?.cost.prompt_tokens ?? 0 }}</span>
          </div>
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.completionTokens') }}</span>
            <span class="ops-dashboard__value">{{ data?.cost.completion_tokens ?? 0 }}</span>
          </div>
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.totalTokens') }}</span>
            <span class="ops-dashboard__value">{{ data?.cost.total_tokens ?? 0 }}</span>
          </div>
          <div class="ops-dashboard__metric">
            <span class="ops-dashboard__label">{{ t('opsDashboard.calls') }}</span>
            <span class="ops-dashboard__value">{{ data?.cost.call_count ?? 0 }}</span>
          </div>
        </div>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { useI18n } from 'vue-i18n';
import { MessagePlugin } from 'tdesign-vue-next';
import { getOperationsDashboard, type OperationsDashboard } from '@/api/governance-console';

/**
 * 运营看板（KnowAxis V3.0 §16.1）。
 *
 * 所有数字直接来自后端各自垂直领域的表，页面不做二次计算——这样"看板显示
 * 12 条待审"与"工作台里正好 12 条"永远一致。
 */

const { t } = useI18n();

const data = ref<OperationsDashboard | null>(null);
const loading = ref(false);
const forbidden = ref(false);
const windowDays = ref(30);

const statusRows = computed(() => {
  const byStatus = data.value?.overview.knowledge_by_status || {};
  return Object.entries(byStatus).map(([status, total]) => ({ status, total }));
});

const governanceMetrics = computed(() => {
  const governance = data.value?.governance;
  return [
    { key: 'pending_reviews', label: t('opsDashboard.pendingReviews'), value: governance?.pending_reviews ?? 0 },
    { key: 'pending_review_cycles', label: t('opsDashboard.pendingCycles'), value: governance?.pending_review_cycles ?? 0 },
    { key: 'expired', label: t('opsDashboard.expired'), value: governance?.expired ?? 0 },
    { key: 'conflicts', label: t('opsDashboard.conflicts'), value: governance?.conflicts ?? 0 },
    { key: 'open_quality_tasks', label: t('opsDashboard.qualityTasks'), value: governance?.open_quality_tasks ?? 0 },
    { key: 'open_impact_tasks', label: t('opsDashboard.impactTasks'), value: governance?.open_impact_tasks ?? 0 },
    { key: 'open_knowledge_gaps', label: t('opsDashboard.gaps'), value: governance?.open_knowledge_gaps ?? 0 },
    { key: 'pending_field_migrations', label: t('opsDashboard.migrations'), value: governance?.pending_field_migrations ?? 0 },
    { key: 'acl_ceiling_violations', label: t('opsDashboard.aclViolations'), value: governance?.acl_ceiling_violations ?? 0 },
    { key: 'pending_change_signals', label: t('opsDashboard.signals'), value: governance?.pending_change_signals ?? 0 },
  ];
});

const trendColumns = computed(() => [
  { colKey: 'day', title: t('opsDashboard.colDay'), width: 130 },
  { colKey: 'knowledge_added', title: t('opsDashboard.colAdded'), width: 130 },
  { colKey: 'reviews_submitted', title: t('opsDashboard.colReviews'), width: 130 },
  { colKey: 'queries', title: t('opsDashboard.colQueries'), width: 130 },
]);

const contributorColumns = computed(() => [
  { colKey: 'user_id', title: t('opsDashboard.colUser'), ellipsis: true, minWidth: 200 },
  { colKey: 'score', title: t('opsDashboard.colScore'), width: 100 },
  { colKey: 'reviews_completed', title: t('opsDashboard.colReviewsDone'), width: 130 },
  { colKey: 'answers', title: t('opsDashboard.colAnswers'), width: 110 },
  { colKey: 'submitted', title: t('opsDashboard.colSubmitted'), width: 110 },
]);

const load = async () => {
  loading.value = true;
  forbidden.value = false;
  try {
    const response = await getOperationsDashboard(windowDays.value);
    data.value = response?.data || null;
  } catch (error: any) {
    const status = error?.response?.status ?? error?.status;
    if (status === 403 || status === 401) {
      forbidden.value = true;
    } else {
      MessagePlugin.error(error?.message || t('opsDashboard.loadFailed'));
    }
  } finally {
    loading.value = false;
  }
};

onMounted(load);
</script>

<style scoped>
.ops-dashboard {
  padding: 24px 32px 48px;
  max-width: 1400px;
  margin: 0 auto;
}

.ops-dashboard__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 20px;
}

.ops-dashboard__title {
  font-size: 20px;
  font-weight: 600;
  margin: 0 0 4px;
}

.ops-dashboard__desc {
  margin: 0;
  color: var(--td-text-color-secondary);
  font-size: 13px;
}

.ops-dashboard__actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.ops-dashboard__state {
  padding: 64px 0;
}

.ops-dashboard__section {
  margin-bottom: 28px;
}

.ops-dashboard__section-title {
  font-size: 14px;
  font-weight: 600;
  margin: 0 0 12px;
}

.ops-dashboard__metrics {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 12px;
}

.ops-dashboard__metric {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 16px;
  border: 1px solid var(--td-component-stroke);
  border-radius: 8px;
  background: var(--td-bg-color-container);
}

.ops-dashboard__metric.is-alert {
  border-color: var(--td-warning-color);
}

.ops-dashboard__label {
  font-size: 12px;
  color: var(--td-text-color-secondary);
}

.ops-dashboard__value {
  font-size: 22px;
  font-weight: 600;
}

.ops-dashboard__chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 12px;
}
</style>
