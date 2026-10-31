/**
 * Row-menu governance actions for one knowledge entry, keyed by its
 * lifecycle status.
 *
 * Visibility mirrors the backend state machine
 * (internal/types/knowledge_lifecycle.go → knowledgeLifecycleTransitions)
 * for the v1 action subset the row menu exposes. The API layer still gets
 * the authoritative 400 on an illegal transition — this map only keeps the
 * menu honest so users don't see actions that would fail.
 */

export type GovernanceActionId =
  | 'submit-review'
  | 'confirm-review'
  | 'mark-expired'
  | 'delist'
  | 'archive'

export interface GovernanceActionMeta {
  id: GovernanceActionId
  /** i18n key under knowledgeGovernance.* */
  labelKey: string
  /** TDesign icon name. */
  icon: string
  /**
   * Body action for POST /knowledge/:id/lifecycle/transition. Absent for
   * submit-review, which posts to /knowledge/:id/review/submit instead.
   */
  transitionAction?: 'confirm_review' | 'mark_expired' | 'delist' | 'archive'
  /** Confirm-dialog theme: destructive actions get a danger tone. */
  danger?: boolean
}

export const GOVERNANCE_ACTIONS: GovernanceActionMeta[] = [
  {
    id: 'submit-review',
    labelKey: 'knowledgeGovernance.actionSubmitReview',
    icon: 'secured',
  },
  {
    id: 'confirm-review',
    labelKey: 'knowledgeGovernance.actionConfirmReview',
    icon: 'check-circle',
    transitionAction: 'confirm_review',
  },
  {
    id: 'mark-expired',
    labelKey: 'knowledgeGovernance.actionMarkExpired',
    icon: 'time',
    transitionAction: 'mark_expired',
  },
  {
    id: 'delist',
    labelKey: 'knowledgeGovernance.actionDelist',
    icon: 'stop-circle',
    transitionAction: 'delist',
  },
  {
    id: 'archive',
    labelKey: 'knowledgeGovernance.actionArchive',
    icon: 'folder-open',
    transitionAction: 'archive',
    danger: true,
  },
]

/**
 * Lifecycle statuses in which each action is offered. `valid` is the healthy
 * default; rows missing lifecycle_status (legacy lists) are treated as valid.
 */
const ACTION_STATUSES: Record<GovernanceActionId, string[]> = {
  // Per the A-phase spec: draft/valid/pending_review_cycle/stale/conflict.
  'submit-review': ['draft', 'valid', 'pending_review_cycle', 'stale', 'conflict'],
  // 复审确认 refreshes last_reviewed_* on everything that can reach valid.
  'confirm-review': ['valid', 'pending_review_cycle', 'stale', 'conflict'],
  // 确认过期: content that is still "live" but past its usefulness.
  'mark-expired': ['valid', 'pending_review_cycle', 'stale'],
  // 下架: immediate removal from retrieval.
  delist: ['valid', 'pending_review_cycle', 'stale', 'conflict'],
  // 归档: also reachable from expired/replaced per the transition map.
  archive: ['valid', 'pending_review_cycle', 'stale', 'conflict', 'expired', 'replaced'],
}

const normalize = (status?: string | null) => (status || '').trim().toLowerCase() || 'valid'

/** The governance actions offered for one lifecycle status (menu order). */
export function governanceActionsForStatus(status?: string | null): GovernanceActionMeta[] {
  const effective = normalize(status)
  return GOVERNANCE_ACTIONS.filter((action) => ACTION_STATUSES[action.id].includes(effective))
}
