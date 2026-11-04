import assert from 'node:assert/strict'
import test from 'node:test'

import {
  applyFlow,
  defaultForm,
  defaultRules,
  findInvalidNode,
  numberOr,
  splitIDs,
  toPayload,
  type DesignerForm,
  type DesignerRules,
  type EditableNode,
} from './approvalFlowPayload'

/** node builds one editable level for the fixtures. */
function node(overrides: Partial<EditableNode> = {}): EditableNode {
  return {
    key: 'k',
    name: '',
    mode: 'or',
    assignee_kind: 'role',
    assignee_role: 'admin',
    assignee_ids_text: '',
    ...overrides,
  }
}

/** form builds a form with the given levels. */
function form(nodes: EditableNode[], overrides: Partial<DesignerForm> = {}): DesignerForm {
  return { ...defaultForm(), nodes, ...overrides }
}

test('seq is recomputed from position so a deleted level leaves no gap', () => {
  // A level was removed in the middle: the surviving nodes still carry the old
  // sequence numbers in the form, and the payload must renumber them.
  const nodes = [
    node({ key: 'a', name: '一级' }),
    node({ key: 'c', name: '三级' }),
  ]
  const payload = toPayload(form(nodes), defaultRules())
  assert.deepEqual(payload.nodes?.map(n => n.seq), [1, 2])
  assert.deepEqual(payload.nodes?.map(n => n.name), ['一级', '三级'])
})

test('a role node never carries assignee ids', () => {
  // The form still holds ids from when the operator had "named users" selected.
  const payload = toPayload(
    form([node({ assignee_kind: 'role', assignee_role: 'contributor', assignee_ids_text: 'u-1,u-2' })]),
    defaultRules(),
  )
  assert.deepEqual(payload.nodes?.[0].assignee_ids, [])
  assert.equal(payload.nodes?.[0].assignee_role, 'contributor')
})

test('a named-users node never carries a role', () => {
  const payload = toPayload(
    form([node({ assignee_kind: 'users', assignee_role: 'admin', assignee_ids_text: 'u-1' })]),
    defaultRules(),
  )
  assert.equal(payload.nodes?.[0].assignee_role, '')
  assert.deepEqual(payload.nodes?.[0].assignee_ids, ['u-1'])
})

test('named-user ids are trimmed, deduplicated and stripped of blanks', () => {
  assert.deepEqual(splitIDs(' u-1 , u-2 ,, u-1 ,  '), ['u-1', 'u-2'])
  assert.deepEqual(splitIDs(''), [])
  assert.deepEqual(splitIDs('   ,  ,'), [])
})

test('empty routing values are omitted rather than sent as empty strings', () => {
  // "" would read as "configured with an empty constraint", which is not the
  // same statement as "not configured".
  const payload = toPayload(form([]), defaultRules())
  assert.deepEqual(payload.rules, {})
})

test('routing values are sent only when set', () => {
  const rules: DesignerRules = {
    ...defaultRules(),
    min_risk_level: 'medium',
    force_dual_above: 'high',
    escalation_hours: 12,
    require_distinct_reviewer_roles: true,
  }
  const payload = toPayload(form([]), rules)
  assert.deepEqual(payload.rules, {
    min_risk_level: 'medium',
    force_dual_above: 'high',
    escalation_hours: 12,
    require_distinct_reviewer_roles: true,
  })
})

test('a zero escalation window is omitted, leaving the SLA default in charge', () => {
  const rules: DesignerRules = { ...defaultRules(), escalation_hours: 0 }
  assert.deepEqual(toPayload(form([]), rules).rules, {})
})

test('named-users levels with no ids are reported as invalid', () => {
  const invalid = node({ key: 'bad', assignee_kind: 'users', assignee_ids_text: '  ' })
  assert.equal(findInvalidNode(form([invalid]))?.key, 'bad')
  // A role-based level is never invalid: it always resolves through a role.
  assert.equal(findInvalidNode(form([node({ assignee_kind: 'role' })])), null)
  // Populated ids make the same level valid.
  assert.equal(
    findInvalidNode(form([node({ assignee_kind: 'users', assignee_ids_text: 'u-1' })])),
    null,
  )
})

test('the chain and the basic fields survive the payload round trip', () => {
  const payload = toPayload(
    form([node({ key: 'a', name: '主管审批', mode: 'and', assignee_kind: 'users', assignee_ids_text: 'u-1,u-2' })], {
      mode: 'dual',
      sla_hours: 48,
      reminder_hours: 8,
      escalate_role: 'contributor',
      enabled: false,
    }),
    defaultRules(),
  )
  assert.equal(payload.mode, 'dual')
  assert.equal(payload.sla_hours, 48)
  assert.equal(payload.reminder_hours, 8)
  assert.equal(payload.escalate_role, 'contributor')
  assert.equal(payload.enabled, false)
  assert.equal(payload.nodes?.[0].mode, 'and')
})

test('hydrating an empty payload yields the documented defaults', () => {
  let seq = 0
  const { form: hydrated, rules } = applyFlow(null, () => `k-${++seq}`)
  assert.equal(hydrated.mode, 'single')
  assert.equal(hydrated.sla_hours, 72)
  assert.equal(hydrated.reminder_hours, 24)
  assert.equal(hydrated.escalate_role, 'admin')
  assert.equal(hydrated.enabled, true, "an absent enabled flag means enabled")
  assert.deepEqual(hydrated.nodes, [])
  assert.deepEqual(rules, defaultRules())
})

test('hydrating restores levels, ids and routing', () => {
  let seq = 0
  const { form: hydrated, rules } = applyFlow(
    {
      mode: 'dual',
      sla_hours: 36,
      enabled: false,
      nodes: [
        { seq: 1, name: '初审', mode: 'or', assignee_kind: 'role', assignee_role: 'contributor' },
        { seq: 2, name: '终审', mode: 'and', assignee_kind: 'users', assignee_ids: ['u-1', 'u-2'] },
      ],
      rules: { min_risk_level: 'high', require_distinct_reviewer_roles: true },
    },
    () => `k-${++seq}`,
  )
  assert.equal(hydrated.mode, 'dual')
  assert.equal(hydrated.sla_hours, 36)
  assert.equal(hydrated.enabled, false)
  assert.equal(hydrated.nodes.length, 2)
  assert.equal(hydrated.nodes[1].assignee_ids_text, 'u-1, u-2')
  assert.equal(hydrated.nodes[1].mode, 'and')
  assert.equal(rules.min_risk_level, 'high')
  assert.equal(rules.require_distinct_reviewer_roles, true)
  // Keys are regenerated so the same payload can be hydrated twice safely.
  assert.notEqual(hydrated.nodes[0].key, hydrated.nodes[1].key)
})

test('unknown enum values fall back instead of producing an unsaveable node', () => {
  let seq = 0
  const { form: hydrated } = applyFlow(
    {
      mode: 'triple',
      nodes: [{ mode: 'maybe', assignee_kind: 'robot', assignee_role: '', assignee_ids: 'x' }],
    },
    () => `k-${++seq}`,
  )
  assert.equal(hydrated.mode, 'single')
  assert.equal(hydrated.nodes[0].mode, 'or')
  assert.equal(hydrated.nodes[0].assignee_kind, 'role')
  assert.equal(hydrated.nodes[0].assignee_role, 'admin')
  assert.equal(hydrated.nodes[0].assignee_ids_text, '')
})

test('non-numeric timings fall back to the defaults', () => {
  assert.equal(numberOr(undefined, 72), 72)
  assert.equal(numberOr('48', 72), 72, 'a string is not a number here')
  assert.equal(numberOr(Number.NaN, 24), 24)
  assert.equal(numberOr(0, 72), 0, 'zero is a real value')
})

test('the designer never offers a way to lower the approval level', () => {
  // 条件路由是"最低要求"：服务层与内容风险取并集。UI 侧的可选项因此只能是
  // 抬高审批强度的值，不存在"降到 low"这类设置。
  const payload = toPayload(form([], { mode: 'dual' }), {
    ...defaultRules(),
    min_risk_level: 'high',
    force_dual_above: 'medium',
  })
  assert.equal(payload.mode, 'dual')
  assert.equal(payload.rules?.min_risk_level, 'high')
  assert.equal(payload.rules?.force_dual_above, 'medium')
  // There is no field through which a caller could request "lower".
  assert.deepEqual(
    Object.keys(payload.rules ?? {}).sort(),
    ['force_dual_above', 'min_risk_level'],
  )
})
