import type { ApprovalFlowNodePayload, ApprovalFlowPayload } from '@/api/governance-console'

/**
 * 审批流设计器的纯逻辑（KnowAxis V3.0 §6.3）。
 *
 * 抽成独立模块而不是留在 .vue 里，是为了让"保存时到底发了什么"可被单测
 * 钉住。审批流的载荷错误后果很重：少发一个节点等于少一道审批，多发一个
 * 空角色节点会让流程卡死在无人可签。
 */

/** EditableNode is one level in the designer, before it becomes a payload. */
export interface EditableNode {
  /** key keeps the DOM stable when levels are added or removed. */
  key: string
  name: string
  mode: 'or' | 'and'
  assignee_kind: 'role' | 'users'
  assignee_role: string
  /** Comma-separated ids as typed by the operator. */
  assignee_ids_text: string
}

/** DesignerForm is the mutable state the view holds. */
export interface DesignerForm {
  mode: 'single' | 'dual'
  sla_hours: number
  reminder_hours: number
  escalate_role: string
  enabled: boolean
  nodes: EditableNode[]
}

/** DesignerRules is the §6.3/§6.4 routing block. */
export interface DesignerRules {
  min_risk_level: string
  force_dual_above: string
  escalation_hours: number
  require_distinct_reviewer_roles: boolean
}

/** defaultForm returns the configuration used for a space with none saved. */
export function defaultForm(): DesignerForm {
  return {
    mode: 'single',
    sla_hours: 72,
    reminder_hours: 24,
    escalate_role: 'admin',
    enabled: true,
    nodes: [],
  }
}

/** defaultRules returns an empty routing block ("inherit everything"). */
export function defaultRules(): DesignerRules {
  return {
    min_risk_level: '',
    force_dual_above: '',
    escalation_hours: 0,
    require_distinct_reviewer_roles: false,
  }
}

/** numberOr returns a finite number or the fallback. */
export function numberOr(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

/** splitIDs parses a comma-separated id list, dropping blanks and duplicates. */
export function splitIDs(raw: string): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const part of raw.split(',')) {
    const id = part.trim()
    if (!id || seen.has(id)) continue
    seen.add(id)
    out.push(id)
  }
  return out
}

/**
 * toPayload builds the save body.
 *
 * 三个刻意的取舍：
 *  - seq 按数组下标重算，不信任表单里残留的序号：删掉中间一级后序号会
 *    出现空洞，后端按 seq 排序会把那一级排到末尾。
 *  - 角色节点的 assignee_ids 一定发空数组，人员节点的 assignee_role 一定
 *    发空串。留着上一模式的残值会让后端拿到"既是角色又是人员"的节点。
 *  - 条件路由的空值**不发送**：发 "" 会被解析成"最低风险级为空"，与"不
 *    配置"在语义上不同，前者可能被当成配置了空约束。
 */
export function toPayload(form: DesignerForm, rules: DesignerRules): ApprovalFlowPayload {
  const nodes: ApprovalFlowNodePayload[] = form.nodes.map((node, index) => ({
    seq: index + 1,
    name: node.name.trim(),
    mode: node.mode,
    assignee_kind: node.assignee_kind,
    assignee_role: node.assignee_kind === 'role' ? node.assignee_role : '',
    assignee_ids: node.assignee_kind === 'users' ? splitIDs(node.assignee_ids_text) : [],
  }))

  const rulePayload: Record<string, unknown> = {}
  if (rules.min_risk_level) rulePayload.min_risk_level = rules.min_risk_level
  if (rules.force_dual_above) rulePayload.force_dual_above = rules.force_dual_above
  if (rules.escalation_hours > 0) rulePayload.escalation_hours = rules.escalation_hours
  if (rules.require_distinct_reviewer_roles) {
    rulePayload.require_distinct_reviewer_roles = true
  }

  return {
    mode: form.mode,
    sla_hours: form.sla_hours,
    reminder_hours: form.reminder_hours,
    escalate_role: form.escalate_role,
    enabled: form.enabled,
    rules: rulePayload,
    nodes,
  }
}

/**
 * findInvalidNode reports the first level that cannot be saved.
 *
 * 只有"指定人员"的节点可能无效：会签或或签都可能出现空人员列表，而一个
 * 没有审批人的节点会让流程卡在"无人可签"。角色节点恒有默认角色，不需要校验。
 */
export function findInvalidNode(form: DesignerForm): EditableNode | null {
  for (const node of form.nodes) {
    if (node.assignee_kind !== 'users') continue
    if (splitIDs(node.assignee_ids_text).length === 0) return node
  }
  return null
}

/** applyFlow hydrates the designer from a server payload. */
export function applyFlow(
  payload: Record<string, unknown> | null | undefined,
  nextKey: () => string,
): { form: DesignerForm; rules: DesignerRules } {
  const source = payload ?? {}
  const form = defaultForm()
  form.mode = source.mode === 'dual' ? 'dual' : 'single'
  form.sla_hours = numberOr(source.sla_hours, form.sla_hours)
  form.reminder_hours = numberOr(source.reminder_hours, form.reminder_hours)
  if (typeof source.escalate_role === 'string' && source.escalate_role) {
    form.escalate_role = source.escalate_role
  }
  form.enabled = source.enabled !== false

  const rawNodes = Array.isArray(source.nodes)
    ? (source.nodes as Record<string, unknown>[])
    : []
  form.nodes = rawNodes.map(raw => ({
    key: nextKey(),
    name: typeof raw.name === 'string' ? raw.name : '',
    mode: raw.mode === 'and' ? 'and' : 'or',
    assignee_kind: raw.assignee_kind === 'users' ? 'users' : 'role',
    assignee_role:
      typeof raw.assignee_role === 'string' && raw.assignee_role ? raw.assignee_role : 'admin',
    assignee_ids_text: Array.isArray(raw.assignee_ids)
      ? (raw.assignee_ids as unknown[]).filter(id => typeof id === 'string').join(', ')
      : '',
  }))

  const rules = defaultRules()
  const rawRules = (source.rules ?? {}) as Record<string, unknown>
  if (typeof rawRules.min_risk_level === 'string') rules.min_risk_level = rawRules.min_risk_level
  if (typeof rawRules.force_dual_above === 'string') rules.force_dual_above = rawRules.force_dual_above
  rules.escalation_hours = numberOr(rawRules.escalation_hours, 0)
  rules.require_distinct_reviewer_roles = rawRules.require_distinct_reviewer_roles === true

  return { form, rules }
}
