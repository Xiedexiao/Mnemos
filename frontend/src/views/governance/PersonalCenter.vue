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
    </t-tabs>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { useI18n } from 'vue-i18n';
import { MessagePlugin } from 'tdesign-vue-next';
import {
  completeLearningTask,
  getPersonalCenter,
  listLearningCertificates,
  listLearningTasks,
  type LearningCertificate,
  type LearningTask,
  type PersonalCenter,
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
const activeTab = ref<'reviews' | 'favorites' | 'knowledge' | 'tasks' | 'certificates'>('reviews');

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
    const [centerResponse, certificateResponse, taskResponse] = await Promise.all([
      getPersonalCenter(),
      // 认证与学习任务是辅助信息：取不到时页面其余部分仍然可用。
      listLearningCertificates().catch(() => ({ data: [] as LearningCertificate[] })),
      listLearningTasks().catch(() => ({ data: [] as LearningTask[] })),
    ]);
    center.value = centerResponse?.data || null;
    certificates.value = (certificateResponse as any)?.data || [];
    learningTasks.value = (taskResponse as any)?.data || [];
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('personalCenter.loadFailed'));
  } finally {
    loading.value = false;
  }
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
  font-size: 20px;
  font-weight: 600;
  margin: 0 0 4px;
}

.personal-center__desc {
  margin: 0;
  color: var(--td-text-color-secondary);
  font-size: 13px;
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
  border-radius: 8px;
}

.personal-center__label {
  font-size: 12px;
  color: var(--td-text-color-secondary);
}

.personal-center__value {
  font-size: 22px;
  font-weight: 600;
}

.is-hidden {
  color: var(--td-text-color-placeholder);
  text-decoration: line-through;
}
</style>
