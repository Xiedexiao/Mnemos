import { del, get } from '@/utils/request'

/**
 * 搜索建议（KnowAxis V3.0 §12.1）。
 *
 * 三个来源都由服务端给出，前端的 localStorage 不再是唯一真相：
 *   - history：本人最近搜过的词（服务端按 (tenant, user) 取）；
 *   - hot：本租户窗口内的高频词（服务端已按可见范围裁决）；
 *   - titles：标题联想（服务端已按文档 ACL 过滤）。
 *
 * §12.6 的四条泄露规则由服务端保证，前端只负责渲染——前端的任何过滤都
 * 只是"少显示一些"，不构成权限边界。
 */
export type SuggestionKind = 'history' | 'hot' | 'title' | 'glossary'

export interface SearchSuggestion {
  text: string
  kind: SuggestionKind
  label?: string
  knowledge_id?: string
  knowledge_base_id?: string
  count?: number
}

export interface SuggestionBundle {
  history: SearchSuggestion[]
  hot: SearchSuggestion[]
  titles: SearchSuggestion[]
  glossary_terms: SearchSuggestion[]
  prefix: string
  hot_window_days: number
}

/** 空载荷：请求失败或未登录时用它，避免调用方到处判空。 */
export function emptySuggestionBundle(): SuggestionBundle {
  return {
    history: [], hot: [], titles: [], glossary_terms: [], prefix: '', hot_window_days: 0,
  }
}

/**
 * fetchSearchSuggestions 取一次建议。
 *
 * prefix 允许为空：那是"搜索框刚打开"的状态，服务端只返回历史与热门。
 */
export async function fetchSearchSuggestions(
  prefix: string,
  options: { limit?: number; hotDays?: number; signal?: AbortSignal } = {},
): Promise<SuggestionBundle> {
  const query = new URLSearchParams()
  query.set('q', prefix)
  if (options.limit) query.set('limit', String(options.limit))
  if (options.hotDays) query.set('hot_days', String(options.hotDays))
  const res: any = await get(`/api/v1/search/suggestions?${query.toString()}`, {
    signal: options.signal,
  })
  if (!res?.success || !res.data) return emptySuggestionBundle()
  return { ...emptySuggestionBundle(), ...res.data }
}

/**
 * clearSearchHistory 清空本人的搜索历史。
 *
 * 只删自己的行：服务端从上下文取身份，不接受 user_id 参数。
 */
export async function clearSearchHistory(): Promise<void> {
  await del('/api/v1/search/history')
}
