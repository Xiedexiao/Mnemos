package types

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 多级审批链（KnowAxis V3.0 §6.3 审批模式）。
//
// v1 的 approval_tasks 只有"攒够 N 票"这一种语义，无法表达：
//   - 或签：这一层只要有人签就过（值班经理池）
//   - 会签：这一层必须全员签（法规类内容需质量+法务同时确认）
//   - 逐级：先 Owner、再质量、最后法务，前一层过了下一层才激活
//   - 加签：在途发现需要某位专家补签
//   - 转签：当前审批人把这一票转给别人
//   - 委托：休假时由代理人代签
//
// 三层模型：flow_nodes 是模板，task_nodes 是提交时实例化的在途节点，
// votes 是每一票。加签/转签只动 task_nodes，定义不受影响。
// ============================================================================

// ApprovalNodeMode is how one node collects its approvals.
type ApprovalNodeMode string

const (
	// ApprovalNodeModeOr 或签：任一审批人通过即该节点通过。
	ApprovalNodeModeOr ApprovalNodeMode = "or"
	// ApprovalNodeModeAnd 会签：全部审批人通过才算该节点通过。
	ApprovalNodeModeAnd ApprovalNodeMode = "and"
)

// Valid reports whether the mode is known.
func (m ApprovalNodeMode) Valid() bool {
	return m == ApprovalNodeModeOr || m == ApprovalNodeModeAnd
}

// ApprovalNodeStatus is the state of an instantiated node.
type ApprovalNodeStatus string

const (
	// ApprovalNodeStatusPending 前序节点未完成，尚未激活。
	ApprovalNodeStatusPending ApprovalNodeStatus = "pending"
	// ApprovalNodeStatusActive 当前应签。
	ApprovalNodeStatusActive ApprovalNodeStatus = "active"
	// ApprovalNodeStatusApproved 该节点已通过。
	ApprovalNodeStatusApproved ApprovalNodeStatus = "approved"
	// ApprovalNodeStatusRejected 该节点被驳回。
	ApprovalNodeStatusRejected ApprovalNodeStatus = "rejected"
	// ApprovalNodeStatusSkipped 该节点被跳过（无人可签 / 流程提前结束）。
	ApprovalNodeStatusSkipped ApprovalNodeStatus = "skipped"
)

// Valid reports whether the status is known.
func (s ApprovalNodeStatus) Valid() bool {
	switch s {
	case ApprovalNodeStatusPending, ApprovalNodeStatusActive, ApprovalNodeStatusApproved,
		ApprovalNodeStatusRejected, ApprovalNodeStatusSkipped:
		return true
	default:
		return false
	}
}

// ApprovalFlowNode is one template node of a space's approval flow.
//
// Assignees are resolved at submission, not stored as a fixed list: a role-based
// node means "whoever holds this role when the task is submitted", so a staffing
// change mid-flight does not silently retarget an active approval.
type ApprovalFlowNode struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	FlowID   string `json:"flow_id" gorm:"type:varchar(36);not null;index:idx_approval_flow_nodes_flow,priority:1"`
	// Seq is the approval order, starting at 1.
	Seq  int    `json:"seq" gorm:"not null;uniqueIndex:uq_approval_flow_nodes,priority:2"`
	Name string `json:"name" gorm:"type:varchar(64);not null;default:''"`
	Mode string `json:"mode" gorm:"type:varchar(8);not null;default:'or'"`
	// AssigneeKind is "role" or "users".
	AssigneeKind string `json:"assignee_kind" gorm:"type:varchar(16);not null;default:'role'"`
	// AssigneeRole is the tenant role whose holders sign this node.
	AssigneeRole string `json:"assignee_role" gorm:"type:varchar(32);not null;default:'admin'"`
	// AssigneeIDs is the explicit approver list when AssigneeKind is "users".
	AssigneeIDs JSON      `json:"assignee_ids" gorm:"type:json;not null;default:'[]'"`
	CreatedAt   time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (ApprovalFlowNode) TableName() string { return "approval_flow_nodes" }

// BeforeCreate assigns an ID before persistence.
func (n *ApprovalFlowNode) BeforeCreate(_ *gorm.DB) error {
	if n.ID == "" {
		n.ID = uuid.NewString()
	}
	return nil
}

// ApprovalTaskNode is one instantiated node of an in-flight task.
type ApprovalTaskNode struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	TaskID   string `json:"task_id" gorm:"type:varchar(36);not null;index:idx_approval_task_nodes_task,priority:1"`
	Seq      int    `json:"seq" gorm:"not null;uniqueIndex:uq_approval_task_nodes,priority:2"`
	Name     string `json:"name" gorm:"type:varchar(64);not null;default:''"`
	Mode     string `json:"mode" gorm:"type:varchar(8);not null;default:'or'"`
	// ApproverIDs is the resolved approver set for this node. 加签 appends,
	// 转签 replaces one entry.
	ApproverIDs JSON `json:"approver_ids" gorm:"type:json;not null;default:'[]'"`
	// Status is the node state.
	Status string `json:"status" gorm:"type:varchar(16);not null;default:'pending'"`
	// RequiredCount is how many approvals the node needs. For 或签 it is 1; for
	// 会签 it is the approver count, and 加签 increments it so a newly added
	// approver must also sign.
	RequiredCount int        `json:"required_count" gorm:"not null;default:1"`
	DecidedAt     *time.Time `json:"decided_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// TableName pins the GORM table name.
func (ApprovalTaskNode) TableName() string { return "approval_task_nodes" }

// BeforeCreate assigns an ID before persistence.
func (n *ApprovalTaskNode) BeforeCreate(_ *gorm.DB) error {
	if n.ID == "" {
		n.ID = uuid.NewString()
	}
	return nil
}

// ApproverIDsList decodes the approver list; a bad blob yields an empty list so
// a corrupt row cannot pin a node forever.
func (n *ApprovalTaskNode) ApproverIDsList() []string {
	if n == nil || len(n.ApproverIDs) == 0 {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(n.ApproverIDs, &ids); err != nil {
		return nil
	}
	return ids
}

// ApprovalVote is one recorded vote on one node.
type ApprovalVote struct {
	ID         string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID   uint64 `json:"tenant_id" gorm:"not null"`
	TaskID     string `json:"task_id" gorm:"type:varchar(36);not null;index:idx_approval_votes_task,priority:1"`
	NodeID     string `json:"node_id" gorm:"type:varchar(36);not null;index:idx_approval_votes_node"`
	ApproverID string `json:"approver_id" gorm:"type:varchar(36);not null"`
	// DelegateOf records whose authority this vote used (empty for a direct
	// vote). A delegated vote is attributed to the delegate for identity but
	// keeps the delegator for the four-eyes check.
	DelegateOf string    `json:"delegate_of" gorm:"type:varchar(36);not null;default:''"`
	Decision   string    `json:"decision" gorm:"type:varchar(16);not null"`
	Comment    string    `json:"comment" gorm:"type:text;not null;default:''"`
	CreatedAt  time.Time `json:"created_at" gorm:"index:idx_approval_votes_task,priority:2"`
}

// TableName pins the GORM table name.
func (ApprovalVote) TableName() string { return "approval_votes" }

// BeforeCreate assigns an ID before persistence.
func (v *ApprovalVote) BeforeCreate(_ *gorm.DB) error {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	return nil
}

// ApprovalDelegation lets a delegate vote on behalf of a delegator while it is
// live (§6.3 委托：休假代理).
type ApprovalDelegation struct {
	ID          string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID    uint64     `json:"tenant_id" gorm:"not null"`
	DelegatorID string     `json:"delegator_id" gorm:"type:varchar(36);not null"`
	DelegateID  string     `json:"delegate_id" gorm:"type:varchar(36);not null"`
	StartsAt    time.Time  `json:"starts_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	Reason      string     `json:"reason" gorm:"type:varchar(255);not null;default:''"`
	RevokedAt   *time.Time `json:"revoked_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// TableName pins the GORM table name.
func (ApprovalDelegation) TableName() string { return "approval_delegations" }

// BeforeCreate assigns an ID before persistence.
func (d *ApprovalDelegation) BeforeCreate(_ *gorm.DB) error {
	if d.ID == "" {
		d.ID = uuid.NewString()
	}
	return nil
}

// Active reports whether the delegation is usable at the given instant.
func (d *ApprovalDelegation) Active(now time.Time) bool {
	if d == nil || d.RevokedAt != nil {
		return false
	}
	if now.Before(d.StartsAt) {
		return false
	}
	if d.ExpiresAt != nil && now.After(*d.ExpiresAt) {
		return false
	}
	return true
}

// ApprovalChainState is the resolved view of one task's chain: what is active,
// who has voted, and whether the whole chain is finished.
type ApprovalChainState struct {
	// TaskID the state belongs to.
	TaskID string `json:"task_id"`
	// Nodes in approval order with their live status.
	Nodes []*ApprovalTaskNode `json:"nodes"`
	// Votes recorded so far, oldest first.
	Votes []*ApprovalVote `json:"votes"`
	// ActiveNodeSeq is the sequence number currently accepting votes; 0 when the
	// chain is finished.
	ActiveNodeSeq int `json:"active_node_seq"`
	// Finished reports whether the chain reached a terminal outcome.
	Finished bool `json:"finished"`
	// Approved reports the overall verdict once Finished is true.
	Approved bool `json:"approved"`
}

// ActiveNode returns the node currently accepting votes, or nil.
func (s *ApprovalChainState) ActiveNode() *ApprovalTaskNode {
	if s == nil {
		return nil
	}
	for _, node := range s.Nodes {
		if node != nil && node.Seq == s.ActiveNodeSeq && s.ActiveNodeSeq > 0 {
			return node
		}
	}
	return nil
}

// NodeByID returns one node, or nil.
func (s *ApprovalChainState) NodeByID(nodeID string) *ApprovalTaskNode {
	if s == nil {
		return nil
	}
	for _, node := range s.Nodes {
		if node != nil && node.ID == nodeID {
			return node
		}
	}
	return nil
}
