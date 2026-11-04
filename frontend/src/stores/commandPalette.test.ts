import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test, { type TestContext } from 'node:test'
import * as vue from 'vue'
import * as pinia from 'pinia'
import { fixture, flush } from '../test-utils/vueFixture.ts'

// 跨 vm 上下文创建的数组与当前 realm 的数组"结构相同但不是同一个原型"，
// assert.deepEqual 会因此失败。比较前统一转成本 realm 的数组。
function list(value: unknown): unknown[] {
  return Array.from((value ?? []) as ArrayLike<unknown>)
}

// commandPalette 的 store 单测：验证服务端建议已经成为真相，localStorage
// 退化为离线缓存（§12.1）。鉴权 store 用桩代替，只暴露 user / tenant。
function setup(
  t: TestContext,
  api: {
    fetchSearchSuggestions?: (prefix: string, options?: any) => Promise<any>
    clearSearchHistory?: () => Promise<void>
  } = {},
  storage = new Map<string, string>(),
) {
  const f = fixture(t, {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => { storage.set(key, value) },
    removeItem: (key: string) => { storage.delete(key) },
  })
  const suggestionApi = {
    emptySuggestionBundle: () => ({
      history: [], hot: [], titles: [], glossary_terms: [], prefix: '', hot_window_days: 0,
    }),
    fetchSearchSuggestions: api.fetchSearchSuggestions ?? (async () => ({
      history: [], hot: [], titles: [], glossary_terms: [], prefix: '', hot_window_days: 30,
    })),
    clearSearchHistory: api.clearSearchHistory ?? (async () => {}),
  }
  const { useCommandPaletteStore } = f.evaluate(
    readFileSync(new URL('./commandPalette.ts', import.meta.url), 'utf8'),
    {
      vue,
      pinia,
      '@/api/search-suggestion': suggestionApi,
      './auth': {
        useAuthStore: () => ({
          user: { id: 'user-1' },
          effectiveTenantId: 7,
        }),
      },
    },
  )
  const root = pinia.createPinia()
  pinia.setActivePinia(root)
  t.after(() => pinia.disposePinia(root))
  return { storage, store: useCommandPaletteStore() }
}

test('server history replaces the local cache and is written back to it', async t => {
  const { store, storage } = setup(t, {
    fetchSearchSuggestions: async () => ({
      history: [{ text: 'E45 报警', kind: 'history' }, { text: '备件清单', kind: 'history' }],
      hot: [{ text: '工单流程', kind: 'hot', count: 12 }],
      titles: [], glossary_terms: [], prefix: '', hot_window_days: 30,
    }),
  })

  await store.loadSuggestions()

  assert.deepEqual(list(store.recentQueries), ['E45 报警', '备件清单'])
  assert.equal(store.suggestions.hot[0].text, '工单流程')
  // 离线缓存同步写入，按 (user, tenant) 分命名空间。
  assert.deepEqual(
    JSON.parse(storage.get('weknora_cmdk_recent:user-1:7') ?? '[]'),
    ['E45 报警', '备件清单'],
    'the offline cache mirrors the server history',
  )
})

test('a failed request keeps the cached history instead of clearing it', async t => {
  const storage = new Map<string, string>([
    ['weknora_cmdk_recent:user-1:7', JSON.stringify(['离线时还看得到'])],
  ])
  const { store } = setup(t, {
    fetchSearchSuggestions: async () => { throw new Error('offline') },
  }, storage)

  await store.loadSuggestions()

  // 清空会让"断网"看起来像"历史被删了"。
  assert.deepEqual(list(store.recentQueries), ['离线时还看得到'])
})

test('an empty server history leaves the cached history untouched', async t => {
  const storage = new Map<string, string>([
    ['weknora_cmdk_recent:user-1:7', JSON.stringify(['缓存里的词'])],
  ])
  const { store } = setup(t, {
    fetchSearchSuggestions: async () => ({
      history: [], hot: [], titles: [], glossary_terms: [], prefix: '', hot_window_days: 30,
    }),
  }, storage)

  store.loadCachedRecent()
  await store.loadSuggestions()

  // 服务端返回空（例如新用户）不应该把本地缓存当成"要删的东西"，但也不该
  // 用它冒充服务端历史：recentQueries 保持缓存值，等用户真正搜索后由日志
  // 决定。这里断言"没有因为空响应而报错或清空"。
  assert.deepEqual(list(store.recentQueries), ['缓存里的词'])
})

test('pushRecent only touches the local cache', async t => {
  let called = 0
  const { store, storage } = setup(t, {
    fetchSearchSuggestions: async () => {
      called++
      return {
        history: [], hot: [], titles: [], glossary_terms: [], prefix: '', hot_window_days: 30,
      }
    },
  })

  store.pushRecent('刚打的字')
  store.pushRecent('刚打的字')
  store.pushRecent('第二个词')

  assert.deepEqual(list(store.recentQueries), ['第二个词', '刚打的字'], 'deduped and newest first')
  assert.equal(called, 0, 'a typed query is not a searched query')
  assert.deepEqual(
    JSON.parse(storage.get('weknora_cmdk_recent:user-1:7') ?? '[]'),
    ['第二个词', '刚打的字'],
  )
})

test('clearRecent deletes on the server before dropping the local copy', async t => {
  const order: string[] = []
  const storage = new Map<string, string>()
  const { store } = setup(t, {
    clearSearchHistory: async () => { order.push('server') },
  }, storage)
  storage.set('weknora_cmdk_recent:user-1:7', JSON.stringify(['旧词']))
  store.loadCachedRecent()

  await store.clearRecent()

  assert.deepEqual(order, ['server'])
  assert.deepEqual(list(store.recentQueries), [])
  assert.equal(storage.get('weknora_cmdk_recent:user-1:7'), undefined)
})

test('clearRecent still clears locally when the server call fails', async t => {
  const storage = new Map<string, string>()
  const { store } = setup(t, {
    clearSearchHistory: async () => { throw new Error('server down') },
  }, storage)
  storage.set('weknora_cmdk_recent:user-1:7', JSON.stringify(['旧词']))
  store.loadCachedRecent()

  await store.clearRecent()

  // 用户明确要求"看不见它"：服务端删除失败时本地仍然清掉，而不是弹错后
  // 把旧词留在搜索框里。
  assert.deepEqual(list(store.recentQueries), [])
})

test('fetchPrefixSuggestions returns an empty bundle on failure', async t => {
  const { store } = setup(t, {
    fetchSearchSuggestions: async () => { throw new Error('boom') },
  })

  const bundle = await store.fetchPrefixSuggestions('e45')
  assert.deepEqual(list(bundle.history), [])
  assert.deepEqual(list(bundle.titles), [])
})

test('openPalette loads suggestions, closePalette clears the pending query', async t => {
  let calls = 0
  const { store } = setup(t, {
    fetchSearchSuggestions: async () => {
      calls++
      return {
        history: [{ text: '最近一次', kind: 'history' }],
        hot: [], titles: [], glossary_terms: [], prefix: '', hot_window_days: 30,
      }
    },
  })

  store.openPalette('预填的词')
  await flush()
  assert.equal(store.open, true)
  assert.equal(store.initialQuery, '预填的词')
  assert.equal(calls, 1, 'opening the palette refreshes history from the server')
  assert.deepEqual(list(store.recentQueries), ['最近一次'])

  store.closePalette()
  assert.equal(store.open, false)
  assert.equal(store.initialQuery, '')
})
