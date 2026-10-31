<template>
  <t-drawer
    :visible="visible"
    placement="right"
    size="560px"
    destroy-on-close
    :close-btn="true"
    :footer="false"
    :header="false"
    @close="handleClose"
  >
    <div class="kb-share-drawer" @click.stop>
      <header class="kb-share-drawer__head">
        <div class="kb-share-drawer__head-text">
          <div class="kb-share-drawer__title">{{ t('knowledgeGovernance.shareDrawerTitle') }}</div>
          <div class="kb-share-drawer__subtitle" :title="knowledgeTitle || knowledgeId">
            {{ knowledgeTitle || knowledgeId || '--' }}
          </div>
        </div>
      </header>

      <!-- 规则提示：机密不可外发；秘密不可带下载权限；链接即凭据 -->
      <div class="kb-share-drawer__notice" role="note">
        <t-icon name="info-circle" class="kb-share-drawer__notice-icon" />
        <span>{{ t('knowledgeGovernance.shareNotice') }}</span>
      </div>

      <!-- 外链列表 -->
      <section class="kb-share-drawer__section">
        <div class="kb-share-drawer__section-header">
          <span class="kb-share-drawer__section-title">{{ t('knowledgeGovernance.shareListTitle') }}</span>
          <span class="kb-share-drawer__count">{{ links.length }}</span>
        </div>
        <div v-if="loading" class="kb-share-drawer__loading">
          <t-loading size="small" />
        </div>
        <div v-else-if="links.length === 0" class="kb-share-drawer__empty">
          <t-empty :description="t('knowledgeGovernance.shareEmpty')" />
        </div>
        <div v-else class="kb-share-drawer__table">
          <t-table
            row-key="id"
            size="small"
            hover
            :data="links"
            :columns="columns"
            :loading="loading"
          >
            <template #token="{ row }">
              <code class="kb-share-token" :title="t('knowledgeGovernance.shareTokenMaskedTip')">
                {{ maskShareToken(row.token) }}
              </code>
            </template>
            <template #link_type="{ row }">
              {{ row.link_type === 'people'
                ? t('knowledgeGovernance.shareTypePeople')
                : t('knowledgeGovernance.shareTypeOrg') }}
            </template>
            <template #permission="{ row }">
              <code class="kb-share-permission">{{ row.permission }}</code>
            </template>
            <template #expires_at="{ row }">{{ formatExpiry(row.expires_at) }}</template>
            <template #visit_count="{ row }">{{ row.visit_count ?? 0 }}</template>
            <template #status="{ row }">
              <t-tag size="small" variant="light" :theme="statusMeta(row).theme">
                {{ statusMeta(row).label }}
              </t-tag>
            </template>
            <template #op="{ row }">
              <t-popconfirm
                v-if="isActive(row)"
                :content="t('knowledgeGovernance.shareRevokeConfirm')"
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
                  :aria-label="t('knowledgeGovernance.shareRevoke')"
                >
                  <template #icon><t-icon name="close-circle" /></template>
                </t-button>
              </t-popconfirm>
              <span v-else class="kb-share-drawer__muted">--</span>
            </template>
          </t-table>
        </div>
      </section>

      <!-- 创建外链 -->
      <section class="kb-share-drawer__section">
        <div class="kb-share-drawer__section-header">
          <span class="kb-share-drawer__section-title">{{ t('knowledgeGovernance.shareCreateTitle') }}</span>
        </div>
        <t-form class="kb-share-drawer__form" label-align="top" @submit.prevent="submitCreate">
          <div class="kb-share-drawer__form-row">
            <t-form-item :label="t('knowledgeGovernance.shareLinkType')">
              <t-select v-model="form.link_type" :options="linkTypeOptions" :disabled="creating" />
            </t-form-item>
            <t-form-item :label="t('knowledgeGovernance.sharePermission')">
              <t-select v-model="form.permission" :options="permissionOptions" :disabled="creating" />
            </t-form-item>
          </div>
          <t-form-item :label="t('knowledgeGovernance.shareValidity')">
            <t-select v-model="form.validity" :options="validityOptions" :disabled="creating" />
          </t-form-item>
          <t-form-item v-if="form.validity === 'custom'" :label="t('knowledgeGovernance.shareCustomExpiry')">
            <t-date-picker
              v-model="form.custom_expires_at"
              mode="date"
              clearable
              :disabled="creating"
              :placeholder="t('knowledgeGovernance.shareCustomExpiryPlaceholder')"
              class="kb-share-drawer__date"
            />
          </t-form-item>
          <div class="kb-share-drawer__form-actions">
            <t-button theme="primary" :loading="creating" :disabled="!canSubmit" @click="submitCreate">
              {{ t('knowledgeGovernance.shareCreate') }}
            </t-button>
          </div>
        </t-form>
      </section>
    </div>

    <!-- 创建成功：完整链接只在此弹窗完整展示一次 -->
    <t-dialog
      v-model:visible="createdDialogVisible"
      :header="t('knowledgeGovernance.shareCreatedTitle')"
      :footer="false"
      :close-on-overlay-click="false"
      width="520px"
    >
      <div class="kb-share-created">
        <p class="kb-share-created__hint">{{ t('knowledgeGovernance.shareCreatedHint') }}</p>
        <div class="kb-share-created__link-box">
          <span class="kb-share-created__link" :title="createdLink">{{ createdLink }}</span>
          <t-tooltip :content="t('common.copy')">
            <t-button variant="text" size="small" @click="copyCreatedLink">
              <t-icon name="file-copy" />
            </t-button>
          </t-tooltip>
        </div>
        <div class="kb-share-created__actions">
          <t-button theme="primary" variant="outline" @click="createdDialogVisible = false">
            {{ t('common.close') }}
          </t-button>
        </div>
      </div>
    </t-dialog>
  </t-drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import { MessagePlugin } from 'tdesign-vue-next';
import { copyWithToast } from '@/utils/clipboard';
import {
  buildShareLinkUrl,
  createKnowledgeShareLink,
  listKnowledgeShareLinks,
  maskShareToken,
  revokeKnowledgeShareLink,
  type KnowledgeShareLink,
  type ShareLinkPermission,
  type ShareLinkType,
} from '@/api/knowledge-governance';

/**
 * External share-link drawer (KnowAxis V3.0 §2.5 v1). Lists one knowledge
 * entry's links (tokens masked to their first 6 characters) and issues new
 * ones. The full URL is only ever shown in the post-create dialog — the
 * token is the visitor's only credential. Classification ceilings
 * (confidential never shares, secret never downloads) are enforced by the
 * backend and surfaced here as error toasts.
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

const links = ref<KnowledgeShareLink[]>([]);
const loading = ref(false);
const creating = ref(false);
const revokingId = ref<string | null>(null);
const createdDialogVisible = ref(false);
const createdLink = ref('');

type ValidityChoice = 7 | 30 | 90 | 'custom';

const form = ref<{
  link_type: ShareLinkType;
  permission: ShareLinkPermission;
  validity: ValidityChoice;
  custom_expires_at: string | null;
}>({
  link_type: 'org',
  permission: 'view',
  validity: 7,
  custom_expires_at: null,
});

const linkTypeOptions = computed(() => [
  { label: t('knowledgeGovernance.shareTypeOrg'), value: 'org' },
  { label: t('knowledgeGovernance.shareTypePeople'), value: 'people' },
]);

const permissionOptions = computed(() => [
  { label: t('knowledgeGovernance.sharePermissionView'), value: 'view' },
  { label: t('knowledgeGovernance.sharePermissionDownload'), value: 'download' },
]);

const validityOptions = computed(() => [
  { label: t('knowledgeGovernance.shareValidity7'), value: 7 },
  { label: t('knowledgeGovernance.shareValidity30'), value: 30 },
  { label: t('knowledgeGovernance.shareValidity90'), value: 90 },
  { label: t('knowledgeGovernance.shareValidityCustom'), value: 'custom' },
]);

const columns = computed(() => [
  { colKey: 'token', title: t('knowledgeGovernance.shareColumnToken'), minWidth: 96 },
  { colKey: 'link_type', title: t('knowledgeGovernance.shareColumnType'), width: 76 },
  { colKey: 'permission', title: t('knowledgeGovernance.shareColumnPermission'), width: 84 },
  { colKey: 'expires_at', title: t('knowledgeGovernance.shareColumnExpires'), width: 100 },
  { colKey: 'visit_count', title: t('knowledgeGovernance.shareColumnVisits'), width: 72 },
  { colKey: 'status', title: t('knowledgeGovernance.shareColumnStatus'), width: 78 },
  { colKey: 'op', title: t('knowledgeGovernance.shareColumnOp'), width: 64 },
]);

const isActive = (link: KnowledgeShareLink) => statusMeta(link).state === 'active';

// Status precedence: revoked → expired → active. Mirrors types.ShareLink.Active.
const statusMeta = (link: KnowledgeShareLink): { state: 'active' | 'revoked' | 'expired'; label: string; theme: 'success' | 'default' | 'danger' } => {
  if (link.revoked_at) {
    return { state: 'revoked', label: t('knowledgeGovernance.shareStatusRevoked'), theme: 'default' };
  }
  if (link.expires_at && new Date(link.expires_at).getTime() <= Date.now()) {
    return { state: 'expired', label: t('knowledgeGovernance.shareStatusExpired'), theme: 'danger' };
  }
  return { state: 'active', label: t('knowledgeGovernance.shareStatusActive'), theme: 'success' };
};

const formatExpiry = (value?: string | null) => {
  if (!value) return t('knowledgeGovernance.shareNeverExpires');
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return '--';
  const yy = d.getFullYear();
  const mm = String(d.getMonth() + 1).padStart(2, '0');
  const dd = String(d.getDate()).padStart(2, '0');
  return `${yy}-${mm}-${dd}`;
};

const fetchLinks = async () => {
  if (!props.knowledgeId) return;
  loading.value = true;
  try {
    const res = await listKnowledgeShareLinks(props.knowledgeId);
    links.value = (res?.data as KnowledgeShareLink[]) || [];
  } catch (error: any) {
    links.value = [];
    MessagePlugin.error(error?.message || t('knowledgeGovernance.shareLoadFailed'));
  } finally {
    loading.value = false;
  }
};

watch(
  () => [props.visible, props.knowledgeId] as const,
  ([visible]) => {
    if (visible && props.knowledgeId) {
      form.value = { link_type: 'org', permission: 'view', validity: 7, custom_expires_at: null };
      createdDialogVisible.value = false;
      createdLink.value = '';
      void fetchLinks();
    }
  },
  { immediate: true },
);

const handleClose = () => {
  emit('update:visible', false);
};

const canSubmit = computed(() => form.value.validity !== 'custom' || !!form.value.custom_expires_at);

// 'YYYY-MM-DD' → end of that local day (same-day expiry stays usable).
const toExpiresAtIso = (value: string): string => {
  const d = new Date(`${value}T23:59:59`);
  return Number.isNaN(d.getTime()) ? value : d.toISOString();
};

const submitCreate = async () => {
  if (!props.knowledgeId || !canSubmit.value) return;
  let expiresAt: string | null = null;
  if (form.value.validity === 'custom') {
    if (!form.value.custom_expires_at) return;
    expiresAt = toExpiresAtIso(form.value.custom_expires_at);
  } else {
    const d = new Date();
    d.setDate(d.getDate() + form.value.validity);
    expiresAt = d.toISOString();
  }
  creating.value = true;
  try {
    const res = await createKnowledgeShareLink(props.knowledgeId, {
      link_type: form.value.link_type,
      permission: form.value.permission,
      expires_at: expiresAt,
    });
    const link = res?.data as KnowledgeShareLink | undefined;
    createdLink.value = buildShareLinkUrl(link?.token || '');
    createdDialogVisible.value = true;
    MessagePlugin.success(t('knowledgeGovernance.shareCreateSuccess'));
    form.value = { link_type: 'org', permission: 'view', validity: 7, custom_expires_at: null };
    await fetchLinks();
    emit('changed');
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('knowledgeGovernance.shareCreateFailed'));
  } finally {
    creating.value = false;
  }
};

const copyCreatedLink = async () => {
  await copyWithToast(createdLink.value, 'knowledgeGovernance.shareCopySuccess');
};

const revoke = async (row: KnowledgeShareLink) => {
  if (!row?.id) return;
  revokingId.value = row.id;
  try {
    await revokeKnowledgeShareLink(row.id);
    MessagePlugin.success(t('knowledgeGovernance.shareRevokeSuccess'));
    await fetchLinks();
    emit('changed');
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('knowledgeGovernance.shareRevokeFailed'));
  } finally {
    revokingId.value = null;
  }
};

defineExpose({ fetchLinks, links });
</script>

<style scoped lang="less">
.kb-share-drawer {
  display: flex;
  flex-direction: column;
  gap: 16px;
  height: 100%;
  padding: 4px;
}

.kb-share-drawer__head-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}

.kb-share-drawer__title {
  color: var(--td-text-color-primary);
  font-size: var(--app-text-lg);
  font-weight: 600;
  line-height: 26px;
}

.kb-share-drawer__subtitle {
  min-width: 0;
  overflow: hidden;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-sm);
  line-height: 20px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kb-share-drawer__notice {
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

.kb-share-drawer__notice-icon {
  flex: 0 0 auto;
  margin-top: 2px;
  color: var(--td-warning-color);
}

.kb-share-drawer__section {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 0;
}

.kb-share-drawer__section-header {
  display: flex;
  align-items: center;
  gap: 6px;
}

.kb-share-drawer__section-title {
  color: var(--td-text-color-primary);
  font-size: var(--app-text-md);
  font-weight: 500;
}

.kb-share-drawer__count {
  color: var(--td-text-color-placeholder);
  font-size: var(--app-text-xs);
  font-variant-numeric: tabular-nums;
}

.kb-share-drawer__loading,
.kb-share-drawer__empty {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px 0;
}

.kb-share-drawer__table {
  max-height: 320px;
  overflow: auto;
}

.kb-share-token,
.kb-share-permission {
  padding: 1px 6px;
  border-radius: var(--app-radius-xs);
  background: var(--td-bg-color-secondarycontainer);
  font-size: var(--app-text-xs);
  font-variant-numeric: tabular-nums;
}

.kb-share-drawer__muted {
  color: var(--td-text-color-disabled);
}

.kb-share-drawer__form-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.kb-share-drawer__date {
  width: 100%;
}

.kb-share-drawer__form-actions {
  display: flex;
  justify-content: flex-end;
}

.kb-share-created__hint {
  margin: 0 0 10px;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-sm);
  line-height: 20px;
}

.kb-share-created__link-box {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 10px;
  border-radius: var(--app-radius-md);
  background: var(--td-bg-color-secondarycontainer);
}

.kb-share-created__link {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--app-font-family-mono, monospace);
  font-size: var(--app-text-sm);
  font-variant-numeric: tabular-nums;
}

.kb-share-created__actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 14px;
}
</style>
