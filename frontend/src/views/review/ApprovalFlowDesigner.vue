<template>
  <div class="flow-designer">
    <header class="flow-designer__header">
      <div class="flow-designer__heading">
        <h2 class="flow-designer__title">{{ t('approvalFlowDesigner.title') }}</h2>
        <p class="flow-designer__desc">{{ t('approvalFlowDesigner.description') }}</p>
      </div>
      <div class="flow-designer__actions">
        <t-button variant="outline" size="medium" :loading="loading" @click="loadFlow">
          <template #icon><t-icon name="refresh" /></template>
          {{ t('common.refresh') }}
        </t-button>
        <t-button theme="primary" size="medium" :loading="saving" :disabled="!kbId" @click="save">
          {{ t('approvalFlowDesigner.save') }}
        </t-button>
      </div>
    </header>

    <!-- 空间选择：审批流按空间配置，没有空间就没有可编辑的流程 -->
    <section class="flow-designer__section">
      <label class="flow-designer__label" for="flow-kb-select">
        {{ t('approvalFlowDesigner.space') }}
      </label>
      <t-select
        id="flow-kb-select"
        v-model="kbId"
        class="flow-designer__space"
        :options="spaceOptions"
        :placeholder="t('approvalFlowDesigner.spacePlaceholder')"
        :loading="spacesLoading"
        filterable
        @change="loadFlow"
      />
      <p v-if="loadFailed" class="flow-designer__error">{{ t('approvalFlowDesigner.loadFailed') }}</p>
    </section>

    <template v-if="kbId">
      <!-- 基本设置 -->
      <section class="flow-designer__section">
        <h3 class="flow-designer__sectionTitle">{{ t('approvalFlowDesigner.basics') }}</h3>
        <div class="flow-designer__grid">
          <label class="flow-designer__field">
            <span class="flow-designer__label">{{ t('approvalFlowDesigner.mode') }}</span>
            <t-radio-group v-model="form.mode" variant="default-filled">
              <t-radio-button value="single">{{ t('approvalFlowDesigner.modeSingle') }}</t-radio-button>
              <t-radio-button value="dual">{{ t('approvalFlowDesigner.modeDual') }}</t-radio-button>
            </t-radio-group>
            <span class="flow-designer__hint">{{ t('approvalFlowDesigner.modeHint') }}</span>
          </label>

          <label class="flow-designer__field">
            <span class="flow-designer__label">{{ t('approvalFlowDesigner.slaHours') }}</span>
            <t-input-number v-model="form.sla_hours" :min="1" :max="720" theme="normal" />
            <span class="flow-designer__hint">{{ t('approvalFlowDesigner.slaHint') }}</span>
          </label>

          <label class="flow-designer__field">
            <span class="flow-designer__label">{{ t('approvalFlowDesigner.reminderHours') }}</span>
            <t-input-number v-model="form.reminder_hours" :min="0" :max="720" theme="normal" />
            <span class="flow-designer__hint">{{ t('approvalFlowDesigner.reminderHint') }}</span>
          </label>

          <label class="flow-designer__field">
            <span class="flow-designer__label">{{ t('approvalFlowDesigner.escalateRole') }}</span>
            <t-select v-model="form.escalate_role" :options="roleOptions" />
            <span class="flow-designer__hint">{{ t('approvalFlowDesigner.escalateHint') }}</span>
          </label>

          <label class="flow-designer__field flow-designer__field--switch">
            <span class="flow-designer__label">{{ t('approvalFlowDesigner.enabled') }}</span>
            <t-switch v-model="form.enabled" />
            <span class="flow-designer__hint">{{ t('approvalFlowDesigner.enabledHint') }}</span>
          </label>
        </div>
      </section>

      <!-- 条件路由（§6.3 / §6.4）：只能抬高审批强度 -->
      <section class="flow-designer__section">
        <h3 class="flow-designer__sectionTitle">{{ t('approvalFlowDesigner.routing') }}</h3>
        <p class="flow-designer__sectionHint">{{ t('approvalFlowDesigner.routingHint') }}</p>
        <div class="flow-designer__grid">
          <label class="flow-designer__field">
            <span class="flow-designer__label">{{ t('approvalFlowDesigner.minRiskLevel') }}</span>
            <t-select v-model="rules.min_risk_level" :options="riskOptions" clearable
              :placeholder="t('approvalFlowDesigner.inherit')" />
          </label>

          <label class="flow-designer__field">
            <span class="flow-designer__label">{{ t('approvalFlowDesigner.forceDualAbove') }}</span>
            <t-select v-model="rules.force_dual_above" :options="riskOptions" clearable
              :placeholder="t('approvalFlowDesigner.inherit')" />
          </label>

          <label class="flow-designer__field">
            <span class="flow-designer__label">{{ t('approvalFlowDesigner.escalationHours') }}</span>
            <t-input-number v-model="rules.escalation_hours" :min="0" :max="720" theme="normal" />
            <span class="flow-designer__hint">{{ t('approvalFlowDesigner.escalationHint') }}</span>
          </label>

          <label class="flow-designer__field flow-designer__field--switch">
            <span class="flow-designer__label">{{ t('approvalFlowDesigner.distinctRoles') }}</span>
            <t-switch v-model="rules.require_distinct_reviewer_roles" />
            <span class="flow-designer__hint">{{ t('approvalFlowDesigner.distinctRolesHint') }}</span>
          </label>
        </div>
      </section>

      <!-- 审批链：逐级节点，每级或签/会签 -->
      <section class="flow-designer__section">
        <div class="flow-designer__sectionHeader">
          <h3 class="flow-designer__sectionTitle">{{ t('approvalFlowDesigner.chain') }}</h3>
          <t-button size="small" variant="outline" @click="addNode">
            <template #icon><t-icon name="add" /></template>
            {{ t('approvalFlowDesigner.addNode') }}
          </t-button>
        </div>
        <p class="flow-designer__sectionHint">{{ t('approvalFlowDesigner.chainHint') }}</p>

        <div v-if="form.nodes.length === 0" class="flow-designer__empty">
          <t-empty :description="t('approvalFlowDesigner.chainEmpty')" />
        </div>

        <ul v-else class="flow-designer__nodes">
          <li v-for="(node, index) in form.nodes" :key="node.key" class="flow-node">
            <div class="flow-node__order">{{ index + 1 }}</div>
            <div class="flow-node__body">
              <div class="flow-node__row">
                <t-input v-model="node.name" class="flow-node__name"
                  :placeholder="t('approvalFlowDesigner.nodeNamePlaceholder')" />
                <t-radio-group v-model="node.mode" variant="default-filled" size="small">
                  <t-radio-button value="or">{{ t('approvalFlowDesigner.nodeOr') }}</t-radio-button>
                  <t-radio-button value="and">{{ t('approvalFlowDesigner.nodeAnd') }}</t-radio-button>
                </t-radio-group>
                <t-radio-group v-model="node.assignee_kind" variant="default-filled" size="small">
                  <t-radio-button value="role">{{ t('approvalFlowDesigner.assigneeRole') }}</t-radio-button>
                  <t-radio-button value="users">{{ t('approvalFlowDesigner.assigneeUsers') }}</t-radio-button>
                </t-radio-group>
                <t-select v-if="node.assignee_kind === 'role'" v-model="node.assignee_role"
                  class="flow-node__role" :options="roleOptions" />
                <t-input v-else v-model="node.assignee_ids_text" class="flow-node__users"
                  :placeholder="t('approvalFlowDesigner.assigneeUsersPlaceholder')" />
                <t-button size="small" variant="text" theme="danger" @click="removeNode(index)">
                  <t-icon name="delete" />
                </t-button>
              </div>
            </div>
          </li>
        </ul>
      </section>

      <!-- 试算：改了配置会影响谁，先看再保存（§6.3 "规则可模拟"） -->
      <section class="flow-designer__section">
        <div class="flow-designer__sectionHeader">
          <h3 class="flow-designer__sectionTitle">{{ t('approvalFlowDesigner.simulate') }}</h3>
          <div class="flow-designer__simulateControls">
            <t-select v-model="simulateRisk" :options="riskOptions" class="flow-designer__risk" />
            <t-button size="small" variant="outline" :loading="simulating" @click="simulate">
              {{ t('approvalFlowDesigner.runSimulation') }}
            </t-button>
          </div>
        </div>
        <p class="flow-designer__sectionHint">{{ t('approvalFlowDesigner.simulateHint') }}</p>
        <div v-if="resolution" class="flow-designer__resolution">
          <dl>
            <div><dt>{{ t('approvalFlowDesigner.resolvedRisk') }}</dt><dd>{{ resolution.risk_level }}</dd></div>
            <div><dt>{{ t('approvalFlowDesigner.resolvedMode') }}</dt><dd>{{ resolution.mode }}</dd></div>
            <div><dt>{{ t('approvalFlowDesigner.resolvedQuorum') }}</dt><dd>{{ resolution.quorum }}</dd></div>
            <div><dt>{{ t('approvalFlowDesigner.resolvedVersion') }}</dt><dd>v{{ resolution.flow_version }}</dd></div>
            <div>
              <dt>{{ t('approvalFlowDesigner.resolvedDistinct') }}</dt>
              <dd>{{ resolution.require_distinct_reviewer_roles
                ? t('approvalFlowDesigner.yes') : t('approvalFlowDesigner.no') }}</dd>
            </div>
          </dl>
          <ul v-if="resolution.reasons && resolution.reasons.length" class="flow-designer__reasons">
            <li v-for="reason in resolution.reasons" :key="reason">{{ reason }}</li>
          </ul>
        </div>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * 审批流设计器（KnowAxis V3.0 §6.3 流程设计器 / 条件路由 / 规则模拟）。
 *
 * 后端 `/approval-flows` 与前端 API 客户端早已就绪，缺的只是调用它们的表单。
 * 三条与后端语义对齐的约束写在这里而不是靠 UI 猜：
 *  1. 节点顺序即审批顺序（seq = 下标 + 1），拖拽排序不引入额外的持久字段。
 *  2. 条件路由**只能抬高**审批强度：这里的 min_risk_level / force_dual_above
 *     是"最低要求"，服务层会与按内容算出的风险取并集，作者无法用低风险
 *     配置把内容降级（§6.4 硬规则）。UI 因此不提供"降低"选项。
 *  3. 试算读的是**已保存**的配置（服务端按流版本求值），所以试算前必须先
 *     保存；这里在试算按钮旁不做静默保存，避免"看到的和保存的不是一回事"。
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  getApprovalFlow,
  resolveApprovalFlow,
  saveApprovalFlow,
  type ApprovalResolution,
} from '@/api/governance-console'
import { listKnowledgeBases } from '@/api/knowledge-base'
import {
  applyFlow as hydrateFlow,
  defaultForm,
  defaultRules,
  findInvalidNode,
  toPayload as buildPayload,
  type DesignerForm,
  type DesignerRules,
  type EditableNode,
} from './approvalFlowPayload'

const { t } = useI18n()

const kbId = ref<string>('')
const spaces = ref<{ id: string; name: string }[]>([])
const spacesLoading = ref(false)
const loading = ref(false)
const saving = ref(false)
const simulating = ref(false)
const loadFailed = ref(false)

const form = reactive<DesignerForm>(defaultForm())
const rules = reactive<DesignerRules>(defaultRules())

const simulateRisk = ref<string>('medium')
const resolution = ref<ApprovalResolution | null>(null)

const spaceOptions = computed(() =>
  spaces.value.map(space => ({ label: space.name || space.id, value: space.id })))

const roleOptions = [
  { label: t('approvalFlowDesigner.roleAdmin'), value: 'admin' },
  { label: t('approvalFlowDesigner.roleContributor'), value: 'contributor' },
  { label: t('approvalFlowDesigner.roleViewer'), value: 'viewer' },
]

const riskOptions = [
  { label: t('approvalFlowDesigner.riskLow'), value: 'low' },
  { label: t('approvalFlowDesigner.riskMedium'), value: 'medium' },
  { label: t('approvalFlowDesigner.riskHigh'), value: 'high' },
]

let nodeSeq = 0
const nextKey = () => `node-${++nodeSeq}`

/** applyFlow hydrates the form from a server payload. */
function applyFlow(payload: Record<string, unknown>) {
  const hydrated = hydrateFlow(payload, nextKey)
  Object.assign(form, hydrated.form)
  Object.assign(rules, hydrated.rules)
}

/** loadSpaces populates the space selector. */
async function loadSpaces() {
  spacesLoading.value = true
  try {
    const res: any = await listKnowledgeBases()
    const rows = Array.isArray(res?.data) ? res.data : []
    spaces.value = rows
      .filter((row: any) => row && row.id)
      .map((row: any) => ({ id: String(row.id), name: String(row.name || '') }))
  } catch {
    spaces.value = []
  } finally {
    spacesLoading.value = false
  }
}

/** loadFlow reads the selected space's configuration. */
async function loadFlow() {
  if (!kbId.value) return
  loading.value = true
  loadFailed.value = false
  resolution.value = null
  try {
    const res: any = await getApprovalFlow(kbId.value)
    applyFlow((res?.data ?? {}) as Record<string, unknown>)
  } catch {
    // 404 表示该空间尚未配置：用默认表单，而不是把页面判成失败。
    // 这正是"新建配置"的常见入口，报错会让用户以为权限不够。
    loadFailed.value = false
    applyFlow({})
  } finally {
    loading.value = false
  }
}

/** toPayload builds the save body from the form. */
function toPayload() {
  return buildPayload(form, rules)
}

/** save persists the configuration; the server advances the flow version. */
async function save() {
  if (!kbId.value) return
  // 会签节点必须指定具体审批人：只有一个 role 的会签等于或签，
  // 保存成会签会让提交人以为需要多人通过而实际只需要一个角色。
  if (findInvalidNode(form)) {
    MessagePlugin.warning(t('approvalFlowDesigner.usersRequired'))
    return
  }
  saving.value = true
  try {
    const res: any = await saveApprovalFlow(kbId.value, toPayload())
    applyFlow((res?.data ?? {}) as Record<string, unknown>)
    MessagePlugin.success(t('approvalFlowDesigner.saved'))
    // 配置已变，之前的试算结果不再成立。
    resolution.value = null
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('approvalFlowDesigner.saveFailed'))
  } finally {
    saving.value = false
  }
}

/** simulate dry-runs the saved configuration for one risk level. */
async function simulate() {
  if (!kbId.value) return
  simulating.value = true
  try {
    const res: any = await resolveApprovalFlow(kbId.value, simulateRisk.value as any)
    resolution.value = (res?.data ?? null) as ApprovalResolution | null
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('approvalFlowDesigner.simulateFailed'))
  } finally {
    simulating.value = false
  }
}

/** addNode appends one approval level. */
function addNode() {
  form.nodes.push({
    key: nextKey(),
    name: '',
    mode: 'or',
    assignee_kind: 'role',
    assignee_role: 'admin',
    assignee_ids_text: '',
  })
}

/** removeNode deletes one level. */
function removeNode(index: number) {
  form.nodes.splice(index, 1)
}

onMounted(async () => {
  await loadSpaces()
  if (spaces.value.length === 1) {
    kbId.value = spaces.value[0].id
    await loadFlow()
  }
})
</script>

<style lang="less" scoped>
.flow-designer {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
  padding: var(--app-space-4);
  max-width: 1080px;

  &__header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--app-space-4);
  }

  &__title {
    margin: 0;
    font-size: var(--app-text-2xl);
    font-weight: 600;
  }

  &__desc,
  &__sectionHint {
    margin: 4px 0 0;
    color: var(--td-text-color-secondary);
    font-size: var(--app-text-xs);
  }

  &__actions {
    display: flex;
    gap: var(--app-space-2);
  }

  &__section {
    border: 1px solid var(--td-component-stroke);
    border-radius: var(--app-radius-lg);
    padding: var(--app-space-4);
    background: var(--td-bg-color-container);
  }

  &__sectionHeader {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--app-space-3);
  }

  &__sectionTitle {
    margin: 0;
    font-size: var(--app-text-md);
    font-weight: 600;
  }

  &__grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
    gap: var(--app-space-4);
    margin-top: var(--app-space-3);
  }

  &__field {
    display: flex;
    flex-direction: column;
    gap: 6px;

    &--switch {
      align-items: flex-start;
    }
  }

  &__label {
    font-size: var(--app-text-xs);
    color: var(--td-text-color-secondary);
  }

  &__hint {
    font-size: var(--app-text-2xs);
    color: var(--td-text-color-placeholder);
  }

  &__space {
    max-width: 360px;
    margin-top: 6px;
  }

  &__error {
    margin: 8px 0 0;
    color: var(--td-error-color);
    font-size: var(--app-text-xs);
  }

  &__nodes {
    list-style: none;
    margin: var(--app-space-3) 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--app-space-2);
  }

  &__empty {
    margin-top: var(--app-space-3);
  }

  &__resolution {
    margin-top: var(--app-space-3);
    border-top: 1px dashed var(--td-component-stroke);
    padding-top: var(--app-space-3);

    dl {
      display: flex;
      flex-wrap: wrap;
      gap: var(--app-space-4);
      margin: 0;
    }

    dt {
      font-size: var(--app-text-2xs);
      color: var(--td-text-color-secondary);
    }

    dd {
      margin: 2px 0 0;
      font-size: var(--app-text-sm);
      font-variant-numeric: tabular-nums;
    }
  }

  &__reasons {
    margin: var(--app-space-3) 0 0;
    padding-left: 18px;
    color: var(--td-text-color-secondary);
    font-size: var(--app-text-xs);
  }

  &__simulateControls {
    display: flex;
    align-items: center;
    gap: var(--app-space-2);
  }

  &__risk {
    width: 140px;
  }
}

.flow-node {
  display: flex;
  align-items: flex-start;
  gap: var(--app-space-3);
  padding: var(--app-space-3);
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-md);

  &__order {
    flex: 0 0 24px;
    height: 24px;
    border-radius: var(--app-radius-pill);
    background: var(--td-brand-color-light);
    color: var(--td-brand-color);
    font-size: var(--app-text-2xs);
    font-weight: 600;
    display: flex;
    align-items: center;
    justify-content: center;
  }

  &__body {
    flex: 1 1 auto;
    min-width: 0;
  }

  &__row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--app-space-2);
  }

  &__name {
    flex: 1 1 180px;
    min-width: 140px;
  }

  &__role {
    width: 140px;
  }

  &__users {
    flex: 1 1 220px;
    min-width: 180px;
  }
}
</style>
