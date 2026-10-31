import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import { createRenderer, h, nextTick } from 'vue'

// Render-level test for the external share-link drawer: links land in the
// table, the credential-notice is visible, and the create form exposes the
// v1 option sets (org/people, view/download, 7/30/90/custom). Follows
// KnowledgeGovernanceBadges.test.ts's compileScript + custom-renderer setup.

const require = createRequire(import.meta.url)

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
  readFileSync(new URL('./KnowledgeShareDrawer.vue', import.meta.url), 'utf8'),
)
const compiled = ts.transpileModule(compileScript(descriptor, {
  id: 'share-drawer-test', inlineTemplate: true,
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

const LINKS = [
  {
    id: 'link-1', knowledge_id: 'k1', token: 'abcdefghijklmnop', link_type: 'org',
    permission: 'view', expires_at: null, revoked_at: null, visit_count: 3,
  },
  {
    id: 'link-2', knowledge_id: 'k1', token: 'qrstuvwxyz012345', link_type: 'people',
    permission: 'download', expires_at: '2020-01-01T00:00:00.000Z', revoked_at: null, visit_count: 9,
  },
  {
    id: 'link-3', knowledge_id: 'k1', token: 'revoked-token-42', link_type: 'org',
    permission: 'view', expires_at: null, revoked_at: '2026-01-01T00:00:00.000Z', visit_count: 1,
  },
]

const mountDrawer = async (links: unknown[] | Error) => {
  const api = loadGovernanceApi({
    listKnowledgeShareLinks: async () => {
      if (links instanceof Error) throw links
      return { success: true, data: links }
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
      if (name === '@/utils/clipboard') return { copyWithToast: async () => true }
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

test('share links land in the table with the v1 column set', async () => {
  const { nodes, close } = await mountDrawer(LINKS)
  const table = nodes.find((n) => n.tag === 't-table')
  assert.ok(table, 'table rendered')
  const data = table.props.data as Array<Record<string, unknown>>
  assert.equal(data.length, 3)
  assert.equal(data[0].token, 'abcdefghijklmnop')
  const columns = table.props.columns as Array<{ colKey: string; title: string }>
  // Array.from copies across the vm realm so deepEqual compares plain arrays.
  assert.deepEqual(Array.from(columns, (c) => c.colKey), [
    'token', 'link_type', 'permission', 'expires_at', 'visit_count', 'status', 'op',
  ])
  assert.ok(columns.every((c) => String(c.title).startsWith('knowledgeGovernance.shareColumn')), 'titles localized')
  close()
})

test('the link-is-a-credential notice is rendered', async () => {
  const { nodes, close } = await mountDrawer([])
  const notice = nodes.find((n) => n.props.role === 'note')
  assert.ok(notice, 'notice rendered')
  // Single text children are set on the element itself (hostSetElementText),
  // so look for the text on any node, not only on #text nodes.
  const texts = collect(notice).map((n) => n.text).filter(Boolean)
  assert.ok(texts.includes('knowledgeGovernance.shareNotice'), JSON.stringify(texts))
  close()
})

test('empty list renders the empty state', async () => {
  const { nodes, close } = await mountDrawer([])
  assert.ok(!nodes.some((n) => n.tag === 't-table'))
  const empty = nodes.find((n) => n.tag === 't-empty')
  assert.ok(empty)
  assert.equal(empty.props.description, 'knowledgeGovernance.shareEmpty')
  close()
})

test('the create form exposes org/people, view/download and 7/30/90/custom', async () => {
  const { nodes, close } = await mountDrawer([])
  const selects = nodes.filter((n) => n.tag === 't-select')
  const optionSets = selects.map((s) => s.props.options as Array<{ label: string; value: unknown }>)
  const linkTypeOptions = optionSets.find((o) => o.length === 2)
  assert.ok(linkTypeOptions, 'link type options rendered')
  assert.deepEqual(Array.from(linkTypeOptions, (o) => o.value), ['org', 'people'])
  const permissionOptions = optionSets.find((o) => o.map((x) => x.value).join() === 'view,download')
  assert.ok(permissionOptions, 'permission options rendered')
  const validityOptions = optionSets.find((o) => o.length === 4)
  assert.ok(validityOptions, 'validity options rendered')
  assert.deepEqual(Array.from(validityOptions, (o) => o.value), [7, 30, 90, 'custom'])
  close()
})

test('token masking keeps only the first 6 characters', () => {
  const api = loadGovernanceApi()
  assert.equal(api.maskShareToken('abcdefghijklmnop'), 'abcdef…')
  assert.equal(api.maskShareToken('abc'), 'abc')
  assert.equal(api.maskShareToken(null), '--')
})

test('buildShareLinkUrl composes the public resolve URL', () => {
  const api = loadGovernanceApi()
  assert.equal(
    api.buildShareLinkUrl('tok123'),
    '/api/v1/public/share/tok123',
  )
})
