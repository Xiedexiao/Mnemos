import { del, get, patch, post, put } from '@/utils/request'

// Governance console API client (KnowAxis V3.0 §6.2 / §13.7 / §3.2 / §4.7 /
// §2.3 / §12.5 / §14.4).
//
// One module for the governance surfaces the backend exposes but the UI never
// reached: governance approvals for all fourteen §6.2 subjects, official
// answers, the template center, directory permissions, search quality and the
// knowledge-gap list. Grouping them keeps the console's data layer in one place
// and mirrors the backend's grouping.

// ============================================================================
// §6.2 governance approvals (fourteen subject types)
// ============================================================================

// GOVERNANCE_SUBJECT_TYPES mirrors the backend types.ApprovalSubjectType enum.
// The labels live in i18n (governanceConsole.subject_<value>) so the console
// stays translatable.
export const GOVERNANCE_SUBJECT_TYPES = [
  'knowledge',
  'official_answer',
  'rule',
  'ontology_schema',
  'permission_grant',
  'external_share',
  'bulk_export',
  'deletion',
  'knowledge_merge',
  'taxonomy_change',
  'model_release',
  'agent_tool_grant',
  'knowledge_candidate',
  'change_request',
] as const

export type GovernanceSubjectType = (typeof GOVERNANCE_SUBJECT_TYPES)[number]

export type ApprovalRiskLevel = 'low' | 'mid' | 'high'

// GovernanceApprovalTask mirrors the backend ApprovalTask as the governance
// queue returns it.
export interface GovernanceApprovalTask {
  id: string
  knowledge_id?: string
  kb_id?: string
  subject_type: GovernanceSubjectType | string
  subject_id: string
  title?: string
  mode?: string
  risk_level: ApprovalRiskLevel | string
  status: 'pending' | 'approved' | 'rejected' | 'canceled' | string
  requested_by: string
  decided_by?: string
  reason_code?: string
  comment?: string
  sla_due?: string | null
  receipt_id?: string
  created_at?: string
  decision_at?: string | null
}

// GovernanceApprovalStatus mirrors the backend GovernanceApprovalStatus.
export interface GovernanceApprovalStatus {
  subject_type: string
  subject_id: string
  task_id?: string
  status?: string
  risk_level?: string
  receipt_id?: string
  requested_by?: string
  created_at?: string | null
  decided_at?: string | null
}

// GovernanceSubmitPayload is the body of POST /governance-approvals.
export interface GovernanceSubmitPayload {
  subject_type: GovernanceSubjectType
  subject_id: string
  kb_id?: string
  title?: string
  reason?: string
  // risk_level may only raise the subject's floor; the backend rejects a lower
  // value for high-risk subjects.
  risk_level?: ApprovalRiskLevel
  snapshot?: Record<string, unknown>
}

interface GovernanceListResponse {
  success: boolean
  data: GovernanceApprovalTask[]
}

interface GovernanceTaskResponse {
  success: boolean
  data: GovernanceApprovalTask
}

interface GovernanceStatusResponse {
  success: boolean
  data: GovernanceApprovalStatus
}

// GET /api/v1/governance-approvals — pending governance tasks by subject type.
export function listGovernanceApprovals(subjectType?: GovernanceSubjectType | '') {
  const query = subjectType ? `?subject_type=${encodeURIComponent(subjectType)}` : ''
  return get<GovernanceListResponse>(`/api/v1/governance-approvals${query}`)
}

// POST /api/v1/governance-approvals — submit one governance action for approval.
export function submitGovernanceApproval(payload: GovernanceSubmitPayload) {
  return post<GovernanceTaskResponse>('/api/v1/governance-approvals', payload)
}

// POST /api/v1/governance-approvals/:task_id/decide — cast one approval vote.
export function decideGovernanceApproval(
  taskId: string,
  payload: { decision: 'approved' | 'rejected'; comment?: string },
) {
  return post<GovernanceTaskResponse>(
    `/api/v1/governance-approvals/${encodeURIComponent(taskId)}/decide`,
    payload,
  )
}

// GET /api/v1/governance-approvals/status — the latest state of one subject.
export function getGovernanceApprovalStatus(subjectType: string, subjectId: string) {
  const query = new URLSearchParams({ subject_type: subjectType, subject_id: subjectId })
  return get<GovernanceStatusResponse>(`/api/v1/governance-approvals/status?${query.toString()}`)
}

// ============================================================================
// §6.3 approval chain (multi-stage)
// ============================================================================

// ApprovalChainNode is one instantiated node of an in-flight task.
export interface ApprovalChainNode {
  id: string
  task_id: string
  seq: number
  name?: string
  // 'or' = 或签 (any), 'and' = 会签 (all).
  mode: 'or' | 'and' | string
  approver_ids?: string[]
  status: 'pending' | 'active' | 'approved' | 'rejected' | 'skipped' | string
  required_count: number
  decided_at?: string | null
}

// ApprovalVote is one recorded vote.
export interface ApprovalVote {
  id: string
  task_id: string
  node_id: string
  approver_id: string
  delegate_of?: string
  decision: string
  comment?: string
  created_at?: string
}

// ApprovalChainState mirrors the backend ApprovalChainState.
export interface ApprovalChainState {
  task_id: string
  nodes: ApprovalChainNode[]
  votes: ApprovalVote[]
  active_node_seq: number
  finished: boolean
  approved: boolean
}

interface ApprovalChainResponse {
  success: boolean
  data: ApprovalChainState
}

// GET /api/v1/review/tasks/:task_id/chain — read the in-flight chain.
export function getApprovalChain(taskId: string) {
  return get<ApprovalChainResponse>(
    `/api/v1/review/tasks/${encodeURIComponent(taskId)}/chain`,
  )
}

// POST /api/v1/review/tasks/:task_id/chain/approvers — 加签.
export function addApprovalChainApprover(taskId: string, nodeId: string, approverId: string) {
  return post<ApprovalChainResponse>(
    `/api/v1/review/tasks/${encodeURIComponent(taskId)}/chain/approvers`,
    { node_id: nodeId, approver_id: approverId },
  )
}

// POST /api/v1/review/tasks/:task_id/chain/transfer — 转签.
export function transferApprovalChainApprover(
  taskId: string,
  nodeId: string,
  fromId: string,
  toId: string,
) {
  return post<ApprovalChainResponse>(
    `/api/v1/review/tasks/${encodeURIComponent(taskId)}/chain/transfer`,
    { node_id: nodeId, from_id: fromId, to_id: toId },
  )
}

// ============================================================================
// §13.7 official answers
// ============================================================================

export type OfficialAnswerStatus =
  | 'draft'
  | 'in_review'
  | 'approved'
  | 'published'
  | 'expired'
  | 'superseded'
  | 'retired'

// OfficialAnswer mirrors the backend types.OfficialAnswer.
export interface OfficialAnswer {
  id: string
  question: string
  answer: string
  conditions?: string
  exceptions?: string
  scope?: string
  risk_level?: ApprovalRiskLevel | string
  status: OfficialAnswerStatus | string
  effective_at?: string | null
  expires_at?: string | null
  owner_id?: string
  evidence?: string[]
  receipt_id?: string
  superseded_by_id?: string
  knowledge_base_id?: string
  version?: number
  created_by?: string
  reviewed_by?: string
  created_at?: string
  updated_at?: string
}

// OfficialAnswerPayload is the body of POST /official-answers.
export interface OfficialAnswerPayload {
  id?: string
  question: string
  answer: string
  conditions?: string
  exceptions?: string
  scope?: string
  risk_level?: ApprovalRiskLevel
  owner_id?: string
  evidence?: string[]
  knowledge_base_id?: string
  effective_at?: string | null
  expires_at?: string | null
}

interface OfficialAnswerListResponse {
  success: boolean
  data: OfficialAnswer[]
}

interface OfficialAnswerResponse {
  success: boolean
  data: OfficialAnswer
}

// GET /api/v1/official-answers — list by status.
export function listOfficialAnswers(status?: OfficialAnswerStatus | '') {
  const query = status ? `?status=${encodeURIComponent(status)}` : ''
  return get<OfficialAnswerListResponse>(`/api/v1/official-answers${query}`)
}

// POST /api/v1/official-answers — create or edit a draft.
export function saveOfficialAnswer(payload: OfficialAnswerPayload) {
  return post<OfficialAnswerResponse>('/api/v1/official-answers', payload)
}

// POST /api/v1/official-answers/:id/submit — draft → in_review.
export function submitOfficialAnswer(id: string) {
  return post<OfficialAnswerResponse>(
    `/api/v1/official-answers/${encodeURIComponent(id)}/submit`,
    {},
  )
}

// POST /api/v1/official-answers/:id/approve — in_review → approved.
// High-risk answers require an approved receipt id.
export function approveOfficialAnswer(id: string, receiptId?: string) {
  return post<OfficialAnswerResponse>(
    `/api/v1/official-answers/${encodeURIComponent(id)}/approve`,
    { receipt_id: receiptId || '' },
  )
}

// POST /api/v1/official-answers/:id/publish — approved → published.
export function publishOfficialAnswer(id: string) {
  return post<OfficialAnswerResponse>(
    `/api/v1/official-answers/${encodeURIComponent(id)}/publish`,
    {},
  )
}

// POST /api/v1/official-answers/:id/retire — withdraw or supersede.
export function retireOfficialAnswer(id: string, supersededById?: string) {
  return post<OfficialAnswerResponse>(
    `/api/v1/official-answers/${encodeURIComponent(id)}/retire`,
    { superseded_by_id: supersededById || '' },
  )
}

// ============================================================================
// §3.2/§4.7 template center
// ============================================================================

export type KnowledgeFieldType =
  | 'text'
  | 'textarea'
  | 'number'
  | 'date'
  | 'select'
  | 'multiselect'
  | 'attachment'
  | 'reference'

// KnowledgeFieldRules mirrors the backend JSON rules blob.
export interface KnowledgeFieldRules {
  options?: string[]
  min?: number
  max?: number
  pattern?: string
}

// KnowledgeField is one typed field definition.
export interface KnowledgeField {
  id?: string
  template_id?: string
  field_key: string
  label: string
  field_type: KnowledgeFieldType | string
  required?: boolean
  default_value?: string
  rules?: KnowledgeFieldRules
  editable_role?: string
  recall_field?: boolean
  sort_order?: number
}

// KnowledgeTemplate is one versioned template.
export interface KnowledgeTemplate {
  id: string
  template_key: string
  version: number
  name: string
  description?: string
  knowledge_type?: string
  active: boolean
  fields?: KnowledgeField[]
  created_by?: string
  created_at?: string
  updated_at?: string
}

// KnowledgeTemplatePayload is the body of POST /knowledge-templates.
export interface KnowledgeTemplatePayload {
  template_key: string
  name: string
  description?: string
  knowledge_type?: string
  fields: KnowledgeField[]
  kb_id?: string
}

// FieldMigration is one structural field change task (§4.7).
export interface FieldMigration {
  id: string
  template_id: string
  change_kind: 'add' | 'remove' | 'rename' | 'retype' | string
  field_key: string
  previous_key?: string
  affected_count: number
  status: 'pending' | 'done' | 'waived' | string
  note?: string
  created_at?: string
}

interface TemplateListResponse {
  success: boolean
  data: KnowledgeTemplate[]
}

interface TemplateResponse {
  success: boolean
  data: KnowledgeTemplate
}

interface FieldMigrationListResponse {
  success: boolean
  data: FieldMigration[]
}

interface FieldMigrationResponse {
  success: boolean
  data: FieldMigration
}

// GET /api/v1/knowledge-templates — active templates with their fields.
export function listKnowledgeTemplates() {
  return get<TemplateListResponse>('/api/v1/knowledge-templates')
}

// POST /api/v1/knowledge-templates — save a new template version.
export function saveKnowledgeTemplate(payload: KnowledgeTemplatePayload) {
  return post<TemplateResponse>('/api/v1/knowledge-templates', payload)
}

// GET /api/v1/knowledge-field-migrations — structural change task list.
export function listFieldMigrations(status?: string) {
  const query = status ? `?status=${encodeURIComponent(status)}` : ''
  return get<FieldMigrationListResponse>(`/api/v1/knowledge-field-migrations${query}`)
}

// PATCH /api/v1/knowledge-field-migrations/:id — close or waive a task.
export function resolveFieldMigration(id: string, status: 'done' | 'waived', note?: string) {
  return patch<FieldMigrationResponse>(
    `/api/v1/knowledge-field-migrations/${encodeURIComponent(id)}`,
    { status, note: note || '' },
  )
}

// ============================================================================
// §2.3 directory permissions
// ============================================================================

// FolderGrant is one directory-level ACL row.
export interface FolderGrant {
  id: string
  knowledge_base_id: string
  folder_path: string
  subject_type?: string
  subject_id: string
  action: string
  effect: 'allow' | 'deny' | string
  inherit_children?: boolean
  granted_by?: string
  expires_at?: string | null
  created_at?: string
}

// FolderACLDecision is the folded verdict for one folder path.
export interface FolderACLDecision {
  folder_path: string
  allowed_actions?: string[]
  denied_actions?: string[]
  inherited_from?: string
}

// ACLCeilingViolation is one governance finding (§2.3 上限).
export interface ACLCeilingViolation {
  id: string
  knowledge_base_id: string
  resource_type: string
  resource_id: string
  kind: string
  detail?: Record<string, unknown>
  detected_at: string
}

// FolderGrantPayload is the body of POST /knowledge-bases/:id/folder-acl.
export interface FolderGrantPayload {
  folder_path: string
  subject_type?: string
  subject_id: string
  action: string
  effect?: 'allow' | 'deny'
  inherit_children?: boolean
  expires_at?: string | null
}

interface FolderGrantListResponse {
  success: boolean
  data: FolderGrant[]
}

interface FolderGrantResponse {
  success: boolean
  data: FolderGrant
}

interface FolderDecisionResponse {
  success: boolean
  data: FolderACLDecision
}

interface CeilingViolationListResponse {
  success: boolean
  data: ACLCeilingViolation[]
}

// GET /api/v1/knowledge-bases/:id/folder-acl — list directory grants.
export function listFolderGrants(kbId: string) {
  return get<FolderGrantListResponse>(
    `/api/v1/knowledge-bases/${encodeURIComponent(kbId)}/folder-acl`,
  )
}

// POST /api/v1/knowledge-bases/:id/folder-acl — grant a directory permission.
// Opening an action the parent folder lacks is a 403 and is recorded as a
// governance finding.
export function grantFolderPermission(kbId: string, payload: FolderGrantPayload) {
  return post<FolderGrantResponse>(
    `/api/v1/knowledge-bases/${encodeURIComponent(kbId)}/folder-acl`,
    payload,
  )
}

// DELETE /api/v1/knowledge-bases/:id/folder-acl/:grant_id
export function revokeFolderGrant(kbId: string, grantId: string) {
  return del(
    `/api/v1/knowledge-bases/${encodeURIComponent(kbId)}/folder-acl/${encodeURIComponent(grantId)}`,
  )
}

// GET /api/v1/knowledge-bases/:id/folder-acl/resolve — folded verdict.
export function resolveFolderPermission(kbId: string, folderPath: string) {
  const query = `?folder_path=${encodeURIComponent(folderPath)}`
  return get<FolderDecisionResponse>(
    `/api/v1/knowledge-bases/${encodeURIComponent(kbId)}/folder-acl/resolve${query}`,
  )
}

// GET /api/v1/acl-ceiling-violations — governance findings.
export function listACLCeilingViolations() {
  return get<CeilingViolationListResponse>('/api/v1/acl-ceiling-violations')
}

// ============================================================================
// §12.5/§14.4 search quality and knowledge gaps
// ============================================================================

// SearchQualityOverview mirrors the backend read model.
export interface SearchQualityOverview {
  total_queries: number
  zero_result_queries: number
  zero_result_rate: number
  permission_filtered_total: number
  clicked_queries: number
  average_duration_ms: number
  window_days: number
}

// FrequentQuery is one row of the hot / zero-result lists.
export interface FrequentQuery {
  query: string
  occurrences: number
}

// KnowledgeGap is one clustered missing-knowledge signal.
export interface KnowledgeGap {
  id: string
  query_normalized: string
  query: string
  source: string
  occurrences: number
  last_seen_at: string
  status: 'open' | 'acknowledged' | 'closed' | string
  assignee_id?: string
  suggestion?: string
  resolved_knowledge_id?: string
}

interface SearchQualityResponse {
  success: boolean
  data: SearchQualityOverview
}

interface FrequentQueryResponse {
  success: boolean
  data: FrequentQuery[]
}

interface KnowledgeGapListResponse {
  success: boolean
  data: KnowledgeGap[]
}

interface KnowledgeGapResponse {
  success: boolean
  data: KnowledgeGap
}

// GET /api/v1/search-quality — the §12.5 dashboard numbers.
export function getSearchQuality(windowDays = 30) {
  return get<SearchQualityResponse>(`/api/v1/search-quality?window_days=${windowDays}`)
}

// GET /api/v1/search-quality/frequent-queries — hot or zero-result queries.
export function getFrequentQueries(options: { windowDays?: number; zeroOnly?: boolean; limit?: number } = {}) {
  const params = new URLSearchParams({
    window_days: String(options.windowDays ?? 30),
    zero_only: String(options.zeroOnly ?? false),
    limit: String(options.limit ?? 20),
  })
  return get<FrequentQueryResponse>(`/api/v1/search-quality/frequent-queries?${params.toString()}`)
}

// GET /api/v1/knowledge-gaps — the gap list.
export function listKnowledgeGaps(status?: string) {
  const query = status ? `?status=${encodeURIComponent(status)}` : ''
  return get<KnowledgeGapListResponse>(`/api/v1/knowledge-gaps${query}`)
}

// PATCH /api/v1/knowledge-gaps/:gap_id — assign or close a gap.
export function updateKnowledgeGap(
  gapId: string,
  payload: { status?: string; assignee_id?: string; resolved_knowledge_id?: string },
) {
  return patch<KnowledgeGapResponse>(
    `/api/v1/knowledge-gaps/${encodeURIComponent(gapId)}`,
    payload,
  )
}

// ============================================================================
// §2.6 revocation ledger
// ============================================================================

// RevocationPropagation is one outlet row of a revocation sweep.
export interface RevocationPropagation {
  id: string
  knowledge_id: string
  subject_id: string
  trigger: string
  outlet: string
  outcome: 'invalidated' | 'not_applicable' | 'pending' | string
  permission_version: number
  created_at: string
}

interface RevocationLedgerResponse {
  success: boolean
  data: RevocationPropagation[]
}

// GET /api/v1/knowledge/:id/acl/revocations — the §2.6 outlet ledger.
export function listRevocationLedger(knowledgeId: string) {
  return get<RevocationLedgerResponse>(
    `/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/acl/revocations`,
  )
}

// ============================================================================
// §2.3 move preview
// ============================================================================

// MoveAccessPreview is the "who gains / who loses access" projection.
export interface MoveAccessPreview {
  knowledge_id: string
  source_kb_id: string
  target_kb_id: string
  retained_subject_ids: string[]
  gained_subject_ids: string[]
  lost_subject_ids: string[]
  reset_grant_count: number
  requires_confirmation: boolean
}

interface MoveAccessPreviewResponse {
  success: boolean
  data: MoveAccessPreview
}

// GET /api/v1/knowledge/:id/acl/move-preview — preview a cross-space move.
export function previewKnowledgeMove(knowledgeId: string, targetKbId: string) {
  const query = `?target_kb_id=${encodeURIComponent(targetKbId)}`
  return get<MoveAccessPreviewResponse>(
    `/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/acl/move-preview${query}`,
  )
}

// ============================================================================
// §3.5 parse quality
// ============================================================================

// ParseQualityScore mirrors the backend per-document score row.
export interface ParseQualityScore {
  knowledge_id: string
  score: number
  grade?: string
  below_threshold?: boolean
  dimensions?: Array<{ key: string; score: number; weight: number; contribution: number }>
}

interface ParseQualityResponse {
  success: boolean
  data: ParseQualityScore
}

interface ParseQualityQueueResponse {
  success: boolean
  data: ParseQualityScore[]
}

// GET /api/v1/knowledge/:id/parse-quality — one document's score.
export function getParseQuality(knowledgeId: string) {
  return get<ParseQualityResponse>(
    `/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/parse-quality`,
  )
}

// GET /api/v1/parse-quality/queue — the parse-exception queue.
export function listParseQualityQueue(limit = 50) {
  return get<ParseQualityQueueResponse>(`/api/v1/parse-quality/queue?limit=${limit}`)
}

// POST /api/v1/knowledge/:id/parse-quality/recompute — recompute and persist.
export function recomputeParseQuality(knowledgeId: string) {
  return post<ParseQualityResponse>(
    `/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/parse-quality/recompute`,
    {},
  )
}

// ============================================================================
// §8.4 schema proposals
// ============================================================================

// SchemaProposal mirrors the backend proposal row (subset the console needs).
export interface SchemaProposal {
  id: string
  status: string
  domain?: string
  name?: string
  created_by?: string
  created_at?: string
  compatibility_report?: Record<string, unknown>
}

interface SchemaProposalListResponse {
  success: boolean
  data: SchemaProposal[]
}

interface SchemaProposalResponse {
  success: boolean
  data: SchemaProposal
}

// GET /api/v1/ontology/proposals — schema proposals.
export function listSchemaProposals(status?: string) {
  const query = status ? `?status=${encodeURIComponent(status)}` : ''
  return get<SchemaProposalListResponse>(`/api/v1/ontology/proposals${query}`)
}

// POST /api/v1/ontology/proposals/:id/approve
export function approveSchemaProposal(id: string) {
  return post<SchemaProposalResponse>(
    `/api/v1/ontology/proposals/${encodeURIComponent(id)}/approve`,
    {},
  )
}

// POST /api/v1/ontology/proposals/:id/reject
export function rejectSchemaProposal(id: string, reason?: string) {
  return post<SchemaProposalResponse>(
    `/api/v1/ontology/proposals/${encodeURIComponent(id)}/reject`,
    { reason: reason || '' },
  )
}

// ============================================================================
// §22.5 approval flow configuration (per space)
// ============================================================================

// ApprovalFlowNodePayload is one node of a flow definition.
export interface ApprovalFlowNodePayload {
  seq?: number
  name?: string
  mode?: 'or' | 'and'
  assignee_kind?: 'role' | 'users'
  assignee_role?: string
  assignee_ids?: string[]
}

// ApprovalFlowPayload is the body of PUT /approval-flows/:kb_id.
export interface ApprovalFlowPayload {
  mode?: 'single' | 'dual'
  sla_hours?: number
  reminder_hours?: number
  escalate_role?: string
  enabled?: boolean
  rules?: Record<string, unknown>
  nodes?: ApprovalFlowNodePayload[]
}

// ApprovalResolution is the dry-run result of the routing rules.
export interface ApprovalResolution {
  risk_level: string
  mode: string
  quorum: number
  flow_version: number
  require_distinct_reviewer_roles: boolean
  reasons?: string[]
}

interface ApprovalFlowResponse {
  success: boolean
  data: Record<string, unknown>
}

interface ApprovalResolutionResponse {
  success: boolean
  data: ApprovalResolution
}

// GET /api/v1/approval-flows/:kb_id — the space's flow configuration.
export function getApprovalFlow(kbId: string) {
  return get<ApprovalFlowResponse>(`/api/v1/approval-flows/${encodeURIComponent(kbId)}`)
}

// PUT /api/v1/approval-flows/:kb_id — save the configuration and its chain.
export function saveApprovalFlow(kbId: string, payload: ApprovalFlowPayload) {
  return put<ApprovalFlowResponse>(`/api/v1/approval-flows/${encodeURIComponent(kbId)}`, payload)
}

// GET /api/v1/approval-flows/:kb_id/resolve — dry-run the chain.
export function resolveApprovalFlow(kbId: string, riskLevel: ApprovalRiskLevel = 'low') {
  const query = `?risk_level=${encodeURIComponent(riskLevel)}`
  return get<ApprovalResolutionResponse>(
    `/api/v1/approval-flows/${encodeURIComponent(kbId)}/resolve${query}`,
  )
}
