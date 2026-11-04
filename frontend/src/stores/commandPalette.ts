import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { useAuthStore } from './auth'
import {
  clearSearchHistory,
  emptySuggestionBundle,
  fetchSearchSuggestions,
  type SuggestionBundle,
} from '@/api/search-suggestion'

const RECENT_KEY_PREFIX = 'weknora_cmdk_recent'
const RECENT_LIMIT = 4

// recentKey scopes the offline cache to the active (user, tenant) pair.
//
// 服务端已经是历史的真相（§12.1 历史搜索落库），这份 localStorage 只剩
// 一个用途：网络不可用或服务端还没返回时，搜索框里仍然有东西可点。键名带上
// (user, tenant) 是为了让同一浏览器上的不同身份不互相看到对方搜过什么。
function recentKey(userId: string | null | undefined, tenantId: number | string | null | undefined): string {
  const u = userId ? String(userId) : 'anon'
  const t = tenantId !== null && tenantId !== undefined && tenantId !== '' ? String(tenantId) : 'none'
  return `${RECENT_KEY_PREFIX}:${u}:${t}`
}

/**
 * Pinia store for the global command palette (⌘K / Ctrl+K).
 * The palette itself is rendered once in platform/index.vue.
 * Any code (router redirect, deep link, programmatic trigger) can open it
 * by calling openPalette(q).
 *
 * 建议来源已从 localStorage 搬到服务端（§12.1：实时联想、历史搜索、热门
 * 搜索；建议只来自用户可见集合）。localStorage 退化为离线缓存：服务端响应
 * 到达即覆盖，请求失败时保留上一次的内容而不是清空——清空会让"断网"看起来
 * 像"历史被删了"。
 */
export const useCommandPaletteStore = defineStore('commandPalette', () => {
  const open = ref(false)
  const initialQuery = ref('')
  const recentQueries = ref<string[]>([])
  const suggestions = ref<SuggestionBundle>(emptySuggestionBundle())
  const suggestionsLoading = ref(false)

  // 每个身份一条请求号：切换用户/租户后，旧请求的响应必须丢弃，否则会把
  // 上一个人的历史写进当前用户的搜索框。
  let requestSeq = 0

  // Lazily resolve the auth store inside actions / watchers — at store
  // setup time pinia may not have finished registering peer stores yet.
  const currentRecentKey = (): string => {
    const auth = useAuthStore()
    return recentKey(auth.user?.id, auth.effectiveTenantId)
  }

  // loadCachedRecent reads the offline cache only.
  const loadCachedRecent = () => {
    try {
      const raw = localStorage.getItem(currentRecentKey())
      const parsed = raw ? JSON.parse(raw) : []
      recentQueries.value = Array.isArray(parsed) ? parsed.filter((q: unknown) => typeof q === 'string') : []
    } catch {
      recentQueries.value = []
    }
  }

  const cacheRecent = (queries: string[]) => {
    try {
      localStorage.setItem(currentRecentKey(), JSON.stringify(queries))
    } catch {
      /* ignore quota errors */
    }
  }

  /**
   * loadSuggestions pulls the server-side history.
   *
   * 只取历史与热门（prefix 为空）：联想是按输入实时取的，打开面板时不必
   * 预取。失败时保留上一次结果并退回到本地缓存。
   */
  const loadSuggestions = async () => {
    const seq = ++requestSeq
    suggestionsLoading.value = true
    try {
      const bundle = await fetchSearchSuggestions('', { limit: RECENT_LIMIT })
      if (seq !== requestSeq) return
      suggestions.value = bundle
      const texts = bundle.history.map(item => item.text).filter(Boolean)
      if (texts.length > 0) {
        recentQueries.value = texts.slice(0, RECENT_LIMIT)
        cacheRecent(recentQueries.value)
      }
    } catch {
      if (seq !== requestSeq) return
      // 服务端不可用：保留 localStorage 里的旧值，不清空。
      loadCachedRecent()
    } finally {
      if (seq === requestSeq) suggestionsLoading.value = false
    }
  }

  /**
   * fetchPrefixSuggestions is the type-ahead call.
   *
   * 返回服务端的联想结果（标题 + 术语），由调用方决定怎么渲染。热门词按
   * 输入过滤也走这里，这样"打开面板"与"开始输入"只有一个真相来源。
   */
  const fetchPrefixSuggestions = async (
    prefix: string, options: { limit?: number; hotDays?: number; signal?: AbortSignal } = {},
  ): Promise<SuggestionBundle> => {
    try {
      return await fetchSearchSuggestions(prefix, options)
    } catch {
      return emptySuggestionBundle()
    }
  }

  const openPalette = (query = '') => {
    initialQuery.value = query
    open.value = true
    void loadSuggestions()
  }

  const closePalette = () => {
    open.value = false
    initialQuery.value = ''
  }

  /**
   * pushRecent records a locally-executed query.
   *
   * 这里只更新本地缓存，不写服务端：服务端的历史来自 search_logs，只有
   * **真正执行过**的查询才会进入日志（见 RecordQuery 的说明）。把一个还没
   * 跑的查询写进服务端历史，会让历史变成"我打过字的东西"。
   */
  const pushRecent = (q: string) => {
    const trimmed = q.trim()
    if (!trimmed) return
    recentQueries.value = [
      trimmed,
      ...recentQueries.value.filter(x => x !== trimmed),
    ].slice(0, RECENT_LIMIT)
    cacheRecent(recentQueries.value)
  }

  /**
   * clearRecent deletes the server-side history, then the local cache.
   *
   * 顺序是刻意的：先删服务端再删本地。反过来（先清本地）会让服务端删除
   * 失败时用户以为清掉了，下次打开面板历史又冒出来。
   */
  const clearRecent = async () => {
    try {
      await clearSearchHistory()
    } catch {
      // 服务端删除失败：本地仍然清掉（用户明确要求"看不见它"），但下一次
      // loadSuggestions 会把服务端的历史拉回来——这是诚实的失败表现。
    }
    recentQueries.value = []
    suggestions.value = emptySuggestionBundle()
    try {
      localStorage.removeItem(currentRecentKey())
    } catch {
      /* ignore */
    }
  }

  // Reload suggestions + cache whenever the active user or tenant changes
  // (login, logout, tenant switch). 先读本地缓存保证面板立刻有内容，再让
  // 服务端结果覆盖它。
  watch(
    () => {
      const auth = useAuthStore()
      return [auth.user?.id, auth.effectiveTenantId] as const
    },
    () => {
      requestSeq++
      loadCachedRecent()
      suggestions.value = emptySuggestionBundle()
    },
    { immediate: true },
  )

  return {
    open,
    initialQuery,
    recentQueries,
    suggestions,
    suggestionsLoading,
    openPalette,
    closePalette,
    pushRecent,
    clearRecent,
    loadSuggestions,
    loadCachedRecent,
    fetchPrefixSuggestions,
  }
})
