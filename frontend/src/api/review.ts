import { get, post } from '@/utils/request'

// Knowledge review workflow API client (KnowAxis V3.0 §6 v1 subset).
// Backend: internal/handler/review.go + internal/types/review.go.

// ReviewTaskBaseline is the content fingerprint snapshot frozen at submit
// time (backend types.ApprovalBaseline). The workbench renders it as a
// collapsible JSON summary; `title` is also the task list's display name.
export interface ReviewTaskBaseline {
  content_hash?: string
  chunk_count?: number
  title?: string
  custom_metadata_summary?: string
  classification?: string
  parse_status?: string
  [key: string]: unknown
}

// ReviewDecisionRecord is one vote in decided_history.
export interface ReviewDecisionRecord {
  by: string
  decision: string
  at: string
}

// ReviewTask is one review submission (backend types.ApprovalTask).
export interface ReviewTask {
  id: string
  knowledge_id: string
  kb_id: string
  // Flow mode frozen at submit time: 'single' | 'dual'.
  mode?: string
  baseline?: ReviewTaskBaseline
  content_hash?: string
  // Risk routes by classification: confidential→high, secret→mid, else low.
  risk_level: 'low' | 'mid' | 'high' | string
  status: 'pending' | 'approved' | 'rejected' | 'canceled' | string
  requested_by: string
  decided_by?: string
  decided_history?: ReviewDecisionRecord[]
  decision_at?: string | null
  reason_code?: string
  comment?: string
  sla_due?: string | null
  receipt_id?: string
  created_at?: string
  updated_at?: string
}

// Canonical machine-readable reject reason codes (backend ApprovalReasonCode).
// Approving leaves reason_code empty; rejecting requires one plus a comment.
export const REVIEW_REASON_CODES = [
  'fact_error',
  'insufficient_evidence',
  'format',
  'conflict',
  'permission',
  'other',
] as const

export type ReviewReasonCode = (typeof REVIEW_REASON_CODES)[number]

export type ReviewDecision = 'approved' | 'rejected'

// ListReviewTasksResponse wraps GET /review/tasks (pending pool, Admin+).
export interface ListReviewTasksResponse {
  success: boolean
  data: ReviewTask[]
}

// GET /api/v1/review/tasks — the tenant's pending review tasks (Admin+).
export function listReviewTasks() {
  return get<ListReviewTasksResponse>('/api/v1/review/tasks')
}

// DecideReviewPayload is the body of POST /review/tasks/:task_id/decide.
// decision is whitelisted server-side; rejection requires reason_code and
// comment (enforced here so the UI never sends an invalid ballot).
export interface DecideReviewPayload {
  decision: ReviewDecision
  reason_code?: ReviewReasonCode | ''
  comment?: string
}

// POST /api/v1/review/tasks/:task_id/decide — cast one approver vote.
// Four-eyes: deciding on your own submission is a 403.
export function decideReviewTask(taskId: string, payload: DecideReviewPayload) {
  return post(`/api/v1/review/tasks/${encodeURIComponent(taskId)}/decide`, payload)
}

// POST /api/v1/knowledge/:id/review/submit — freeze the content baseline,
// create an approval task and move the knowledge into pending_review.
// Same knowledge already pending review is a 409 (surfaced as a toast).
export function submitKnowledgeReview(knowledgeId: string) {
  return post(`/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/review/submit`, {})
}
