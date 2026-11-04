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

// ============================================================================
// §7.2 change requests (变更单)
// ============================================================================

// CHANGE_REQUEST_STATUSES mirrors the backend state machine.
export const CHANGE_REQUEST_STATUSES = [
  'draft',
  'analyzing',
  'in_approval',
  'scheduled',
  'publishing',
  'verifying',
  'closed',
  'rolled_back',
] as const

export type ChangeRequestStatus = (typeof CHANGE_REQUEST_STATUSES)[number]

// ChangeItem is one knowledge entry in the change set.
export interface ChangeItem {
  id?: string
  cr_id?: string
  knowledge_id: string
  item_status: 'pending' | 'applied' | 'failed' | 'skipped' | string
  replaced_by_id?: string
}

// ImpactTask is one affected-object task (§7.6).
export interface ImpactTask {
  id: string
  cr_id: string
  target_type: string
  target_id: string
  status: 'open' | 'done' | 'waived' | string
  priority?: string
  note?: string
}

// ChangeRequest mirrors the backend aggregate.
export interface ChangeRequest {
  id: string
  cr_number: string
  title: string
  reason: string
  change_type: string
  status: ChangeRequestStatus | string
  risk_level: string
  effective_at?: string | null
  rollback_plan?: string
  approval_task_id?: string
  approval_receipt_id?: string
  verification_result?: string
  notified_at?: string | null
  created_by: string
  created_at?: string
  closed_at?: string | null
  items?: ChangeItem[]
  impact_tasks?: ImpactTask[]
}

interface ChangeRequestListResponse {
  success: boolean
  data: ChangeRequest[]
  total?: number
}

interface ChangeRequestResponse {
  success: boolean
  data: ChangeRequest
}

// GET /api/v1/change-requests — list change requests.
export function listChangeRequests(status?: string) {
  const query = status ? `?status=${encodeURIComponent(status)}` : ''
  return get<ChangeRequestListResponse>(`/api/v1/change-requests${query}`)
}

// GET /api/v1/change-requests/:id — one change request with its items and tasks.
export function getChangeRequest(id: string) {
  return get<ChangeRequestResponse>(`/api/v1/change-requests/${encodeURIComponent(id)}`)
}

// POST /api/v1/change-requests/:id/analyze — freeze the impact analysis.
export function analyzeChangeRequest(id: string) {
  return post<ChangeRequestResponse>(`/api/v1/change-requests/${encodeURIComponent(id)}/analyze`, {})
}

// POST /api/v1/change-requests/:id/submit — create the §6.2 approval task.
export function submitChangeRequest(id: string) {
  return post<ChangeRequestResponse>(`/api/v1/change-requests/${encodeURIComponent(id)}/submit`, {})
}

// POST /api/v1/change-requests/:id/schedule — requires an approved receipt.
export function scheduleChangeRequest(id: string, effectiveAt?: string) {
  return post<ChangeRequestResponse>(`/api/v1/change-requests/${encodeURIComponent(id)}/schedule`, {
    effective_at: effectiveAt || null,
  })
}

// POST /api/v1/change-requests/:id/publish — apply items by change type.
export function publishChangeRequest(id: string) {
  return post<ChangeRequestResponse>(`/api/v1/change-requests/${encodeURIComponent(id)}/publish`, {})
}

// POST /api/v1/change-requests/:id/verify — close gate condition ②.
export function verifyChangeRequest(id: string, result: string) {
  return post<ChangeRequestResponse>(`/api/v1/change-requests/${encodeURIComponent(id)}/verify`, { result })
}

// POST /api/v1/change-requests/:id/notify — close gate condition ④.
export function notifyChangeRequest(id: string) {
  return post<ChangeRequestResponse>(`/api/v1/change-requests/${encodeURIComponent(id)}/notify`, {})
}

// POST /api/v1/change-requests/:id/close — all four gates must pass.
export function closeChangeRequest(id: string) {
  return post<ChangeRequestResponse>(`/api/v1/change-requests/${encodeURIComponent(id)}/close`, {})
}

// POST /api/v1/change-requests/:id/rollback
export function rollbackChangeRequest(id: string) {
  return post<ChangeRequestResponse>(`/api/v1/change-requests/${encodeURIComponent(id)}/rollback`, {})
}

// POST /api/v1/change-requests/:id/impact-tasks/:task_id — close or waive a task.
export function updateImpactTask(
  id: string,
  taskId: string,
  payload: { status: 'open' | 'done' | 'waived'; note?: string },
) {
  return post(
    `/api/v1/change-requests/${encodeURIComponent(id)}/impact-tasks/${encodeURIComponent(taskId)}`,
    payload,
  )
}

// ============================================================================
// §4.5 knowledge relations
// ============================================================================

// KNOWLEDGE_RELATION_TYPES mirrors the backend KnowledgeRelationType enum.
export const KNOWLEDGE_RELATION_TYPES = [
  'based_on',
  'replaces',
  'supplements',
  'conflicts',
  'prerequisite',
  'responsible',
  'related',
] as const

export type KnowledgeRelationType = (typeof KNOWLEDGE_RELATION_TYPES)[number]

// KnowledgeRelation is one stored directed edge.
export interface KnowledgeRelation {
  id: string
  from_knowledge_id: string
  to_knowledge_id: string
  relation_type: string
  direction: 'forward' | 'reverse' | string
  origin: 'manual' | 'ai_suggested' | 'migration' | string
  note?: string
  created_by?: string
}

// KnowledgeRelationView is one edge as the reading page renders it.
// peerId/relationType are camelCase because this module normalises the wire
// shape once, so components never branch on snake_case.
export interface KnowledgeRelationView {
  relation?: KnowledgeRelation
  peerId: string
  relationType: string
  outbound: boolean
  origin: string
  note?: string
}

interface KnowledgeRelationListResponse {
  success: boolean
  data: Array<{
    relation?: KnowledgeRelation
    peer_id: string
    relation_type: string
    outbound: boolean
    origin: string
    note?: string
  }>
}

// GET /api/v1/knowledge/:id/relations — links and backlinks of one document.
export async function listKnowledgeRelations(knowledgeId: string) {
  const response = await get<KnowledgeRelationListResponse>(
    `/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/relations`,
  )
  // The backend serialises snake_case; the panel reads camelCase.
  const rows = (response?.data || []).map((row) => ({
    relation: row.relation,
    peerId: row.peer_id,
    relationType: row.relation_type,
    outbound: row.outbound,
    origin: row.origin,
    note: row.note,
  }));
  return { ...response, data: rows } as {
    success: boolean
    data: KnowledgeRelationView[]
  };
}

// POST /api/v1/knowledge/:id/relations — create a typed edge.
export function linkKnowledgeRelation(
  knowledgeId: string,
  payload: { to_knowledge_id: string; relation_type: KnowledgeRelationType; note?: string },
) {
  return post(
    `/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/relations`,
    payload,
  );
}

// DELETE /api/v1/knowledge/:id/relations — remove an edge in both directions.
export function unlinkKnowledgeRelation(
  knowledgeId: string,
  peerId: string,
  relationType: string,
) {
  const params = new URLSearchParams({
    to_knowledge_id: peerId,
    relation_type: relationType,
  });
  return del(
    `/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/relations?${params.toString()}`,
  );
}

// GET /api/v1/knowledge/:id/relations/delete-impact — links a delete would cut.
export function getRelationDeleteImpact(knowledgeId: string) {
  return get<{ success: boolean; data: { link_count: number } }>(
    `/api/v1/knowledge/${encodeURIComponent(knowledgeId)}/relations/delete-impact`,
  );
}

// ============================================================================
// §15.3 personal center
// ============================================================================

// PersonalCenter mirrors the backend aggregate.
export interface PersonalCenter {
  overview: {
    user_id: string
    favorite_count: number
    pending_review_count: number
    latest_announcement_count: number
  }
  knowledge: Array<{
    id: string
    title: string
    knowledge_base_id: string
    lifecycle_status: string
    parse_status: string
    updated_at: string
  }>
  reviews: Array<{
    task_id: string
    knowledge_id: string
    title: string
    status: string
    risk_level: string
    sla_due: string
    created_at: string
  }>
  favorites: Array<{
    resource_type: string
    resource_id: string
    label: string
    visible: boolean
    created_at: string
  }>
}

interface PersonalCenterResponse {
  success: boolean
  data: PersonalCenter
}

// GET /api/v1/me/center — the signed-in user's personal center.
export function getPersonalCenter() {
  return get<PersonalCenterResponse>('/api/v1/me/center')
}

// ============================================================================
// §16.1 operations dashboard
// ============================================================================

// OperationsDashboard mirrors the backend aggregate.
export interface OperationsDashboard {
  overview: {
    knowledge_total: number
    knowledge_by_status: Record<string, number>
    knowledge_base_count: number
    chunk_total: number
  }
  governance: {
    pending_reviews: number
    pending_review_cycles: number
    expired: number
    conflicts: number
    open_quality_tasks: number
    open_impact_tasks: number
    open_knowledge_gaps: number
    pending_field_migrations: number
    acl_ceiling_violations: number
    pending_change_signals: number
  }
  delivery: {
    approved_last_30_days: number
    rejected_last_30_days: number
    overdue_pending: number
    average_decision_hours: number
    four_eyes_violations: number
  }
  search_quality: {
    total_queries: number
    zero_result_queries: number
    zero_result_rate: number
    permission_filtered_total: number
    average_duration_ms: number
  }
  trends: Array<{
    day: string
    knowledge_added: number
    reviews_submitted: number
    queries: number
  }>
  contributors: Array<{
    user_id: string
    reviews_completed: number
    submitted: number
    answers: number
    questions: number
    score: number
  }>
  cost: {
    prompt_tokens: number
    completion_tokens: number
    total_tokens: number
    call_count: number
    by_model: Record<string, number>
    by_scene: Record<string, number>
    allocations: Array<{
      knowledge_base_id: string
      knowledge_base_name: string
      scene: string
      prompt_tokens: number
      completion_tokens: number
      total_tokens: number
      call_count: number
    }>
    storage_by_space: Array<{
      knowledge_base_id: string
      knowledge_base_name: string
      storage_bytes: number
      knowledge_count: number
      chunk_count: number
    }>
    unallocated_tokens: number
    total_storage_bytes: number
    total_chunks: number
  }
  observability: {
    zero_result_queries: number
    refusal_rate: number
    permission_blocked: number
    average_latency_ms: number
    slow_queries: number
    overdue_approvals: number
  }
  health?: Record<string, unknown>
  window_days: number
}

interface OperationsDashboardResponse {
  success: boolean
  data: OperationsDashboard
}

// GET /api/v1/operations/dashboard — the §16.1 operations view.
export function getOperationsDashboard(windowDays = 30) {
  return get<OperationsDashboardResponse>(`/api/v1/operations/dashboard?window_days=${windowDays}`)
}

// ============================================================================
// §15.6 Q&A community
// ============================================================================

// CommunityQuestion mirrors the backend question row.
export interface CommunityQuestion {
  id: string
  title: string
  body: string
  knowledge_id?: string
  expert_id?: string
  bounty: number
  status: 'open' | 'answered' | 'resolved' | 'closed' | string
  accepted_answer_id?: string
  asked_by: string
  created_at?: string
  answers?: CommunityAnswer[]
}

// CommunityAnswer is one reply.
export interface CommunityAnswer {
  id: string
  question_id: string
  body: string
  answered_by: string
  votes: number
  accepted: boolean
  created_at?: string
}

interface CommunityQuestionListResponse {
  success: boolean
  data: CommunityQuestion[]
}

interface CommunityQuestionResponse {
  success: boolean
  data: CommunityQuestion
}

interface CommunityAnswerResponse {
  success: boolean
  data: CommunityAnswer
}

// GET /api/v1/community/questions — the community feed.
export function listCommunityQuestions(status?: string) {
  const query = status ? `?status=${encodeURIComponent(status)}` : ''
  return get<CommunityQuestionListResponse>(`/api/v1/community/questions${query}`)
}

// GET /api/v1/community/questions/:id — one question with its answers.
export function getCommunityQuestion(id: string) {
  return get<CommunityQuestionResponse>(
    `/api/v1/community/questions/${encodeURIComponent(id)}`,
  )
}

// POST /api/v1/community/questions — ask.
export function askCommunityQuestion(payload: {
  title: string
  body: string
  knowledge_id?: string
  expert_id?: string
  bounty?: number
}) {
  return post<CommunityQuestionResponse>('/api/v1/community/questions', payload)
}

// POST /api/v1/community/questions/:id/answers — answer.
export function answerCommunityQuestion(id: string, body: string) {
  return post<CommunityAnswerResponse>(
    `/api/v1/community/questions/${encodeURIComponent(id)}/answers`,
    { body },
  )
}

// POST /api/v1/community/questions/:id/accept — adopt an answer (asker only).
export function acceptCommunityAnswer(questionId: string, answerId: string) {
  return post<CommunityQuestionResponse>(
    `/api/v1/community/questions/${encodeURIComponent(questionId)}/accept`,
    { answer_id: answerId },
  )
}

// POST /api/v1/community/answers/:answer_id/vote — mark helpful.
export function voteCommunityAnswer(answerId: string) {
  return post<CommunityAnswerResponse>(
    `/api/v1/community/answers/${encodeURIComponent(answerId)}/vote`,
    {},
  )
}

// ============================================================================
// §15.5 learning certification
// ============================================================================

// LearningCertificate mirrors the backend credential.
export interface LearningCertificate {
  id: string
  user_id: string
  path_id: string
  stage: string
  serial: string
  status: 'active' | 'revoked' | 'expired' | string
  issued_at: string
  expires_at?: string | null
  revoked_reason?: string
}

// LearningCertification is the result of a completion attempt.
export interface LearningCertification {
  certificate?: LearningCertificate
  eligible: boolean
  completed_steps: number
  total_steps: number
  reason?: string
}

interface CertificateListResponse {
  success: boolean
  data: LearningCertificate[]
}

interface CertificationResponse {
  success: boolean
  data: LearningCertification
}

// GET /api/v1/learning-certificates — the caller's certificates.
export function listLearningCertificates() {
  return get<CertificateListResponse>('/api/v1/learning-certificates')
}

// POST /api/v1/learning-paths/:path_id/certify — request certification.
export function certifyLearningPath(pathId: string, validMonths = 12) {
  return post<CertificationResponse>(
    `/api/v1/learning-paths/${encodeURIComponent(pathId)}/certify?valid_months=${validMonths}`,
    {},
  )
}

// ============================================================================
// §7.3 change signals
// ============================================================================

// CHANGE_SIGNAL_TYPES mirrors the backend enum.
export const CHANGE_SIGNAL_TYPES = [
  'internal_event',
  'source_system',
  'external_subscription',
  'dependency',
  'user_feedback',
  'conflict',
  'connector_health',
] as const

export type ChangeSignalType = (typeof CHANGE_SIGNAL_TYPES)[number]

// ChangeSignal is one collected update signal.
export interface ChangeSignal {
  id: string
  signal_type: string
  source_id: string
  knowledge_id?: string
  summary: string
  confidence: 'high' | 'mid' | 'low' | string
  default_action: string
  status: 'pending' | 'converted' | 'dismissed' | string
  change_request_id?: string
  created_at?: string
}

interface ChangeSignalListResponse {
  success: boolean
  data: ChangeSignal[]
}

interface ChangeSignalResponse {
  success: boolean
  data: ChangeSignal
}

// GET /api/v1/change-signals — the untriaged signal queue.
export function listChangeSignals(status = 'pending', signalType = '') {
  const params = new URLSearchParams({ status })
  if (signalType) params.set('signal_type', signalType)
  return get<ChangeSignalListResponse>(`/api/v1/change-signals?${params.toString()}`)
}

// POST /api/v1/change-signals/:id/convert — turn a signal into a CR draft.
export function convertChangeSignal(id: string) {
  return post<ChangeSignalResponse>(
    `/api/v1/change-signals/${encodeURIComponent(id)}/convert`,
    {},
  )
}

// POST /api/v1/change-signals/:id/dismiss — mark a false positive.
export function dismissChangeSignal(id: string) {
  return post<ChangeSignalResponse>(
    `/api/v1/change-signals/${encodeURIComponent(id)}/dismiss`,
    {},
  )
}

// ============================================================================
// §15.5 learning tasks
// ============================================================================

// LearningTask is one assigned study item.
export interface LearningTask {
  id: string
  knowledge_id: string
  path_id?: string
  title: string
  source: 'sop_change' | 'review_cycle' | 'certification' | 'manual' | string
  status: 'open' | 'done' | 'waived' | string
  due_at?: string | null
  completed_at?: string | null
}

interface LearningTaskListResponse {
  success: boolean
  data: LearningTask[]
}

interface LearningTaskResponse {
  success: boolean
  data: LearningTask
}

// GET /api/v1/learning-tasks — the caller's study queue.
export function listLearningTasks(status = '') {
  const query = status ? `?status=${encodeURIComponent(status)}` : ''
  return get<LearningTaskListResponse>(`/api/v1/learning-tasks${query}`)
}

// POST /api/v1/learning-tasks/:id/complete — mark done (assignee only).
export function completeLearningTask(id: string) {
  return post<LearningTaskResponse>(
    `/api/v1/learning-tasks/${encodeURIComponent(id)}/complete`,
    {},
  )
}

// ============================================================================
// §7.3 external subscriptions (regulation / standards feeds)
// ============================================================================

// ExternalSubscriptionFeed is one subscribed regulation/standards feed.
export interface ExternalSubscriptionFeed {
  id: string
  tenant_id: number
  name: string
  feed_url: string
  format: 'rss' | 'atom' | 'json' | 'auto' | string
  domain_tags: string[]
  knowledge_base_ids: string[]
  enabled: boolean
  check_interval_hours: number
  last_checked_at?: string | null
  last_success_at?: string | null
  last_error: string
  has_auth_header: boolean
  created_by: string
  created_at: string
  updated_at: string
}

// ExternalSubscriptionEntry is one entry already seen in a feed.
export interface ExternalSubscriptionEntry {
  id: string
  feed_id: string
  external_id: string
  title: string
  url: string
  published_at?: string | null
  matched_knowledge_count: number
  signal_count: number
  created_at: string
}

// ExternalSubscriptionSyncResult reports one feed check.
export interface ExternalSubscriptionSyncResult {
  feed_id: string
  fetched: number
  new_entries: number
  matched_knowledge: number
  signals: number
  unmatched_entries: number
  truncated: boolean
  synced_at: string
}

// ExternalSubscriptionFeedPayload is the create/update body. On update an empty
// field means "unchanged"; [] clears the list and auth_header "-" clears the
// stored secret.
export interface ExternalSubscriptionFeedPayload {
  name?: string
  feed_url?: string
  format?: string
  domain_tags?: string[]
  knowledge_base_ids?: string[]
  enabled?: boolean
  check_interval_hours?: number
  auth_header?: string
}

interface ExternalFeedListResponse {
  success: boolean
  data: ExternalSubscriptionFeed[]
}

interface ExternalFeedResponse {
  success: boolean
  data: ExternalSubscriptionFeed
}

interface ExternalEntryListResponse {
  success: boolean
  data: ExternalSubscriptionEntry[]
}

interface ExternalSyncResponse {
  success: boolean
  data: ExternalSubscriptionSyncResult
}

// GET /api/v1/external-subscriptions/feeds
export function listExternalFeeds(enabledOnly = false) {
  const query = enabledOnly ? '?enabled_only=true' : ''
  return get<ExternalFeedListResponse>(`/api/v1/external-subscriptions/feeds${query}`)
}

// POST /api/v1/external-subscriptions/feeds
export function createExternalFeed(payload: ExternalSubscriptionFeedPayload) {
  return post<ExternalFeedResponse>('/api/v1/external-subscriptions/feeds', payload)
}

// PUT /api/v1/external-subscriptions/feeds/:id
export function updateExternalFeed(id: string, payload: ExternalSubscriptionFeedPayload) {
  return put<ExternalFeedResponse>(
    `/api/v1/external-subscriptions/feeds/${encodeURIComponent(id)}`,
    payload,
  )
}

// DELETE /api/v1/external-subscriptions/feeds/:id
export function deleteExternalFeed(id: string) {
  return del<{ success: boolean }>(
    `/api/v1/external-subscriptions/feeds/${encodeURIComponent(id)}`,
  )
}

// POST /api/v1/external-subscriptions/feeds/:id/sync
export function syncExternalFeed(id: string) {
  return post<ExternalSyncResponse>(
    `/api/v1/external-subscriptions/feeds/${encodeURIComponent(id)}/sync`,
    {},
  )
}

// GET /api/v1/external-subscriptions/entries
export function listExternalEntries(feedId = '', limit = 50) {
  const params = new URLSearchParams({ limit: String(limit) })
  if (feedId) params.set('feed_id', feedId)
  return get<ExternalEntryListResponse>(
    `/api/v1/external-subscriptions/entries?${params.toString()}`,
  )
}

// ============================================================================
// §15.4 points, redemption and knowledge stars
// ============================================================================

export interface PointsLedgerEntry {
  id: string
  action: string
  points: number
  source_type: string
  source_id: string
  created_at: string
}

export interface PointsSummary {
  user_id: string
  balance: number
  level: 'bronze' | 'silver' | 'gold' | 'diamond' | 'expert' | string
  recent: PointsLedgerEntry[]
}

export interface RewardCatalogItem {
  id: string
  name: string
  description: string
  cost_points: number
  /** -1 means unlimited. */
  stock: number
  enabled: boolean
}

export interface RewardRedemption {
  id: string
  user_id?: string
  item_id: string
  item_name: string
  points_cost: number
  status: 'pending' | 'fulfilled' | 'rejected' | 'canceled' | string
  note: string
  handled_by?: string
  handled_at?: string | null
  created_at: string
}

export interface KnowledgeStarAward {
  id: string
  period_type: 'month' | 'quarter' | string
  period: string
  user_id: string
  rank: number
  score: number
  reason: string
  published: boolean
  awarded_at: string
}

interface PointsSummaryResponse {
  success: boolean
  data: PointsSummary
}

interface RewardCatalogResponse {
  success: boolean
  data: RewardCatalogItem[]
}

interface RewardRedemptionResponse {
  success: boolean
  data: RewardRedemption
}

interface RewardRedemptionListResponse {
  success: boolean
  data: RewardRedemption[]
}

interface KnowledgeStarListResponse {
  success: boolean
  data: KnowledgeStarAward[]
}

// GET /api/v1/me/points
export function getMyPoints(recentLimit = 20) {
  return get<PointsSummaryResponse>(`/api/v1/me/points?recent_limit=${recentLimit}`)
}

// GET /api/v1/rewards/catalog
export function listRewardCatalog(all = false) {
  return get<RewardCatalogResponse>(`/api/v1/rewards/catalog${all ? '?all=true' : ''}`)
}

// POST /api/v1/rewards/catalog/:id/redeem
export function redeemReward(itemId: string, note = '') {
  return post<RewardRedemptionResponse>(
    `/api/v1/rewards/catalog/${encodeURIComponent(itemId)}/redeem`,
    { note },
  )
}

// GET /api/v1/rewards/redemptions
export function listMyRedemptions(status = '') {
  const query = status ? `?status=${encodeURIComponent(status)}` : ''
  return get<RewardRedemptionListResponse>(`/api/v1/rewards/redemptions${query}`)
}

// POST /api/v1/rewards/redemptions/:id/cancel
export function cancelRedemption(id: string) {
  return post<RewardRedemptionResponse>(
    `/api/v1/rewards/redemptions/${encodeURIComponent(id)}/cancel`,
    {},
  )
}

// GET /api/v1/knowledge-stars — the published public board.
export function listKnowledgeStars(limit = 20) {
  return get<KnowledgeStarListResponse>(`/api/v1/knowledge-stars?limit=${limit}`)
}

// ============================================================================
// §15.5 embedded contextual push
// ============================================================================

export interface EmbedPushPreference {
  user_id: string
  enabled: boolean
  updated_at?: string
}

export interface EmbedPushRule {
  id: string
  name: string
  system: string
  scene: string
  entity_type: string
  entity_value: string
  attributes: Record<string, string>
  tag_names: string[]
  knowledge_ids: string[]
  reason: string
  priority: number
  enabled: boolean
  created_by: string
}

export interface EmbedPushRuleStats {
  rule_id: string
  rule_name: string
  impressions: number
  clicks: number
  dismissals: number
}

export interface EmbedPushRulePayload {
  name: string
  system?: string
  scene?: string
  entity_type?: string
  entity_value?: string
  attributes?: Record<string, string>
  tag_names?: string[]
  knowledge_ids?: string[]
  reason?: string
  priority?: number
  enabled?: boolean
}

interface EmbedPushPreferenceResponse {
  success: boolean
  data: EmbedPushPreference
}

interface EmbedPushRuleListResponse {
  success: boolean
  data: EmbedPushRule[]
}

interface EmbedPushRuleResponse {
  success: boolean
  data: EmbedPushRule
}

interface EmbedPushRuleStatsResponse {
  success: boolean
  data: EmbedPushRuleStats[]
}

// GET /api/v1/embed/push/preferences
export function getEmbedPushPreference() {
  return get<EmbedPushPreferenceResponse>('/api/v1/embed/push/preferences')
}

// PUT /api/v1/embed/push/preferences
export function setEmbedPushPreference(enabled: boolean) {
  return put<EmbedPushPreferenceResponse>('/api/v1/embed/push/preferences', { enabled })
}

// GET /api/v1/embed/push/rules
export function listEmbedPushRules() {
  return get<EmbedPushRuleListResponse>('/api/v1/embed/push/rules')
}

// POST /api/v1/embed/push/rules
export function createEmbedPushRule(payload: EmbedPushRulePayload) {
  return post<EmbedPushRuleResponse>('/api/v1/embed/push/rules', payload)
}

// PUT /api/v1/embed/push/rules/:id
export function updateEmbedPushRule(id: string, payload: EmbedPushRulePayload) {
  return put<EmbedPushRuleResponse>(
    `/api/v1/embed/push/rules/${encodeURIComponent(id)}`,
    payload,
  )
}

// DELETE /api/v1/embed/push/rules/:id
export function deleteEmbedPushRule(id: string) {
  return del<{ success: boolean }>(`/api/v1/embed/push/rules/${encodeURIComponent(id)}`)
}

// GET /api/v1/embed/push/stats
export function getEmbedPushRuleStats(days = 30) {
  return get<EmbedPushRuleStatsResponse>(`/api/v1/embed/push/stats?days=${days}`)
}

// ============================================================================
// §2.1 ACL subject groups (grant targets beyond a single user)
// ============================================================================

// ACLSubjectGroup is one tenant-scoped group usable as an ACL subject.
export interface ACLSubjectGroup {
  id: string
  name: string
  description: string
  created_by: string
  member_count: number
  created_at: string
}

interface ACLGroupListResponse {
  success: boolean
  data: ACLSubjectGroup[]
}

interface ACLGroupResponse {
  success: boolean
  data: ACLSubjectGroup
}

interface ACLGroupMembersResponse {
  success: boolean
  data: string[]
}

// GET /api/v1/acl/groups
export function listACLGroups() {
  return get<ACLGroupListResponse>('/api/v1/acl/groups')
}

// POST /api/v1/acl/groups
export function createACLGroup(payload: { name: string; description?: string }) {
  return post<ACLGroupResponse>('/api/v1/acl/groups', payload)
}

// PUT /api/v1/acl/groups/:id
export function updateACLGroup(
  id: string,
  payload: { name?: string; description?: string },
) {
  return put<ACLGroupResponse>(`/api/v1/acl/groups/${encodeURIComponent(id)}`, payload)
}

// DELETE /api/v1/acl/groups/:id
export function deleteACLGroup(id: string) {
  return del<{ success: boolean }>(`/api/v1/acl/groups/${encodeURIComponent(id)}`)
}

// GET /api/v1/acl/groups/:id/members
export function listACLGroupMembers(id: string) {
  return get<ACLGroupMembersResponse>(
    `/api/v1/acl/groups/${encodeURIComponent(id)}/members`,
  )
}

// POST /api/v1/acl/groups/:id/members
export function addACLGroupMembers(id: string, userIds: string[]) {
  return post<{ success: boolean; data: { added: number } }>(
    `/api/v1/acl/groups/${encodeURIComponent(id)}/members`,
    { user_ids: userIds },
  )
}

// DELETE /api/v1/acl/groups/:id/members/:userId
export function removeACLGroupMember(id: string, userId: string) {
  return del<{ success: boolean }>(
    `/api/v1/acl/groups/${encodeURIComponent(id)}/members/${encodeURIComponent(userId)}`,
  )
}
