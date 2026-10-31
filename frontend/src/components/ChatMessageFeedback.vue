<template>
  <span v-if="messageId" class="chat-message-feedback" @click.stop>
    <!-- 有用：直接提交；已提交后保持高亮，再次点击会覆盖原评价 -->
    <t-button size="small" variant="outline" shape="round" class="chat-feedback-btn"
      :class="{ 'is-active': submitted === 'useful' }" :disabled="submitting"
      :title="t('chat.feedback.useful')" :aria-label="t('chat.feedback.useful')"
      @click="submitUseful">
      <svg class="chat-action-icon" width="18" height="18" viewBox="0 0 24 24" fill="none"
        stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round"
        aria-hidden="true" focusable="false">
        <path d="M14 9V5a3 3 0 0 0-3-3l-4 9v11h11.28a2 2 0 0 0 2-1.7l1.38-9a2 2 0 0 0-2-2.3zM7 22H4a2 2 0 0 1-2-2v-7a2 2 0 0 1 2-2h3" />
      </svg>
    </t-button>
    <!-- 没用：弹出原因面板（多选 + 可选备注）后再提交 -->
    <t-popup v-model:visible="panelVisible" trigger="click" placement="top" :show-arrow="true"
      destroy-on-close overlay-class-name="chat-feedback-popup" :overlay-inner-style="{ padding: 0 }">
      <t-button size="small" variant="outline" shape="round" class="chat-feedback-btn"
        :class="{ 'is-active': submitted === 'useless' }" :disabled="submitting"
        :title="t('chat.feedback.useless')" :aria-label="t('chat.feedback.useless')">
        <svg class="chat-action-icon" width="18" height="18" viewBox="0 0 24 24" fill="none"
          stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round"
          aria-hidden="true" focusable="false">
          <path d="M10 15v4a3 3 0 0 0 3 3l4-9V2H5.72a2 2 0 0 0-2 1.7l-1.38 9a2 2 0 0 0 2 2.3zm7-13h2.67A2.31 2.31 0 0 1 22 4v7a2.31 2.31 0 0 1-2.33 2H17" />
        </svg>
      </t-button>
      <template #content>
        <div class="chat-feedback-panel">
          <div class="chat-feedback-panel-title">{{ t('chat.feedback.panelTitle') }}</div>
          <div class="chat-feedback-reasons">
            <t-checkbox v-for="reason in REASON_OPTIONS" :key="reason.code" class="chat-feedback-reason"
              :checked="selectedReasons.has(reason.code)"
              :label="t(reason.labelKey)"
              @change="(checked: boolean) => toggleReason(reason.code, checked)" />
          </div>
          <t-textarea v-model="comment" class="chat-feedback-comment"
            :placeholder="t('chat.feedback.commentPlaceholder')" :maxlength="500" />
          <div class="chat-feedback-panel-actions">
            <t-button size="small" variant="outline" :disabled="submitting" @click="panelVisible = false">
              {{ t('common.cancel') }}
            </t-button>
            <t-button size="small" theme="primary" :loading="submitting" @click="submitUseless">
              {{ t('chat.feedback.submit') }}
            </t-button>
          </div>
        </div>
      </template>
    </t-popup>
  </span>
</template>

<script setup lang="ts">
import { ref } from 'vue';
import { useI18n } from 'vue-i18n';
import { MessagePlugin } from 'tdesign-vue-next';
import {
  submitMessageFeedback,
  MESSAGE_FEEDBACK_REASON_CODES,
  type MessageFeedbackRating,
  type MessageFeedbackReasonCode,
} from '@/api/chat-history';

const props = defineProps<{
  /** Persisted assistant message id this rating is attached to. */
  messageId: string;
}>();

const emit = defineEmits<{
  (e: 'submitted', rating: MessageFeedbackRating): void;
}>();

const { t } = useI18n();

const REASON_OPTIONS: { code: MessageFeedbackReasonCode; labelKey: string }[] = [
  { code: 'fact_error', labelKey: 'chat.feedback.reasonFactError' },
  { code: 'outdated', labelKey: 'chat.feedback.reasonOutdated' },
  { code: 'incomplete', labelKey: 'chat.feedback.reasonIncomplete' },
  { code: 'off_topic', labelKey: 'chat.feedback.reasonOffTopic' },
  { code: 'other', labelKey: 'chat.feedback.reasonOther' },
];

const panelVisible = ref(false);
const submitting = ref(false);
const submitted = ref<MessageFeedbackRating | null>(null);
const selectedReasons = ref<Set<MessageFeedbackReasonCode>>(new Set());
const comment = ref('');

const toggleReason = (code: MessageFeedbackReasonCode, checked: boolean) => {
  const next = new Set(selectedReasons.value);
  if (checked) {
    next.add(code);
  } else {
    next.delete(code);
  }
  selectedReasons.value = next;
};

const send = async (payload: { rating: MessageFeedbackRating; reason_codes?: string[]; comment?: string }) => {
  submitting.value = true;
  try {
    await submitMessageFeedback(props.messageId, payload);
    submitted.value = payload.rating;
    emit('submitted', payload.rating);
    MessagePlugin.success(t('chat.feedback.submitted'));
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('chat.feedback.failed'));
  } finally {
    submitting.value = false;
  }
};

const submitUseful = () => {
  if (submitting.value) return;
  void send({ rating: 'useful' });
};

const submitUseless = () => {
  if (submitting.value) return;
  const reasons = [...selectedReasons.value];
  const trimmed = comment.value.trim();
  void send({
    rating: 'useless',
    ...(reasons.length ? { reason_codes: reasons } : {}),
    ...(trimmed ? { comment: trimmed } : {}),
  });
  panelVisible.value = false;
};
</script>

<style scoped lang="less">
.chat-message-feedback {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.chat-feedback-btn {
  color: var(--td-text-color-secondary);

  &:hover:not(:disabled) {
    color: var(--td-text-color-primary);
  }

  &.is-active {
    color: var(--td-brand-color);
    border-color: var(--td-brand-color);
  }
}
</style>

<style lang="less">
.chat-feedback-popup {
  .t-popup__content {
    padding: 0;
    border-radius: var(--app-radius-md);
    box-shadow: var(--td-shadow-2);
  }
}
</style>

<style scoped lang="less">
.chat-feedback-panel {
  display: flex;
  flex-direction: column;
  gap: 10px;
  width: 280px;
  padding: 12px;
  font-size: var(--app-text-sm);
  color: var(--td-text-color-primary);
}

.chat-feedback-panel-title {
  font-weight: 600;
}

.chat-feedback-reasons {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.chat-feedback-comment :deep(.t-textarea__inner) {
  font-size: var(--app-text-sm);
}

.chat-feedback-panel-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
