import { del, get, post } from '@/utils/request'

// Knowledge governance API client: lifecycle transitions, per-document ACL
// and external share links (KnowAxis V3.0 §2.1 / §2.5 / §7.1 v1 subsets).
// Backend: internal/handler/knowledge_lifecycle.go, knowledge_acl.go,
// share_link.go.

// ============================================================================
// Lifecycle state machine
// ============================================================================

// KnowledgeLifecycleAction mirrors the backend constants
// (internal/types/knowledge_lifecycle.go). The row menu's v1 subset is
// submit_review / confirm_review / mark_expired / delist / archive.
export type KnowledgeLifecycleAction =
  | 'submit_review'
  | 'publish'
  | 'reject_review'
  | 'confirm_review'
  | 'mark_stale'
  | 'mark_expired'
  | 'replace'
  | 'mark_conflict'
  | 'delist'
  | 'archive'
  | 'restore'
  | 'update_settings'

// KnowledgeLifecycleResolved is the read-time resolved behaviour returned by
// the transition endpoint for UI badges / toasts.
export interface KnowledgeLifecycleResolved {
  status?: string
  effective_status?: string
  reasons?: string[]
}

// KnowledgeLifecycleTransitionPayload is the body of POST
// /knowledge/:id/lifecycle/transition. Only `action` is required for the
// v1 row-menu actions.
export interface KnowledgeLifecycleTransitionPayload {
  action: KnowledgeLifecycleAction
  change_note?: string
}

// KnowledgeLifecycleTransitionResponse wraps the transition endpoint:
// the updated knowledge row plus its resolved behaviour.
export interface KnowledgeLifecycleTransitionResponse {
  success: boolean
  // Kept loose: the caller only consumes `resolved` for feedback.
  data?: Record<string, unknown>
  resolved: KnowledgeLifecycleResolved
}

// POST /api/v1/knowledge/:id/lifecycle/transition — apply one lifecycle
// action. Illegal transitions are a 400 with an audited log entry.
export function transitionKnowledgeLifecycle(
  knowledgeId: string,
  payload: KnowledgeLifecycleTransitionPayload,
) {
  return post<KnowledgeLifecycleTransitionResponse>(
    `/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/lifecycle/transition`,
    payload,
  )
}

// ============================================================================
// Per-document ACL (deny wins; secret/confidential need an explicit allow)
// ============================================================================

// KnowledgeAclEntry is one grant row (backend types.KnowledgeACL).
export interface KnowledgeAclEntry {
  id: string
  knowledge_id: string
  // v1 resolves 'user' only; group/role are reserved.
  subject_type: string
  subject_id: string
  action: string
  effect: 'allow' | 'deny' | string
  granted_by?: string
  expires_at?: string | null
  created_at?: string
}

// The v1 action dropdown: a subset of the backend's 13 PermissionAction
// constants (lowercase strings, internal/types/permission_action.go).
export const KNOWLEDGE_ACL_ACTIONS = [
  'view',
  'ask',
  'download',
  'share',
  'manage',
  'review',
  'export',
] as const

export type KnowledgeAclAction = (typeof KNOWLEDGE_ACL_ACTIONS)[number]

export type KnowledgeAclEffect = 'allow' | 'deny'

// GrantKnowledgeAclPayload is the body of POST /knowledge/:id/acl.
// subject_type is fixed to 'user' in v1; expires_at is optional (null =
// grant stands until revoked).
export interface GrantKnowledgeAclPayload {
  subject_type?: 'user'
  subject_id: string
  action: KnowledgeAclAction
  effect: KnowledgeAclEffect
  expires_at?: string | null
}

export interface KnowledgeAclListResponse {
  success: boolean
  data: KnowledgeAclEntry[]
}

export interface KnowledgeAclMutationResponse {
  success: boolean
  data?: KnowledgeAclEntry
}

// GET /api/v1/knowledge/:id/acl — all grant rows of one document (Contributor+).
export function listKnowledgeAcl(knowledgeId: string) {
  return get<KnowledgeAclListResponse>(`/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/acl`)
}

// POST /api/v1/knowledge/:id/acl — grant or deny one action to one user
// (Admin+; granting to yourself is rejected server-side).
export function grantKnowledgeAcl(knowledgeId: string, payload: GrantKnowledgeAclPayload) {
  return post<KnowledgeAclMutationResponse>(
    `/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/acl`,
    payload,
  )
}

// DELETE /api/v1/knowledge/:id/acl/:acl_id — revoke one grant (Admin+;
// takes effect on the next request).
export function revokeKnowledgeAcl(knowledgeId: string, aclId: string) {
  return del(`/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/acl/${encodeURIComponent(aclId)}`)
}

// ============================================================================
// External share links
// ============================================================================

// KnowledgeShareLink is one issued link (backend types.ShareLink). The
// management list returns the token because v1 has no "reveal once" store.
export interface KnowledgeShareLink {
  id: string
  knowledge_id: string
  token: string
  // Audience: 'org' (any member with the link) | 'people'.
  link_type: 'org' | 'people' | string
  // Capability: 'view' | 'download'.
  permission: 'view' | 'download' | string
  expires_at?: string | null
  revoked_at?: string | null
  created_by?: string
  approval_status?: string
  visit_count?: number
  created_at?: string
  updated_at?: string
}

export type ShareLinkType = 'org' | 'people'
export type ShareLinkPermission = 'view' | 'download'

// CreateShareLinkPayload is the body of POST /knowledge/:id/share-links.
// expires_at is optional (null = valid until revoked). Classification is the
// hard ceiling server-side: confidential can never be shared and secret
// cannot get download permission.
export interface CreateShareLinkPayload {
  link_type: ShareLinkType
  permission: ShareLinkPermission
  expires_at?: string | null
}

export interface KnowledgeShareLinkListResponse {
  success: boolean
  data: KnowledgeShareLink[]
}

export interface KnowledgeShareLinkResponse {
  success: boolean
  data: KnowledgeShareLink
}

// GET /api/v1/knowledge/:knowledge_id/share-links — the document's links,
// newest first (Contributor + KB write).
export function listKnowledgeShareLinks(knowledgeId: string) {
  return get<KnowledgeShareLinkListResponse>(
    `/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/share-links`,
  )
}

// POST /api/v1/knowledge/:knowledge_id/share-links — issue a link (201).
export function createKnowledgeShareLink(
  knowledgeId: string,
  payload: CreateShareLinkPayload,
) {
  return post<KnowledgeShareLinkResponse>(
    `/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/share-links`,
    payload,
  )
}

// DELETE /api/v1/share-links/:share_link_id — revoke immediately (idempotent
// server-side; unknown/already-revoked ids are a 404).
export function revokeKnowledgeShareLink(shareLinkId: string) {
  return del(`/api/v1/share-links/${encodeURIComponent(shareLinkId)}`)
}

// maskShareToken renders a token safe for list display: keep the first 6
// characters, mask the rest. Tokens are the visitor's only credential.
export function maskShareToken(token?: string | null): string {
  const raw = (token || '').trim()
  if (!raw) return '--'
  if (raw.length <= 6) return raw
  return `${raw.slice(0, 6)}…`
}

// buildShareLinkUrl composes the visitor URL shown once after creation:
// {origin}/api/v1/public/share/{token} (anonymous resolve endpoint).
export function buildShareLinkUrl(token: string): string {
  const origin = typeof window !== 'undefined' && window.location ? window.location.origin : ''
  return `${origin}/api/v1/public/share/${token}`
}
