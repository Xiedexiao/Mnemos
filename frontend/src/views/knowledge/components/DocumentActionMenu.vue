<script setup lang="ts">
import { computed } from 'vue';
import {
  governanceActionsForStatus,
  type GovernanceActionMeta,
} from '@/components/knowledge/governanceActions';

interface KnowledgeItem {
  id: string;
  file_name?: string;
  title?: string;
  type?: string;
  parse_status?: string;
  // Governance fields served by the knowledge list/detail API (A期).
  lifecycle_status?: string;
}

const props = defineProps<{
  item: KnowledgeItem;
  canDownload: boolean;
  canMutateKnowledge: boolean;
  traceVisible: boolean;
  /**
   * Show the per-document ACL entry (权限管理). The backend floors
   * grant/revoke at tenant Admin; callers pass their canManage signal.
   */
  canManageAcl?: boolean;
  /** Whether the knowledge base has a folder structure to file documents into. */
  foldersAvailable?: boolean;
}>();

const emit = defineEmits<{
  (e: 'download'): void;
  (e: 'edit'): void;
  (e: 'view-trace'): void;
  (e: 'reparse'): void;
  (e: 'cancel-parse'): void;
  (e: 'move'): void;
  (e: 'move-folder'): void;
  (e: 'batch-manage'): void;
  (e: 'delete'): void;
  (e: 'governance-action', action: GovernanceActionMeta): void;
  (e: 'acl-manage'): void;
  (e: 'share-manage'): void;
}>();


const CANCELABLE_PARSE_STATUSES = new Set(['pending', 'processing', 'finalizing']);

const isParseInFlight = computed(() =>
  CANCELABLE_PARSE_STATUSES.has(String(props.item.parse_status ?? ''))
);

const fileName = computed(() => props.item.file_name || props.item.title || props.item.id);

// 治理动作随 lifecycle_status 变化；后端对非法迁移仍会 400，这里只保证
// 菜单不展示注定失败的动作。
const governanceActions = computed(() => governanceActionsForStatus(props.item.lifecycle_status));
</script>

<template>
  <!-- 下载原始文档 -->
  <div
    v-if="canDownload && (item.type === 'file' || item.type === 'manual')"
    class="doc-action-menu-item"
    @click.stop="emit('download')"
  >
    <t-icon class="icon" name="download" />
    <span>{{ $t('common.download') }}</span>
  </div>

  <!-- 编辑文档 -->
  <div v-if="item.type === 'manual'" class="doc-action-menu-item" @click.stop="emit('edit')">
    <t-icon class="icon" name="edit" />
    <span>{{ $t('knowledgeBase.editDocument') }}</span>
  </div>

  <!-- 查看处理过程 -->
  <div v-if="traceVisible" class="doc-action-menu-item" @click.stop="emit('view-trace')">
    <t-icon class="icon" name="chart-bar" />
    <span>{{ $t('knowledgeStages.viewTrace') }}</span>
  </div>

  <!-- 重建知识 (in-flight: no popconfirm, just emits) -->
  <div v-if="isParseInFlight" class="doc-action-menu-item" @click.stop="emit('reparse')">
    <t-icon class="icon" name="refresh" />
    <span>{{ $t('knowledgeBase.rebuildDocument') }}</span>
  </div>

  <!-- 重建知识 (normal: with popconfirm) -->
  <t-popconfirm v-else theme="warning"
    :content="$t('knowledgeBase.rebuildConfirm', { fileName })"
    :confirm-btn="{ content: $t('common.confirm'), theme: 'primary' }"
    :cancel-btn="{ content: $t('common.cancel') }" placement="left"
    @confirm="emit('reparse')">
    <div class="doc-action-menu-item" @click.stop>
      <t-icon class="icon" name="refresh" />
      <span>{{ $t('knowledgeBase.rebuildDocument') }}</span>
    </div>
  </t-popconfirm>

  <!-- 取消解析 -->
  <t-popconfirm v-if="isParseInFlight" theme="warning"
    :content="$t('knowledgeBase.cancelParseConfirmBody', { title: fileName })"
    :confirm-btn="{ content: $t('knowledgeBase.cancelParse'), theme: 'danger' }"
    :cancel-btn="{ content: $t('common.cancel') }" placement="left"
    @confirm="emit('cancel-parse')">
    <div class="doc-action-menu-item danger" @click.stop>
      <t-icon class="icon" name="close-circle" />
      <span>{{ $t('knowledgeBase.cancelParse') }}</span>
    </div>
  </t-popconfirm>

  <!-- 移动到目录 -->
  <div v-if="canMutateKnowledge" class="doc-action-menu-item" @click.stop="emit('move-folder')">
    <t-icon class="icon" name="folder" />
    <span>{{ $t('knowledgeBase.moveToFolder.action') }}</span>
  </div>

  <!-- 移动到其他知识库 -->
  <div v-if="canMutateKnowledge" class="doc-action-menu-item" @click.stop="emit('move')">
    <t-icon class="icon" name="swap" />
    <span>{{ $t('knowledgeBase.moveDocument') }}</span>
  </div>

  <!-- 知识治理：提交审核 / 复审确认 / 标记过期 / 下架 / 归档（随 lifecycle_status 变化） -->
  <template v-if="canMutateKnowledge && governanceActions.length">
    <div class="doc-action-menu-divider" role="separator"></div>
    <div
      v-for="action in governanceActions"
      :key="action.id"
      class="doc-action-menu-item"
      :class="{ danger: action.danger }"
      @click.stop="emit('governance-action', action)"
    >
      <t-icon class="icon" :name="action.icon" />
      <span>{{ $t(action.labelKey) }}</span>
    </div>
  </template>

  <!-- 权限管理（文档级 ACL，deny 优先） -->
  <div v-if="canManageAcl" class="doc-action-menu-item" @click.stop="emit('acl-manage')">
    <t-icon class="icon" name="user-safety" />
    <span>{{ $t('knowledgeGovernance.aclMenuEntry') }}</span>
  </div>

  <!-- 分享链接（外链管理） -->
  <div v-if="canMutateKnowledge" class="doc-action-menu-item" @click.stop="emit('share-manage')">
    <t-icon class="icon" name="share" />
    <span>{{ $t('knowledgeGovernance.shareMenuEntry') }}</span>
  </div>

  <!-- 批量管理 -->
  <div v-if="canMutateKnowledge || canDownload" class="doc-action-menu-item" @click.stop="emit('batch-manage')">
    <t-icon class="icon" name="queue" />
    <span>{{ $t('menu.batchManage') }}</span>
  </div>

  <!-- 删除文档 -->
  <t-popconfirm theme="warning"
    :content="$t('knowledgeBase.confirmDeleteDocument', { fileName })"
    :confirm-btn="{ content: $t('knowledgeBase.confirmDelete'), theme: 'danger' }"
    :cancel-btn="{ content: $t('common.cancel') }" placement="left"
    @confirm="emit('delete')">
    <div class="doc-action-menu-item danger" @click.stop>
      <t-icon class="icon" name="delete" />
      <span>{{ $t('knowledgeBase.deleteDocument') }}</span>
    </div>
  </t-popconfirm>
</template>

<style scoped lang="less">
.doc-action-menu-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  font-family: var(--app-font-family);
  font-size: var(--app-text-base);
  font-weight: 400;
  line-height: 20px;
  color: var(--td-text-color-primary);
  cursor: pointer;
  border-radius: var(--app-radius-xs);
  transition: background-color var(--app-motion-fast) cubic-bezier(0.2, 0, 0, 1);

  &:hover {
    background: var(--td-bg-color-container-hover);
  }

  &:active {
    background: var(--td-bg-color-container-active);
  }

  .icon {
    font-size: var(--app-text-2xl);
    color: var(--td-text-color-secondary);
    transition: color var(--app-motion-fast) ease;
  }

  &:hover .icon {
    color: var(--td-text-color-primary);
  }

  &.danger {
    color: var(--td-error-color-6);
    margin-top: 4px;
    position: relative;

    &::before {
      content: '';
      position: absolute;
      top: -3px;
      left: 8px;
      right: 8px;
      height: 1px;
      background: var(--td-component-stroke);
    }

    .icon {
      color: var(--td-error-color-6);
    }

    &:hover {
      background: var(--td-error-color-1);
      color: var(--td-error-color-6);

      .icon {
        color: var(--td-error-color-6);
      }
    }

    &:active {
      background: var(--td-error-color-2);
    }
  }
}

/* 治理区块与常规动作之间的细分隔线 */
.doc-action-menu-divider {
  height: 1px;
  margin: 4px 8px;
  background: var(--td-component-stroke);
}
</style>
