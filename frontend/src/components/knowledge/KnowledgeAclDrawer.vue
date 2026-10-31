<template>
  <t-drawer
    :visible="visible"
    placement="right"
    size="560px"
    destroy-on-close
    :close-btn="true"
    :footer="false"
    :header="false"
    :confirm-on-exit="false"
    @close="handleClose"
  >
    <div class="kb-acl-drawer" @click.stop>
      <header class="kb-acl-drawer__head">
        <div class="kb-acl-drawer__head-text">
          <div class="kb-acl-drawer__title">{{ t('knowledgeGovernance.aclDrawerTitle') }}</div>
          <div class="kb-acl-drawer__subtitle" :title="knowledgeTitle || knowledgeId">
            {{ knowledgeTitle || knowledgeId || '--' }}
          </div>
        </div>
      </header>

      <!-- 治理规则提示：deny 优先；secret/confidential 需显式 allow 才可问 -->
      <div class="kb-acl-drawer__notice" role="note">
        <t-icon name="info-circle" class="kb-acl-drawer__notice-icon" />
        <span>{{ t('knowledgeGovernance.aclNotice') }}</span>
      </div>

      <!-- 授权列表 -->
      <section class="kb-acl-drawer__section">
        <div class="kb-acl-drawer__section-header">
          <span class="kb-acl-drawer__section-title">{{ t('knowledgeGovernance.aclListTitle') }}</span>
          <span class="kb-acl-drawer__count">{{ grants.length }}</span>
        </div>
        <div v-if="loading" class="kb-acl-drawer__loading">
          <t-loading size="small" />
        </div>
        <div v-else-if="grants.length === 0" class="kb-acl-drawer__empty">
          <t-empty :description="t('knowledgeGovernance.aclEmpty')" />
        </div>
        <div v-else class="kb-acl-drawer__table">
          <t-table
            row-key="id"
            size="small"
            hover
            :data="grants"
            :columns="columns"
            :loading="loading"
          >
            <template #subject="{ row }">
              <span class="kb-acl-subject" :title="row.subject_id">
                <t-tag size="small" variant="outline" theme="default" class="kb-acl-subject__type">
                  {{ row.subject_type || 'user' }}
                </t-tag>
                <span class="kb-acl-subject__id">{{ row.subject_id }}</span>
              </span>
            </template>
            <template #action="{ row }">
              <code class="kb-acl-action">{{ row.action }}</code>
            </template>
            <template #effect="{ row }">
              <t-tag size="small" variant="light" :theme="row.effect === 'deny' ? 'danger' : 'success'">
                {{ effectLabel(row.effect) }}
              </t-tag>
            </template>
            <template #expires_at="{ row }">{{ formatExpiry(row.expires_at) }}</template>
            <template #op="{ row }">
              <t-popconfirm
                :content="t('knowledgeGovernance.aclRevokeConfirm')"
                :confirm-btn="{ content: t('common.confirm'), theme: 'danger' }"
                :cancel-btn="{ content: t('common.cancel') }"
                placement="left"
                @confirm="revoke(row)"
              >
                <t-button
                  theme="danger"
                  variant="text"
                  size="small"
                  shape="square"
                  :loading="revokingId === row.id"
                  :aria-label="t('knowledgeGovernance.aclRevoke')"
                >
                  <template #icon><t-icon name="delete" /></template>
                </t-button>
              </t-popconfirm>
            </template>
          </t-table>
        </div>
      </section>

      <!-- 添加授权 -->
      <section class="kb-acl-drawer__section">
        <div class="kb-acl-drawer__section-header">
          <span class="kb-acl-drawer__section-title">{{ t('knowledgeGovernance.aclAddTitle') }}</span>
        </div>
        <t-form class="kb-acl-drawer__form" label-align="top" @submit.prevent="submitGrant">
          <t-form-item :label="t('knowledgeGovernance.aclSubjectId')" :status="subjectError ? 'error' : undefined"
            :help="subjectError || undefined">
            <t-input
              v-model="form.subject_id"
              :placeholder="t('knowledgeGovernance.aclSubjectIdPlaceholder')"
              :disabled="submitting"
              clearable
            />
          </t-form-item>
          <div class="kb-acl-drawer__form-row">
            <t-form-item :label="t('knowledgeGovernance.aclAction')">
              <t-select
                v-model="form.action"
                :options="actionOptions"
                :disabled="submitting"
              />
            </t-form-item>
            <t-form-item :label="t('knowledgeGovernance.aclEffect')">
              <t-select
                v-model="form.effect"
                :options="effectOptions"
                :disabled="submitting"
              />
            </t-form-item>
          </div>
          <t-form-item :label="t('knowledgeGovernance.aclExpiresAt')">
            <t-date-picker
              v-model="form.expires_at"
              mode="date"
              clearable
              :disabled="submitting"
              :placeholder="t('knowledgeGovernance.aclExpiresAtPlaceholder')"
              class="kb-acl-drawer__date"
            />
          </t-form-item>
          <div class="kb-acl-drawer__form-actions">
            <t-button theme="primary" :loading="submitting" @click="submitGrant">
              {{ t('knowledgeGovernance.aclGrant') }}
            </t-button>
          </div>
        </t-form>
      </section>
    </div>
  </t-drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import { MessagePlugin } from 'tdesign-vue-next';
import {
  KNOWLEDGE_ACL_ACTIONS,
  grantKnowledgeAcl,
  listKnowledgeAcl,
  revokeKnowledgeAcl,
  type KnowledgeAclAction,
  type KnowledgeAclEntry,
  type KnowledgeAclEffect,
} from '@/api/knowledge-governance';

/**
 * Per-document ACL drawer (KnowAxis V3.0 §2.1 v1). Lists one knowledge
 * entry's grant rows and lets Admins add (grant/deny one action for one
 * user) or revoke them. Deny wins over allow; secret/confidential content
 * additionally needs an explicit allow before it can be asked about —
 * both rules are surfaced in the notice banner.
 */

const props = defineProps<{
  visible: boolean;
  knowledgeId?: string;
  knowledgeTitle?: string;
  classification?: string;
}>();

const emit = defineEmits<{
  (e: 'update:visible', value: boolean): void;
  (e: 'changed'): void;
}>();

const { t } = useI18n();

const grants = ref<KnowledgeAclEntry[]>([]);
const loading = ref(false);
const submitting = ref(false);
const revokingId = ref<string | null>(null);
const subjectError = ref('');

const form = ref<{
  subject_id: string;
  action: KnowledgeAclAction;
  effect: KnowledgeAclEffect;
  expires_at: string | null;
}>({
  subject_id: '',
  action: 'view',
  effect: 'allow',
  expires_at: null,
});

const actionOptions = computed(() =>
  KNOWLEDGE_ACL_ACTIONS.map((action) => ({
    label: t(`knowledgeGovernance.aclAction_${action}`),
    value: action,
  })),
);

const effectOptions = computed(() => [
  { label: t('knowledgeGovernance.aclEffectAllow'), value: 'allow' },
  { label: t('knowledgeGovernance.aclEffectDeny'), value: 'deny' },
]);

const columns = computed(() => [
  { colKey: 'subject', title: t('knowledgeGovernance.aclColumnSubject'), minWidth: 150 },
  { colKey: 'action', title: t('knowledgeGovernance.aclColumnAction'), width: 84 },
  { colKey: 'effect', title: t('knowledgeGovernance.aclColumnEffect'), width: 76 },
  { colKey: 'expires_at', title: t('knowledgeGovernance.aclColumnExpires'), width: 108 },
  { colKey: 'op', title: t('knowledgeGovernance.aclColumnOp'), width: 64 },
]);

const effectLabel = (effect: string) =>
  effect === 'deny'
    ? t('knowledgeGovernance.aclEffectDeny')
    : t('knowledgeGovernance.aclEffectAllow');

const formatExpiry = (value?: string | null) => {
  if (!value) return t('knowledgeGovernance.aclNeverExpires');
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return '--';
  const yy = d.getFullYear();
  const mm = String(d.getMonth() + 1).padStart(2, '0');
  const dd = String(d.getDate()).padStart(2, '0');
  return `${yy}-${mm}-${dd}`;
};

const fetchGrants = async () => {
  if (!props.knowledgeId) return;
  loading.value = true;
  try {
    const res = await listKnowledgeAcl(props.knowledgeId);
    grants.value = (res?.data as KnowledgeAclEntry[]) || [];
  } catch (error: any) {
    grants.value = [];
    MessagePlugin.error(error?.message || t('knowledgeGovernance.aclLoadFailed'));
  } finally {
    loading.value = false;
  }
};

watch(
  () => [props.visible, props.knowledgeId] as const,
  ([visible]) => {
    if (visible && props.knowledgeId) {
      form.value = { subject_id: '', action: 'view', effect: 'allow', expires_at: null };
      subjectError.value = '';
      void fetchGrants();
    }
  },
  { immediate: true },
);

const handleClose = () => {
  emit('update:visible', false);
};

// Date picker yields 'YYYY-MM-DD'; expire at the end of that local day so a
// same-day expiry stays usable until midnight.
const toExpiresAtIso = (value: string): string => {
  const d = new Date(`${value}T23:59:59`);
  return Number.isNaN(d.getTime()) ? value : d.toISOString();
};

const submitGrant = async () => {
  const subjectId = form.value.subject_id.trim();
  if (!subjectId) {
    subjectError.value = t('knowledgeGovernance.aclSubjectRequired');
    return;
  }
  subjectError.value = '';
  if (!props.knowledgeId) return;
  submitting.value = true;
  try {
    await grantKnowledgeAcl(props.knowledgeId, {
      subject_type: 'user',
      subject_id: subjectId,
      action: form.value.action,
      effect: form.value.effect,
      expires_at: form.value.expires_at ? toExpiresAtIso(form.value.expires_at) : null,
    });
    MessagePlugin.success(t('knowledgeGovernance.aclGrantSuccess'));
    form.value = { subject_id: '', action: 'view', effect: 'allow', expires_at: null };
    await fetchGrants();
    emit('changed');
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('knowledgeGovernance.aclGrantFailed'));
  } finally {
    submitting.value = false;
  }
};

const revoke = async (row: KnowledgeAclEntry) => {
  if (!props.knowledgeId || !row?.id) return;
  revokingId.value = row.id;
  try {
    await revokeKnowledgeAcl(props.knowledgeId, row.id);
    MessagePlugin.success(t('knowledgeGovernance.aclRevokeSuccess'));
    await fetchGrants();
    emit('changed');
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('knowledgeGovernance.aclRevokeFailed'));
  } finally {
    revokingId.value = null;
  }
};

defineExpose({ fetchGrants, grants });
</script>

<style scoped lang="less">
.kb-acl-drawer {
  display: flex;
  flex-direction: column;
  gap: 16px;
  height: 100%;
  padding: 4px;
}

.kb-acl-drawer__head-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.kb-acl-drawer__title {
  color: var(--td-text-color-primary);
  font-size: var(--app-text-lg);
  font-weight: 600;
  line-height: 26px;
}

.kb-acl-drawer__subtitle {
  min-width: 0;
  overflow: hidden;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-sm);
  line-height: 20px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kb-acl-drawer__notice {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 10px 12px;
  border-radius: var(--app-radius-md);
  background: color-mix(in srgb, var(--td-warning-color) 8%, transparent);
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-sm);
  line-height: 20px;
}

.kb-acl-drawer__notice-icon {
  flex: 0 0 auto;
  margin-top: 2px;
  color: var(--td-warning-color);
}

.kb-acl-drawer__section {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 0;
}

.kb-acl-drawer__section-header {
  display: flex;
  align-items: center;
  gap: 6px;
}

.kb-acl-drawer__section-title {
  color: var(--td-text-color-primary);
  font-size: var(--app-text-md);
  font-weight: 500;
}

.kb-acl-drawer__count {
  color: var(--td-text-color-placeholder);
  font-size: var(--app-text-xs);
  font-variant-numeric: tabular-nums;
}

.kb-acl-drawer__loading,
.kb-acl-drawer__empty {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px 0;
}

.kb-acl-drawer__table {
  max-height: 320px;
  overflow: auto;
}

.kb-acl-subject {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  max-width: 100%;
}

.kb-acl-subject__id {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}

.kb-acl-action {
  padding: 1px 6px;
  border-radius: var(--app-radius-xs);
  background: var(--td-bg-color-secondarycontainer);
  font-size: var(--app-text-xs);
}

.kb-acl-drawer__form-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.kb-acl-drawer__date {
  width: 100%;
}

.kb-acl-drawer__form-actions {
  display: flex;
  justify-content: flex-end;
}
</style>
