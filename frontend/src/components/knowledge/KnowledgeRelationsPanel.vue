<template>
  <section v-if="visible" class="kb-relations">
    <header class="kb-relations__head">
      <h4 class="kb-relations__title">{{ t('knowledgeRelations.title') }}</h4>
      <t-button v-if="!readOnly" vairant="text" variant="text" size="small" @click="openLink">
        {{ t('knowledgeRelations.add') }}
      </t-button>
    </header>

    <div v-if="loading" class="kb-relations__state">{{ t('common.loading') }}</div>
    <div v-else-if="views.length === 0" class="kb-relations__state">
      {{ t('knowledgeRelations.empty') }}
    </div>
    <ul v-else class="kb-relations__list">
      <li v-for="view in views" :key="view.relation?.id || view.peerId" class="kb-relations__item">
        <t-tag size="small" variant="light" :theme="relationMeta(view).theme">
          {{ relationMeta(view).label }}
        </t-tag>
        <button type="button" class="kb-relations__peer" :title="view.peerId" @click="$emit('open', view.peerId)">
          {{ view.peerId }}
        </button>
        <t-tag v-if="view.origin === 'ai_suggested'" size="small" variant="outline" theme="primary">
          {{ t('knowledgeRelations.aiSuggested') }}
        </t-tag>
        <span v-if="!view.outbound" class="kb-relations__direction">
          {{ t('knowledgeRelations.inbound') }}
        </span>
        <span v-if="view.note" class="kb-relations__note" :title="view.note">{{ view.note }}</span>
        <t-button v-if="!readOnly" theme="danger" variant="text" size="small"
          :loading="removingId === (view.relation?.id || '')" @click="remove(view)">
          {{ t('knowledgeRelations.remove') }}
        </t-button>
      </li>
    </ul>

    <t-dialog
      v-model:visible="linkVisible"
      :header="t('knowledgeRelations.add')"
      width="520px"
      :confirm-btn="{ content: t('common.confirm'), loading: saving }"
      :cancel-btn="{ content: t('common.cancel') }"
      @confirm="submitLink"
    >
      <t-form label-align="top">
        <t-form-item :label="t('knowledgeRelations.peer')">
          <t-input v-model="form.toKnowledgeId" :placeholder="t('knowledgeRelations.peerPlaceholder')" />
        </t-form-item>
        <t-form-item :label="t('knowledgeRelations.type')">
          <t-select v-model="form.relationType" :options="typeOptions" />
        </t-form-item>
        <t-form-item :label="t('knowledgeRelations.note')">
          <t-textarea v-model="form.note" :autosize="{ minRows: 2, maxRows: 4 }" :maxlength="512" />
        </t-form-item>
      </t-form>
    </t-dialog>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import { MessagePlugin } from 'tdesign-vue-next';
import {
  KNOWLEDGE_RELATION_TYPES,
  linkKnowledgeRelation,
  listKnowledgeRelations,
  unlinkKnowledgeRelation,
  type KnowledgeRelationType,
  type KnowledgeRelationView,
} from '@/api/governance-console';

/**
 * 知识关联面板（KnowAxis V3.0 §4.5）。
 *
 * 阅读页此前只展示正文与治理徽章：文档之间的关系（依据/替代/补充/冲突/
 * 前置/责任）无处呈现，也无法从"这份 SOP 依据哪条制度"跳过去。面板把关联
 * 与反向引用（谁引用了当前页面）一起列出，AI 推荐的关联单独标注。
 */

const props = defineProps<{
  knowledgeId: string;
  /** readOnly hides the add/remove affordances (viewer or archived document). */
  readOnly?: boolean;
}>();

const emit = defineEmits<{ open: [knowledgeId: string] }>();

const { t } = useI18n();

const views = ref<KnowledgeRelationView[]>([]);
const loading = ref(false);
const saving = ref(false);
const removingId = ref('');
const linkVisible = ref(false);
const form = ref<{ toKnowledgeId: string; relationType: KnowledgeRelationType; note: string }>({
  toKnowledgeId: '',
  relationType: 'based_on',
  note: '',
});

const visible = computed(() => !!props.knowledgeId);

const typeOptions = computed(() =>
  KNOWLEDGE_RELATION_TYPES.map((value) => ({
    label: t(`knowledgeRelations.type_${value}`),
    value,
  })),
);

type TagTheme = 'default' | 'success' | 'warning' | 'danger' | 'primary';

const relationMeta = (view: KnowledgeRelationView): { label: string; theme: TagTheme } => {
  const key = view.relationType || 'related';
  const theme: TagTheme =
    key === 'conflicts' ? 'danger'
      : key === 'replaces' ? 'warning'
        : key === 'based_on' ? 'primary'
          : 'default';
  return { label: t(`knowledgeRelations.type_${key}`), theme };
};

const load = async () => {
  if (!props.knowledgeId) {
    views.value = [];
    return;
  }
  loading.value = true;
  try {
    const response = await listKnowledgeRelations(props.knowledgeId);
    views.value = response?.data || [];
  } catch {
    // 关联是阅读页的辅助信息：加载失败不打断阅读，静默显示为空。
    views.value = [];
  } finally {
    loading.value = false;
  }
};

const openLink = () => {
  form.value = { toKnowledgeId: '', relationType: 'based_on', note: '' };
  linkVisible.value = true;
};

const submitLink = async () => {
  const peer = form.value.toKnowledgeId.trim();
  if (!peer) {
    MessagePlugin.warning(t('knowledgeRelations.peerRequired'));
    return;
  }
  saving.value = true;
  try {
    await linkKnowledgeRelation(props.knowledgeId, {
      to_knowledge_id: peer,
      relation_type: form.value.relationType,
      note: form.value.note.trim(),
    });
    linkVisible.value = false;
    await load();
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('knowledgeRelations.saveFailed'));
  } finally {
    saving.value = false;
  }
};

const remove = async (view: KnowledgeRelationView) => {
  removingId.value = view.relation?.id || '';
  try {
    await unlinkKnowledgeRelation(props.knowledgeId, view.peerId, view.relationType);
    await load();
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('knowledgeRelations.saveFailed'));
  } finally {
    removingId.value = '';
  }
};

watch(() => props.knowledgeId, load, { immediate: true });
</script>

<style scoped>
.kb-relations {
  margin-top: 24px;
  padding-top: 16px;
  border-top: 1px solid var(--td-component-stroke);
}

.kb-relations__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}

.kb-relations__title {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
}

.kb-relations__state {
  color: var(--td-text-color-placeholder);
  font-size: 13px;
  padding: 8px 0;
}

.kb-relations__list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.kb-relations__item {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.kb-relations__peer {
  background: none;
  border: none;
  padding: 0;
  color: var(--td-brand-color);
  cursor: pointer;
  font: inherit;
  text-align: left;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 320px;
}

.kb-relations__peer:hover {
  text-decoration: underline;
}

.kb-relations__direction {
  font-size: 12px;
  color: var(--td-text-color-placeholder);
}

.kb-relations__note {
  font-size: 12px;
  color: var(--td-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 360px;
}
</style>
