import { get, put, post } from '@/utils/request'

// ChatHistoryConfig represents the chat history KB configuration for a tenant.
// knowledge_base_id is auto-managed by the backend; frontend only sets other fields.
export interface ChatHistoryConfig {
  enabled: boolean
  embedding_model_id: string
  knowledge_base_id?: string // read-only, auto-managed
}

// ChatHistoryKBStats represents statistics about the chat history knowledge base
export interface ChatHistoryKBStats {
  enabled: boolean
  embedding_model_id?: string
  knowledge_base_id?: string
  knowledge_base_name?: string
  indexed_message_count: number
  has_indexed_messages: boolean
}

// MessageSearchRequest defines search parameters for message search
export interface MessageSearchRequest {
  query: string
  mode?: 'keyword' | 'vector' | 'hybrid'
  limit?: number
  session_ids?: string[]
}

// MessageSearchGroupItem represents a merged Q&A pair in search results
export interface MessageSearchGroupItem {
  request_id: string
  session_id: string
  session_title: string
  query_content: string
  answer_content: string
  score: number
  match_type: string
  created_at: string
}

// MessageSearchResult represents the full search result
export interface MessageSearchResult {
  items: MessageSearchGroupItem[]
  total: number
}

// Get tenant chat history config via KV API
export function getTenantChatHistoryConfig() {
  return get('/api/v1/tenants/kv/chat-history-config')
}

// Update tenant chat history config via KV API
export function updateTenantChatHistoryConfig(config: ChatHistoryConfig) {
  return put('/api/v1/tenants/kv/chat-history-config', config)
}

// Get chat history KB statistics
export function getChatHistoryKBStats() {
  return get('/api/v1/messages/chat-history-stats')
}

// Search messages across all sessions (keyword + vector hybrid search)
export function searchMessages(data: MessageSearchRequest) {
  return post('/api/v1/messages/search', data)
}

// MessageFeedbackRating is the verdict a user gives one chat answer.
export type MessageFeedbackRating = 'useful' | 'useless'

// MessageFeedbackPayload is the body for POST /messages/:message_id/feedback.
// reason_codes only makes sense for useless ratings; comment is free text.
export interface MessageFeedbackPayload {
  rating: MessageFeedbackRating
  reason_codes?: string[]
  comment?: string
}

// Canonical machine-readable reason codes for useless ratings, mirrored by the
// ChatMessageFeedback panel options. The backend stores them verbatim.
export const MESSAGE_FEEDBACK_REASON_CODES = [
  'fact_error',
  'outdated',
  'incomplete',
  'off_topic',
  'other',
] as const

export type MessageFeedbackReasonCode = (typeof MESSAGE_FEEDBACK_REASON_CODES)[number]

// Submit (or overwrite — the backend keys one row per user per message) the
// caller's useful/useless rating for one message.
export function submitMessageFeedback(messageId: string, data: MessageFeedbackPayload) {
  return post(`/api/v1/messages/${encodeURIComponent(messageId)}/feedback`, data)
}
