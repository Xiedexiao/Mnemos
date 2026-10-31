<template>
  <div class="review-workbench">
    <header class="review-workbench__header">
      <div class="review-workbench__heading">
        <h2 class="review-workbench__title">{{ t('reviewWorkbench.title') }}</h2>
        <p class="review-workbench__desc">{{ t('reviewWorkbench.description') }}</p>
      </div>
      <t-button variant="outline" size="medium" :loading="loading" @click="loadTasks">
        <template #icon><t-icon name="refresh" /></template>
        {{ t('common.refresh') }}
      </t-button>
    </header>

    <!-- 403 / 加载失败：审核面是 Admin+ 的治理入口 -->
    <div v-if="loadFailed" class="review-workbench__state">
      <t-empty :description="t('reviewWorkbench.loadFailed')" />
    </div>
    <div v-else-if="!loading && tasks.length === 0" class="review-workbench__state">
      <t-empty :description="t('reviewWorkbench.empty')" />
    </div>
    <div v-else class="review-workbench__table" :class="{ 'is-loading': loading && tasks.length === 0 }">
      <t-table
        row-key="id"
        :data="tasks"
        :columns="columns"
        size="medium"
        hover
        expand-on-row-click
        :loading="loading"
        :expanded-row-keys="expandedKeys"
        :empty="t('reviewWorkbench.empty')"
        @expand-change="onExpandChange"
      >
        <template #title="{ row }">
          <div class="review-task-title">
            <span class="review-task-title__name" :title="taskTitle(row)">{{ taskTitle(row) }}</span>
            <span class="review-task-title__meta" :title="row.knowledge_id">{{ row.knowledge_id }}</span>
          </div>
        </template>
        <template #risk_level="{ row }">
          <t-tag size="small" variant="light" :theme="riskMeta(row.risk_level).theme">
            {{ riskMeta(row.risk_level).label }}
          </t-tag>
        </template>
        <template #requested_by="{ row }">
          <span class="review-task-user" :title="row.requested_by">{{ row.requested_by || '--' }}</span>
        </template>
        <template #sla_due="{ row }">
          <span class="review-task-sla" :class="{ 'is-overdue': isSlaOverdue(row) }">
            {{ formatTime(row.sla_due) }}
          </span>
        </template>
        <template #status="{ row }">
          <t-tag size="small" variant="light" :theme="statusMeta(row.status).theme">
            {{ statusMeta(row.status).label }}
          </t-tag>
        </template>
        <template #op="{ row }">
          <div v-if="isPending(row)" class="review-task-actions" @click.stop>
            <t-button theme="primary" variant="text" size="small"
              :loading="decidingTaskId === row.id" @click="openApprove(row)">
              {{ t('reviewWorkbench.decideApprove') }}
            </t-button>
            <t-button theme="danger" variant="text" size="small"
              :loading="decidingTaskId === row.id" @click="openReject(row)">
              {{ t('reviewWorkbench.decideReject') }}
            </t-button>
          </div>
          <span v-else class="review-task-actions__none">--</span>
        </template>
        <!-- 基线摘要：v1 直接折叠展示冻结的 JSON 快照，不做富 diff -->
        <template #expandedRow="{ row }">
          <div class="review-baseline">
            <div class="review-baseline__head">
              <span class="review-baseline__label">{{ t('reviewWorkbench.baselineTitle') }}</span>
              <code v-if="row.content_hash" class="review-baseline__hash" :title="row.content_hash">
                {{ shortHash(row.content_hash) }}
              </code>
            </div>
            <pre class="review-baseline__json">{{ formatBaseline(row) }}</pre>
            <div v-if="row.comment" class="review-baseline__decision">
              <span class="review-baseline__label">{{ t('reviewWorkbench.lastDecision') }}</span>
              <span>{{ row.comment }}</span>
            </div>
          </div>
        </template>
      </t-table>
    </div>

    <!-- 驳回弹窗：原因码 + 备注（均必填） -->
    <t-dialog
      v-model:visible="rejectDialogVisible"
      :header="t('reviewWorkbench.rejectTitle')"
      width="480px"
      :confirm-btn="{
        content: t('reviewWorkbench.decideReject'),
        theme: 'danger',
        loading: decidingTaskId === rejectingTask?.id,
      }"
      :cancel-btn="{ content: t('common.cancel') }"
      :close-on-overlay-click="false"
      @confirm="confirmReject"
    >
      <div class="review-reject-form" @click.stop>
        <p class="review-reject-form__task">
          {{ rejectingTask ? taskTitle(rejectingTask) : '' }}
        </p>
        <t-form label-align="top">
          <t-form-item :label="t('reviewWorkbench.reasonCode')" :status="rejectError ? 'error' : undefined"
            :help="rejectError || undefined">
            <t-select
              v-model="rejectForm.reason_code"
              :options="reasonCodeOptions"
              :placeholder="t('reviewWorkbench.reasonCodePlaceholder')"
            />
          </t-form-item>
          <t-form-item :label="t('reviewWorkbench.comment')">
            <t-textarea
              v-model="rejectForm.comment"
              :placeholder="t('reviewWorkbench.commentPlaceholder')"
              :autosize="{ minRows: 3, maxRows: 6 }"
              :maxlength="2000"
            />
          </t-form-item>
        </t-form>
      </div>
    </t-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { useI18n } from 'vue-i18n';
import { MessagePlugin, DialogPlugin } from 'tdesign-vue-next';
import {
  REVIEW_REASON_CODES,
  decideReviewTask,
  listReviewTasks,
  type ReviewReasonCode,
  type ReviewTask,
} from '@/api/review';

/**
 * 审核工作台（KnowAxis V3.0 §6 v1 子集）。租户 Admin 的待审池：列出
 * pending 审批任务（含冻结基线摘要），通过 / 驳回。驳回必须选原因码并
 * 填写备注；通过允许附注意见。四眼校验（不能审自己提交的任务）由后端
 * 兜底，403 会以错误 toast 呈现。
 */

const { t } = useI18n();

const tasks = ref<ReviewTask[]>([]);
const loading = ref(false);
const loadFailed = ref(false);
const expandedKeys = ref<string[]>([]);
const decidingTaskId = ref<string | null>(null);

const rejectDialogVisible = ref(false);
const rejectingTask = ref<ReviewTask | null>(null);
const rejectForm = ref<{ reason_code: ReviewReasonCode | ''; comment: string }>({
  reason_code: '',
  comment: '',
});
const rejectError = ref('');

const isPending = (row: ReviewTask) => row.status === 'pending';

const taskTitle = (row: ReviewTask) => {
  const baseline = row.baseline as { title?: string } | undefined;
  return baseline?.title || row.knowledge_id || row.id;
};

const columns = computed(() => [
  { colKey: 'title', title: t('reviewWorkbench.columnTitle'), ellipsis: true, minWidth: 220 },
  { colKey: 'risk_level', title: t('reviewWorkbench.columnRisk'), width: 96 },
  { colKey: 'requested_by', title: t('reviewWorkbench.columnSubmitter'), width: 140, ellipsis: true },
  { colKey: 'sla_due', title: t('reviewWorkbench.columnSla'), width: 150 },
  { colKey: 'status', title: t('reviewWorkbench.columnStatus'), width: 104 },
  { colKey: 'op', title: t('reviewWorkbench.columnOp'), width: 140 },
]);

// 风险级别由密级路由：confidential→high、secret→mid、其余 low。
const riskMeta = (level: string): { label: string; theme: 'default' | 'success' | 'warning' | 'danger' } => {
  switch (level) {
    case 'high':
      return { label: t('reviewWorkbench.riskHigh'), theme: 'danger' };
    case 'mid':
      return { label: t('reviewWorkbench.riskMid'), theme: 'warning' };
    case 'low':
      return { label: t('reviewWorkbench.riskLow'), theme: 'success' };
    default:
      return { label: level || '--', theme: 'default' };
  }
};

const statusMeta = (status: string): { label: string; theme: 'default' | 'success' | 'warning' | 'danger' } => {
  switch (status) {
    case 'pending':
      return { label: t('reviewWorkbench.statusPending'), theme: 'warning' };
    case 'approved':
      return { label: t('reviewWorkbench.statusApproved'), theme: 'success' };
    case 'rejected':
      return { label: t('reviewWorkbench.statusRejected'), theme: 'danger' };
    case 'canceled':
      return { label: t('reviewWorkbench.statusCanceled'), theme: 'default' };
    default:
      return { label: status || '--', theme: 'default' };
  }
};

const reasonCodeOptions = computed(() =>
  REVIEW_REASON_CODES.map((code) => ({
    label: t(`reviewWorkbench.reason_${code}`),
    value: code,
  })),
);

const formatTime = (value?: string | null) => {
  if (!value) return '--';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return '--';
  const yy = d.getFullYear();
  const mm = String(d.getMonth() + 1).padStart(2, '0');
  const dd = String(d.getDate()).padStart(2, '0');
  const hh = String(d.getHours()).padStart(2, '0');
  const mi = String(d.getMinutes()).padStart(2, '0');
  return `${yy}-${mm}-${dd} ${hh}:${mi}`;
};

const isSlaOverdue = (row: ReviewTask) =>
  isPending(row) && !!row.sla_due && new Date(row.sla_due).getTime() <= Date.now();

const shortHash = (hash: string) => (hash.length > 12 ? `${hash.slice(0, 12)}…` : hash);

const formatBaseline = (row: ReviewTask) => {
  const baseline = row.baseline ?? {};
  try {
    return JSON.stringify(baseline, null, 2);
  } catch {
    return String(baseline);
  }
};

const onExpandChange = (keys: (string | number)[]) => {
  expandedKeys.value = keys as string[];
};

const loadTasks = async () => {
  loading.value = true;
  loadFailed.value = false;
  try {
    const res = await listReviewTasks();
    tasks.value = res?.data || [];
    expandedKeys.value = [];
  } catch (error: any) {
    tasks.value = [];
    loadFailed.value = true;
    MessagePlugin.error(error?.message || t('reviewWorkbench.loadFailed'));
  } finally {
    loading.value = false;
  }
};

onMounted(() => {
  void loadTasks();
});

// ── 通过：简单确认；通过无需原因码，备注可选 ──
const openApprove = (row: ReviewTask) => {
  const dialog = DialogPlugin.confirm({
    header: t('reviewWorkbench.approveTitle'),
    body: t('reviewWorkbench.approveBody', { title: taskTitle(row) }),
    confirmBtn: { content: t('reviewWorkbench.decideApprove'), theme: 'primary', loading: decidingTaskId.value === row.id },
    cancelBtn: { content: t('common.cancel') },
    onConfirm: async () => {
      await submitDecision(row, 'approved');
      dialog.destroy();
    },
    onClose: () => {
      dialog.destroy();
    },
  });
};

// ── 驳回：原因码 + 备注必填 ──
const openReject = (row: ReviewTask) => {
  rejectingTask.value = row;
  rejectForm.value = { reason_code: '', comment: '' };
  rejectError.value = '';
  rejectDialogVisible.value = true;
};

const confirmReject = async () => {
  const task = rejectingTask.value;
  if (!task) return;
  if (!rejectForm.value.reason_code) {
    rejectError.value = t('reviewWorkbench.reasonRequired');
    return;
  }
  if (!rejectForm.value.comment.trim()) {
    rejectError.value = t('reviewWorkbench.commentRequired');
    return;
  }
  rejectError.value = '';
  await submitDecision(task, 'rejected');
  rejectDialogVisible.value = false;
};

const submitDecision = async (row: ReviewTask, decision: 'approved' | 'rejected') => {
  decidingTaskId.value = row.id;
  try {
    const res = await decideReviewTask(row.id, {
      decision,
      reason_code: decision === 'rejected' ? (rejectForm.value.reason_code as ReviewReasonCode) : '',
      comment: decision === 'rejected' ? rejectForm.value.comment.trim() : '',
    });
    const updated = res?.data as ReviewTask | undefined;
    // dual 模式一票可能仍不够法定人数：以返回任务的最终状态为准提示。
    const finalStatus = updated?.status;
    if (finalStatus && finalStatus !== 'pending') {
      MessagePlugin.success(t(`reviewWorkbench.decided_${finalStatus}`));
    } else {
      MessagePlugin.success(t('reviewWorkbench.voteRecorded'));
    }
    await loadTasks();
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('reviewWorkbench.decideFailed'));
    throw error;
  } finally {
    decidingTaskId.value = null;
  }
};
</script>

<style scoped lang="less">
.review-workbench {
  display: flex;
  flex-direction: column;
  gap: 16px;
  height: 100%;
  padding: 24px;
  overflow: auto;
}

.review-workbench__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}

.review-workbench__heading {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.review-workbench__title {
  margin: 0;
  color: var(--td-text-color-primary);
  font-size: var(--app-text-xl);
  font-weight: 600;
  line-height: 28px;
}

.review-workbench__desc {
  margin: 0;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-sm);
  line-height: 20px;
}

.review-workbench__state {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 64px 0;
}

.review-workbench__table {
  min-height: 200px;
}

.review-task-title {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.review-task-title__name {
  min-width: 0;
  overflow: hidden;
  color: var(--td-text-color-primary);
  font-weight: 500;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.review-task-title__meta {
  min-width: 0;
  overflow: hidden;
  color: var(--td-text-color-placeholder);
  font-size: var(--app-text-xs);
  text-overflow: ellipsis;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}

.review-task-user {
  display: inline-block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}

.review-task-sla {
  font-variant-numeric: tabular-nums;

  &.is-overdue {
    color: var(--td-error-color);
    font-weight: 500;
  }
}

.review-task-actions {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.review-task-actions__none {
  color: var(--td-text-color-disabled);
}

.review-baseline {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 4px 8px;
}

.review-baseline__head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.review-baseline__label {
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-sm);
  font-weight: 500;
}

.review-baseline__hash {
  padding: 1px 6px;
  border-radius: var(--app-radius-xs);
  background: var(--td-bg-color-secondarycontainer);
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-xs);
  font-variant-numeric: tabular-nums;
}

.review-baseline__json {
  max-height: 260px;
  margin: 0;
  padding: 10px 12px;
  border-radius: var(--app-radius-md);
  background: var(--td-bg-color-secondarycontainer);
  color: var(--td-text-color-primary);
  font-family: var(--app-font-family-mono, monospace);
  font-size: var(--app-text-xs);
  line-height: 18px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-all;
}

.review-baseline__decision {
  display: flex;
  align-items: baseline;
  gap: 8px;
  color: var(--td-text-color-primary);
  font-size: var(--app-text-sm);
}

.review-reject-form__task {
  margin: 0 0 10px;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-sm);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
