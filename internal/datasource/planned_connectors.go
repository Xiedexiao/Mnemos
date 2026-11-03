package datasource

// PlannedConnector is one declared-but-unimplemented connector.
//
// 这里的清单是"平台打算接什么"的单一来源：元数据注册表与测试都读它，避免
// 一边声明可用、一边又没有实现。
type PlannedConnector struct {
	// Type is the connector type identifier.
	Type string
	// Name is the display name.
	Name string
	// Description says what the integration is meant to sync.
	Description string
	// Needs names the interface contract a real implementation requires.
	Needs string
	// Priority orders the UI list.
	Priority int
}

// plannedConnectors is the §3.3 business-system list.
//
// 这些类型由 internal/datasource/connector/business 提供占位实现：Validate
// 与所有 Fetch 都返回明确的"未实现"错误，而不是空结果。声明在这里是为了让
// 前端能展示"计划中的接入源"，同时不让人误以为可以配置。
var plannedConnectors = []PlannedConnector{
	{Type: "erp", Name: "ERP (企业资源计划)",
		Description: "同步工单、生产记录、停线事件与参数调整",
		Needs:       "ERP 的工单/生产记录只读接口（列表 + 按 updated_at 增量 + 业务键）",
		Priority:    20},
	{Type: "mes", Name: "MES (制造执行)",
		Description: "同步生产事件、停机恢复与工艺参数变更",
		Needs:       "MES 的生产事件/停机记录只读接口（事件时间戳 + 设备与工艺版本）",
		Priority:    21},
	{Type: "plm", Name: "PLM / PDM (产品生命周期)",
		Description: "同步图纸、BOM、ECR/ECO 与版本替代关系",
		Needs:       "PLM 的图纸/BOM/ECN 只读接口（版本号、生效日、替代关系）",
		Priority:    22},
	{Type: "qms", Name: "QMS (质量管理)",
		Description: "同步 NCR/CAPA、异常、检验记录与供应商质量事件",
		Needs:       "QMS 的 NCR/CAPA 只读接口（状态变更事件 + 根因字段）",
		Priority:    23},
	{Type: "oa", Name: "OA (办公自动化)",
		Description: "同步制度、通知与流程审批记录",
		Needs:       "OA 的公文/制度只读接口（发文号、生效日、版本）",
		Priority:    24},
	{Type: "hr", Name: "HR (人力资源)",
		Description: "同步组织架构、岗位与任职变更",
		Needs:       "HR 的组织/岗位只读接口（组织树 + 变动事件；个人信息最小化）",
		Priority:    25},
	{Type: "crm", Name: "CRM (客户关系)",
		Description: "同步客诉、服务记录与解决方案",
		Needs:       "CRM 的工单/客诉只读接口（客户脱敏字段 + 关闭事件）",
		Priority:    26},
}

// plannedConnectorMetadata renders the registry rows for the declared set.
//
// Description 带上"未实现"与缺口说明：前端即使只渲染 description 也能让
// 运维看到为什么不能选，而不是看到一个空的能力列表。
func plannedConnectorMetadata() []ConnectorMetadata {
	out := make([]ConnectorMetadata, 0, len(plannedConnectors))
	for _, plan := range plannedConnectors {
		out = append(out, ConnectorMetadata{
			Type:         plan.Type,
			Name:         plan.Name,
			Description:  plan.Description + "（未实现：需要源系统接口契约）",
			Priority:     plan.Priority,
			AuthType:     "token",
			Capabilities: []string{},
			Planned:      true,
		})
	}
	return out
}

// PlannedConnectorTypes returns the declared-but-unimplemented connector types.
func PlannedConnectorTypes() []string {
	out := make([]string, 0, len(plannedConnectors))
	for _, plan := range plannedConnectors {
		out = append(out, plan.Type)
	}
	return out
}

// IsPlannedConnector reports whether a type is declared but unimplemented.
func IsPlannedConnector(connectorType string) bool {
	for _, plan := range plannedConnectors {
		if plan.Type == connectorType {
			return true
		}
	}
	return false
}
