// Package business implements placeholder connectors for the business systems
// V3.0 §3.3 lists as synchronization sources: ERP, MES, PLM, QMS, OA, HR and
// CRM.
//
// 这些连接器是**占位实现**，不是能用的集成。它们存在的理由有三个：
//
//  1. 让平台能如实表达"目标形态"：产品设计要求公司级的业务系统同步，而
//     每个系统的接口（ERP 的工单表、PLM 的 ECN、QMS 的 CAPA）都取决于具体
//     部署方。没有对方的接口契约时，写一个"看起来能跑"的实现只会产出错误
//     数据。
//  2. 让"未实现"可见且可诊断：Validate 会返回一条明确的错误，说明缺什么、
//     怎么接，而不是"connector not found"或静默返回空结果。
//  3. 给通用接入留一个落点：内部 generic 类型已经能通过 HTTP+JSON 读取任意
//     业务系统（见 generic.go），占位类型把它按业务系统命名暴露出来。
//
// 关键约束：占位连接器**永不返回空结果**。一次"同步成功但 0 条"会被读成
// "这个系统里没有数据"，而真实原因是集成还没接。宁可让同步失败并说明原因。
package business

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
)

// Compile-time proof that *Connector satisfies the datasource.Connector contract.
var _ datasource.Connector = (*Connector)(nil)

// Compile-time proof that *Connector satisfies the error interface contract used
// by the sync service to detect "not implemented" rather than a transient fault.
var _ error = (*NotImplementedError)(nil)

// NotImplementedError explains that a business-system connector is planned but
// not implemented, and what a deployment needs to provide.
type NotImplementedError struct {
	// ConnectorType is the requested business system (erp/mes/plm/...).
	ConnectorType string
	// SystemName is the human label used in the message.
	SystemName string
	// Needs is what an implementation requires from the deployment.
	Needs string
}

// Error implements error.
func (e *NotImplementedError) Error() string {
	return fmt.Sprintf(
		"%s connector is planned but not implemented: %s. "+
			"Use the generic business API connector (type %q) with your system's "+
			"endpoint and field mapping, or contribute a dedicated connector",
		e.SystemName, e.Needs, types.ConnectorTypeBusinessAPI)
}

// systemProfile is the per-system description used in metadata and errors.
type systemProfile struct {
	// Type is the connector type identifier.
	Type string
	// Name is the display name.
	Name string
	// Description says what the connector is meant to sync.
	Description string
	// Needs names the interface contract a real implementation requires.
	Needs string
	// Priority orders the UI list.
	Priority int
}

// businessSystems is the §3.3 source list in display order.
//
// Needs 刻意写成"要什么契约"而不是笼统的"待实现"：部署方照着这句话就能
// 判断自己有没有条件接。
var businessSystems = []systemProfile{
	{
		Type:        types.ConnectorTypeERP,
		Name:        "ERP (企业资源计划)",
		Description: "同步工单、生产记录、停线事件与参数调整",
		Needs:       "ERP 的工单/生产记录只读接口（列表 + 按 updated_at 增量 + 业务键）",
		Priority:    20,
	},
	{
		Type:        types.ConnectorTypeMES,
		Name:        "MES (制造执行)",
		Description: "同步生产事件、停机恢复与工艺参数变更",
		Needs:       "MES 的生产事件/停机记录只读接口（事件时间戳 + 设备与工艺版本）",
		Priority:    21,
	},
	{
		Type:        types.ConnectorTypePLM,
		Name:        "PLM / PDM (产品生命周期)",
		Description: "同步图纸、BOM、ECR/ECO 与版本替代关系",
		Needs:       "PLM 的图纸/BOM/ECN 只读接口（版本号、生效日、替代关系）",
		Priority:    22,
	},
	{
		Type:        types.ConnectorTypeQMS,
		Name:        "QMS (质量管理)",
		Description: "同步 NCR/CAPA、异常、检验记录与供应商质量事件",
		Needs:       "QMS 的 NCR/CAPA 只读接口（状态变更事件 + 根因字段）",
		Priority:    23,
	},
	{
		Type:        types.ConnectorTypeOA,
		Name:        "OA (办公自动化)",
		Description: "同步制度、通知与流程审批记录",
		Needs:       "OA 的公文/制度只读接口（发文号、生效日、版本）",
		Priority:    24,
	},
	{
		Type:        types.ConnectorTypeHR,
		Name:        "HR (人力资源)",
		Description: "同步组织架构、岗位与任职变更",
		Needs:       "HR 的组织/岗位只读接口（组织树 + 变动事件；注意个人信息最小化）",
		Priority:    25,
	},
	{
		Type:        types.ConnectorTypeCRM,
		Name:        "CRM (客户关系)",
		Description: "同步客诉、服务记录与解决方案",
		Needs:       "CRM 的工单/客诉只读接口（客户脱敏字段 + 关闭事件）",
		Priority:    26,
	},
}

// Connector is the placeholder for one business system.
type Connector struct {
	// profile describes the system this instance stands for.
	profile systemProfile
}

// NewConnector builds the placeholder for one connector type.
//
// Returns nil for an unknown type so a bad registration fails loudly at wiring
// time instead of producing a connector that silently accepts anything.
func NewConnector(connectorType string) *Connector {
	for _, profile := range businessSystems {
		if profile.Type == connectorType {
			return &Connector{profile: profile}
		}
	}
	return nil
}

// Type returns the connector identifier.
func (c *Connector) Type() string {
	if c == nil {
		return ""
	}
	return c.profile.Type
}

// notImplemented renders the error every operation returns.
func (c *Connector) notImplemented() error {
	if c == nil {
		return fmt.Errorf("business connector is not configured")
	}
	return &NotImplementedError{
		ConnectorType: c.profile.Type,
		SystemName:    c.profile.Name,
		Needs:         c.profile.Needs,
	}
}

// Validate always fails for a placeholder.
//
// 这是刻意的：让配置阶段就暴露"这个源还接不了"，而不是等到第一次同步
// 才发现。错误里带上了通用接入方式的类型名，运维可以直接改用它。
func (c *Connector) Validate(_ context.Context, _ *types.DataSourceConfig) error {
	return c.notImplemented()
}

// ListResources always fails, for the same reason as Validate.
func (c *Connector) ListResources(
	_ context.Context, _ *types.DataSourceConfig, _ string,
) ([]types.Resource, error) {
	return nil, c.notImplemented()
}

// ResolveResourceAncestors always fails.
func (c *Connector) ResolveResourceAncestors(
	_ context.Context, _ *types.DataSourceConfig, _ []string,
) ([]string, error) {
	return nil, c.notImplemented()
}

// FetchAll always fails rather than returning an empty slice.
//
// 返回空切片会让同步记录显示"success, 0 items"，看起来像"源系统里没有内容"
// ——真实原因是没接。失败能让人查日志并看到原因。
func (c *Connector) FetchAll(
	_ context.Context, _ *types.DataSourceConfig, _ []string,
) ([]types.FetchedItem, error) {
	return nil, c.notImplemented()
}

// FetchIncremental always fails, for the same reason as FetchAll.
func (c *Connector) FetchIncremental(
	_ context.Context, _ *types.DataSourceConfig, _ *types.SyncCursor,
) ([]types.FetchedItem, *types.SyncCursor, error) {
	return nil, nil, c.notImplemented()
}

// Profiles returns the declared business systems so the metadata registry and
// tests can share one source of truth.
func Profiles() []systemProfile {
	out := make([]systemProfile, len(businessSystems))
	copy(out, businessSystems)
	return out
}

// MetadataFor returns connector metadata for every declared business system.
func MetadataFor() []datasource.ConnectorMetadata {
	out := make([]datasource.ConnectorMetadata, 0, len(businessSystems))
	for _, profile := range businessSystems {
		out = append(out, datasource.ConnectorMetadata{
			Type:        profile.Type,
			Name:        profile.Name,
			Description: profile.Description + "（未实现：需要源系统接口契约）",
			Priority:    profile.Priority,
			AuthType:    "token",
			// 能力声明为空是准确的：占位实现没有任何一种同步能力。
			Capabilities: []string{},
			Planned:      true,
		})
	}
	return out
}
