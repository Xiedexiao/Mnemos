import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import { createRequire } from 'node:module'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
import { createRenderer, h, nextTick } from 'vue'

// Render-level test for the governance badge mapping: which lifecycle /
// classification values produce a badge, with which tone class, and which
// stay invisible (valid lifecycle, internal classification, unknown values).

const require = createRequire(import.meta.url)
const { descriptor } = parse(
  readFileSync(new URL('./KnowledgeGovernanceBadges.vue', import.meta.url), 'utf8'),
)
const compiled = ts.transpileModule(compileScript(descriptor, {
  id: 'governance-badges-test', inlineTemplate: true,
}).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText

interface VNode { tag: string; props: Record<string, unknown>; children: VNode[]; text?: string; parent?: VNode }

const node = (tag: string): VNode => ({ tag, props: {}, children: [] })
const renderer = createRenderer<VNode, VNode>({
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

interface Badge { className: string; text: string }

const collect = (root: VNode, out: VNode[] = []): VNode[] => {
  for (const child of root.children) {
    out.push(child)
    collect(child, out)
  }
  return out
}

const renderBadges = (props: { lifecycleStatus?: string; classification?: string }) => {
  const exports: any = {}
  runInNewContext(compiled, {
    exports,
    console,
    require(name: string) {
      if (name === 'vue') return require('vue')
      if (name === 'vue-i18n') return { useI18n: () => ({ t: (key: string) => key }) }
      throw new Error(`Unexpected import: ${name}`)
    },
  })
  const root = node('root')
  const app = renderer.createApp({ setup: () => () => h(exports.default, props) })
  app.mount(root)
  const badges = collect(root)
    .filter(el => typeof el.props.class === 'string' && el.props.class.includes('kb-gov-badge'))
    .map(el => ({ className: el.props.class as string, text: (el.text || '') as string }))
  app.unmount()
  return badges
}

const flush = async () => { await new Promise(resolve => setImmediate(resolve)); await nextTick() }

test('valid lifecycle and internal classification stay invisible', async () => {
  await flush()
  for (const props of [
    {},
    { lifecycleStatus: 'valid' },
    { classification: 'internal' },
    { lifecycleStatus: 'valid', classification: 'internal' },
  ]) {
    assert.deepEqual(renderBadges(props), [], JSON.stringify(props))
  }
})

test('lifecycle tones follow the governance colour rules', async () => {
  const cases: Array<[string, string]> = [
    ['draft', 'kb-gov-badge--muted'],
    ['pending_review', 'kb-gov-badge--info'],
    ['pending_effective', 'kb-gov-badge--info'],
    ['pending_review_cycle', 'kb-gov-badge--pending'],
    ['stale', 'kb-gov-badge--warning'],
    ['expired', 'kb-gov-badge--muted'],
    ['replaced', 'kb-gov-badge--muted'],
    ['delisted', 'kb-gov-badge--muted'],
    ['archived', 'kb-gov-badge--muted'],
    ['conflict', 'kb-gov-badge--danger'],
  ]
  for (const [status, tone] of cases) {
    const badges = renderBadges({ lifecycleStatus: status })
    assert.equal(badges.length, 1, status)
    assert.ok(badges[0].className.includes(tone), `${status} -> ${badges[0].className}`)
    assert.ok(badges[0].text.startsWith('knowledgeGovernance.lifecycle'), status)
  }
})

test('classification tones: public green, secret orange, confidential red', async () => {
  const cases: Array<[string, string]> = [
    ['public', 'kb-gov-badge--success'],
    ['secret', 'kb-gov-badge--warning'],
    ['confidential', 'kb-gov-badge--danger'],
  ]
  for (const [level, tone] of cases) {
    const badges = renderBadges({ classification: level })
    assert.equal(badges.length, 1, level)
    assert.ok(badges[0].className.includes(tone), `${level} -> ${badges[0].className}`)
    assert.ok(badges[0].text.startsWith('knowledgeGovernance.classification'), level)
  }
})

test('unknown values render nothing and matching is case-insensitive', async () => {
  assert.deepEqual(renderBadges({ lifecycleStatus: 'something_new' }), [])
  assert.deepEqual(renderBadges({ classification: 'top_secret' }), [])
  assert.deepEqual(renderBadges({ lifecycleStatus: 'VALID', classification: 'INTERNAL' }), [])
})

test('lifecycle and classification badges render together', async () => {
  const badges = renderBadges({ lifecycleStatus: 'stale', classification: 'secret' })
  assert.equal(badges.length, 2)
  assert.ok(badges[0].className.includes('kb-gov-badge--warning'))
  assert.ok(badges[1].className.includes('kb-gov-badge--warning'))
})
