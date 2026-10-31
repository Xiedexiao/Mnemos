<template>
  <span v-if="lifecycleMeta || classificationMeta" class="kb-governance-badges">
    <span v-if="lifecycleMeta" class="kb-gov-badge" :class="`kb-gov-badge--${lifecycleMeta.tone}`"
      :title="t(lifecycleMeta.labelKey)">{{ t(lifecycleMeta.labelKey) }}</span>
    <span v-if="classificationMeta" class="kb-gov-badge" :class="`kb-gov-badge--${classificationMeta.tone}`"
      :title="t(classificationMeta.labelKey)">{{ t(classificationMeta.labelKey) }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import { useI18n } from 'vue-i18n';

/**
 * Governance badges for one knowledge entry: lifecycle state + security
 * classification. Both props are optional strings straight from the backend
 * JSON (`lifecycle_status`, `classification`); unknown/empty values and the
 * "quiet" defaults (`valid` lifecycle, `internal` classification) render
 * nothing so normal rows stay visually unchanged.
 */
const props = defineProps<{
  lifecycleStatus?: string | null;
  classification?: string | null;
}>();

const { t } = useI18n();

type BadgeTone = 'muted' | 'info' | 'pending' | 'warning' | 'danger' | 'success';

interface BadgeMeta {
  labelKey: string;
  tone: BadgeTone;
}

// Lifecycle → badge. `valid` is the healthy default and stays invisible
// (弱显示) so the badge only appears when governance needs attention.
const LIFECYCLE_BADGES: Record<string, BadgeMeta> = {
  draft: { labelKey: 'knowledgeGovernance.lifecycleDraft', tone: 'muted' },
  pending_review: { labelKey: 'knowledgeGovernance.lifecyclePendingReview', tone: 'info' },
  pending_effective: { labelKey: 'knowledgeGovernance.lifecyclePendingEffective', tone: 'info' },
  pending_review_cycle: { labelKey: 'knowledgeGovernance.lifecyclePendingReviewCycle', tone: 'pending' },
  stale: { labelKey: 'knowledgeGovernance.lifecycleStale', tone: 'warning' },
  expired: { labelKey: 'knowledgeGovernance.lifecycleExpired', tone: 'muted' },
  replaced: { labelKey: 'knowledgeGovernance.lifecycleReplaced', tone: 'muted' },
  delisted: { labelKey: 'knowledgeGovernance.lifecycleDelisted', tone: 'muted' },
  archived: { labelKey: 'knowledgeGovernance.lifecycleArchived', tone: 'muted' },
  conflict: { labelKey: 'knowledgeGovernance.lifecycleConflict', tone: 'danger' },
};

// Classification → badge. `internal` is the working default and stays hidden;
// only levels that restrict (or deliberately widen) visibility get a badge.
const CLASSIFICATION_BADGES: Record<string, BadgeMeta> = {
  public: { labelKey: 'knowledgeGovernance.classificationPublic', tone: 'success' },
  secret: { labelKey: 'knowledgeGovernance.classificationSecret', tone: 'warning' },
  confidential: { labelKey: 'knowledgeGovernance.classificationConfidential', tone: 'danger' },
};

const normalize = (value?: string | null) => (value || '').trim().toLowerCase();

const lifecycleMeta = computed<BadgeMeta | null>(() => {
  const status = normalize(props.lifecycleStatus);
  if (!status || status === 'valid') return null;
  return LIFECYCLE_BADGES[status] || null;
});

const classificationMeta = computed<BadgeMeta | null>(() => {
  const level = normalize(props.classification);
  if (!level || level === 'internal') return null;
  return CLASSIFICATION_BADGES[level] || null;
});
</script>

<style scoped lang="less">
.kb-governance-badges {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  flex: 0 0 auto;
  max-width: 100%;
}

.kb-gov-badge {
  display: inline-flex;
  align-items: center;
  padding: 0 6px;
  border-radius: 4px;
  border: 1px solid transparent;
  font-size: var(--app-text-xs);
  line-height: 18px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 88px;
}

.kb-gov-badge--muted {
  color: var(--td-text-color-secondary);
  border-color: var(--td-component-stroke);
  background: var(--td-bg-color-secondarycontainer);
}

.kb-gov-badge--info {
  color: var(--td-brand-color);
  border-color: color-mix(in srgb, var(--td-brand-color) 30%, transparent);
  background: color-mix(in srgb, var(--td-brand-color) 10%, transparent);
}

.kb-gov-badge--pending {
  color: var(--app-source-highlight);
  border-color: color-mix(in srgb, var(--app-source-highlight) 35%, transparent);
  background: color-mix(in srgb, var(--app-source-highlight) 12%, transparent);
}

.kb-gov-badge--warning {
  color: var(--td-warning-color);
  border-color: color-mix(in srgb, var(--td-warning-color) 35%, transparent);
  background: color-mix(in srgb, var(--td-warning-color) 10%, transparent);
}

.kb-gov-badge--danger {
  color: var(--td-error-color);
  border-color: color-mix(in srgb, var(--td-error-color) 35%, transparent);
  background: color-mix(in srgb, var(--td-error-color) 10%, transparent);
}

.kb-gov-badge--success {
  color: var(--td-success-color);
  border-color: color-mix(in srgb, var(--td-success-color) 35%, transparent);
  background: color-mix(in srgb, var(--td-success-color) 10%, transparent);
}
</style>
