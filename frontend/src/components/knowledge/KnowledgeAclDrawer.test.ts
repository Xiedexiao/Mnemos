import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import { createRenderer, h, nextTick } from 'vue'

// Render-level test for the per-document ACL drawer: grants land in the
// table, the deny-wins notice is visible, and the action/effect dropdowns
// expose the v1 option sets. Follows KnowledgeGovernanceBadges.test.ts:
// compile the SFC, run the CommonJS output with a require shim, mount with
// a minimal custom renderer and inspect the host tree.

const require = createRequire(import.meta.url)

// The real API module is loaded so maskShareToken-style helpers and the
// exported constants are genuine; tests stub the network functions on top.
const loadGovernanceApi = (stubs: Record<string, unknown> = {}) => {
  const source = readFileSync(
    new URL('../../api/knowledge-governance.ts', import.meta.url),
    'utf8',
  )
  const exports: any = {}
  runInNewContext(
    ts.transpileModule(source, {
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
    }).outputText,
    {
      exports,
      console,
      require(name: string) {
        if (name === '@/utils/request') {
          return { get: async () => ({}), post: async () => ({}), del: async () => ({}) }
        }
        throw new Error(`Unexpected import: ${name}`)
      },
    },
  )
  return { ...exports, ...stubs }
}

const { descriptor } = parse(
  readFileSync(new URL('./KnowledgeAclDrawer.vue', import.meta.url), 'utf8'),
)
const compiled = ts.transpileModule(compileScript(descriptor, {
  id: 'acl-drawer-test', inlineTemplate: true,
}).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText

interface HostNode { tag: string; props: Record<string, unknown>; children: HostNode[]; text?: string; parent?: HostNode }

const node = (tag: string): HostNode => ({ tag, props: {}, children: [] })
const renderer = createRenderer<HostNode, HostNode>({
  createElement: (tag: string) => node(tag),
  createText: (text: string) => ({ ...node('#text'), text }),
  createComment: () => node('#comment'),
  insert(child, parent) { child.parent = parent; parent.children.push(child) },
  remove(child) {
    const siblings = child.parent?.children
    if (siblings) siblings.splice(siblings.indexOf(child), 1)
  },
  setElementText(el, text) { el.text = text },
  setText(el, text) { el.text = text },
  patchProp(el, key, _prev, value) { el.props[key] = value },
  parentNode: el => el.parent || null,
  nextSibling: () => null,
})

const collect = (root: HostNode, out: HostNode[] = []): HostNode[] => {
  for (const child of root.children) {
    out.push(child)
    collect(child, out)
  }
  return out
}

const flush = async () => {
  await new Promise(resolve => setImmediate(resolve))
  await nextTick()
  await new Promise(resolve => setImmediate(resolve))
  await nextTick()
}

const GRANTS = [
  {
    id: 'acl-1', knowledge_id: 'k1', subject_type: 'user', subject_id: 'user-alice',
    action: 'ask', effect: 'deny', granted_by: 'boss', expires_at: null, created_at: '',
  },
  {
    id: 'acl-2', knowledge_id: 'k1', subject_type: 'user', subject_id: 'user-bob',
    action: 'view', effect: 'allow', granted_by: 'boss',
    expires_at: '2026-12-31T15:59:59.000Z', created_at: '',
  },
]

const mountDrawer = async (grants: unknown[] | Error) => {
  const api = loadGovernanceApi({
    listKnowledgeAcl: async () => {
      if (grants instanceof Error) throw grants
      return { success: true, data: grants }
    },
  })
  const exports: any = {}
  runInNewContext(compiled, {
    exports,
    console,
    require(name: string) {
      if (name === 'vue') return require('vue')
      if (name === 'vue-i18n') return { useI18n: () => ({ t: (key: string) => key }) }
      if (name === 'tdesign-vue-next') return { MessagePlugin: { success() {}, error() {} } }
      if (name === '@/api/knowledge-governance') return api
      throw new Error(`Unexpected import: ${name}`)
    },
  })
  const root = node('root')
  const app = renderer.createApp({
    setup: () => () => h(exports.default, { visible: true, knowledgeId: 'k1', knowledgeTitle: 'Doc' }),
  })
  app.mount(root)
  await flush()
  return { nodes: collect(root), close: () => app.unmount() }
}

test('grants returned by the API land in the table columns', async () => {
  const { nodes, close } = await mountDrawer(GRANTS)
  const table = nodes.find((n) => n.tag === 't-table')
  assert.ok(table, 'table rendered')
  assert.equal(table.props['row-key'], 'id')
  const data = table.props.data as Array<Record<string, unknown>>
  assert.equal(data.length, 2)
  assert.equal(data[0].subject_id, 'user-alice')
  assert.equal(data[1].effect, 'allow')
  const columns = table.props.columns as Array<{ colKey: string; title: string }>
  // Array.from copies across the vm realm so deepEqual compares plain arrays.
  assert.deepEqual(Array.from(columns, (c) => c.colKey), ['subject', 'action', 'effect', 'expires_at', 'op'])
  assert.ok(columns.every((c) => String(c.title).startsWith('knowledgeGovernance.aclColumn')), 'titles localized')
  close()
})

test('the deny-wins notice is rendered', async () => {
  const { nodes, close } = await mountDrawer([])
  const notice = nodes.find((n) => n.props.role === 'note')
  assert.ok(notice, 'notice rendered')
  // Single text children are set on the element itself (hostSetElementText),
  // so look for the text on any node, not only on #text nodes.
  const texts = collect(notice).map((n) => n.text).filter(Boolean)
  assert.ok(texts.includes('knowledgeGovernance.aclNotice'), JSON.stringify(texts))
  close()
})

test('empty grant list renders the empty state, not the table', async () => {
  const { nodes, close } = await mountDrawer([])
  assert.ok(!nodes.some((n) => n.tag === 't-table'), 'no table without grants')
  const empty = nodes.find((n) => n.tag === 't-empty')
  assert.ok(empty, 'empty state rendered')
  assert.equal(empty.props.description, 'knowledgeGovernance.aclEmpty')
  close()
})

test('a failed load surfaces the empty state (error toast via MessagePlugin)', async () => {
  const { nodes, close } = await mountDrawer(new Error('denied'))
  assert.ok(!nodes.some((n) => n.tag === 't-table'))
  assert.ok(nodes.some((n) => n.tag === 't-empty'))
  close()
})

test('the action dropdown offers the 7 v1 actions, the effect dropdown allow/deny', async () => {
  const { nodes, close } = await mountDrawer([])
  const selects = nodes.filter((n) => n.tag === 't-select')
  const optionSets = selects.map((s) => s.props.options as Array<{ label: string; value: string }>)
  const actionOptions = optionSets.find((o) => o.length === 7)
  assert.ok(actionOptions, 'action options rendered')
  assert.deepEqual(Array.from(actionOptions, (o) => o.value), [
    'view', 'ask', 'download', 'share', 'manage', 'review', 'export',
  ])
  assert.ok(actionOptions.every((o) => o.label.startsWith('knowledgeGovernance.aclAction_')), 'action labels localized')
  const effectOptions = optionSets.find((o) => o.length === 2)
  assert.ok(effectOptions, 'effect options rendered')
  assert.deepEqual(Array.from(effectOptions, (o) => o.value), ['allow', 'deny'])
  close()
})

test('the real API module still answers share-token helpers', () => {
  const api = loadGovernanceApi()
  assert.equal(api.maskShareToken('abcdefghijklmnop'), 'abcdef…')
  assert.equal(api.maskShareToken('short'), 'short')
  assert.equal(api.maskShareToken(''), '--')
  assert.equal(api.buildShareLinkUrl('tok123'), '/api/v1/public/share/tok123')
  assert.deepEqual([...api.KNOWLEDGE_ACL_ACTIONS], [
    'view', 'ask', 'download', 'share', 'manage', 'review', 'export',
  ])
})
