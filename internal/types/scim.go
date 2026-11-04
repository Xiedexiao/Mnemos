package types

import (
	"fmt"
	"net/mail"
	"strings"
	"time"
)

// ============================================================================
// SCIM 2.0 用户/组同步（KnowAxis V3.0 §16.4 "SSO / 组织：OIDC/SAML；组织与
// 用户组同步；SCIM"）。
//
// 平台此前只有手工邀请与手工建组：一个 500 人的企业接入后，HR 系统的入职/
// 离职/调岗必须由管理员在平台上再点一遍，必然漂移——离职员工仍能访问，新员工
// 进不来。
//
// 实现范围（如实）：提供 SCIM 2.0 的 Users 与 Groups **资源端点**，供 IdP
// 主动推送（outbound provisioning）。不支持：
//   - /ServiceProviderConfig 等发现端点（IdP 侧手工填表即可）；
//   - PATCH 的完整 RFC 7644 路径语法（只支持对 displayName / members / active
//     三类常用属性的简化 patch）；
//   - 分页游标（startIndex/count 语义已实现，但不返回 nextCursor）。
//
// 两个关键决定：
//  1. 组同步落成 ACL 主体组，而不是另建一套"SCIM 组"：否则同一个"工艺部"
//     在授权里会出现两个互不相干的身份。
//  2. 用户删除是**停用**而不是物理删除：审计、审批回执、历史版本都引用用户
//     ID，物理删除会让这些记录失去主体。停用即刻失去访问（会员状态与 ACL
//     都不再命中）。
// ============================================================================

// SCIMResource is the common envelope of a SCIM resource.
type SCIMResource struct {
	// Schemas identifies the resource type.
	Schemas []string `json:"schemas"`
	// ID is the platform's identifier.
	ID string `json:"id"`
	// ExternalID is the IdP's identifier (stable across renames).
	ExternalID string `json:"externalId,omitempty"`
	// Meta carries resource metadata.
	Meta SCIMMeta `json:"meta"`
}

// SCIMMeta is the SCIM resource metadata.
type SCIMMeta struct {
	// ResourceType is "User" or "Group".
	ResourceType string `json:"resourceType"`
	// Created is the creation instant.
	Created time.Time `json:"created,omitempty"`
	// LastModified is the last change.
	LastModified time.Time `json:"lastModified,omitempty"`
	// Location is the resource URL.
	Location string `json:"location,omitempty"`
}

// SCIMName is the SCIM name complex attribute.
type SCIMName struct {
	Formatted  string `json:"formatted,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
	GivenName  string `json:"givenName,omitempty"`
}

// SCIMEmail is one SCIM email entry.
type SCIMEmail struct {
	Value   string `json:"value"`
	Type    string `json:"type,omitempty"`
	Primary bool   `json:"primary,omitempty"`
}

// SCIMUser is the SCIM 2.0 User resource.
type SCIMUser struct {
	SCIMResource
	// UserName is the login name (required by SCIM).
	UserName string `json:"userName"`
	// Name is the display name parts.
	Name *SCIMName `json:"name,omitempty"`
	// DisplayName is the display label.
	DisplayName string `json:"displayName,omitempty"`
	// Emails are the address entries; the primary one is the account email.
	Emails []SCIMEmail `json:"emails,omitempty"`
	// Active controls whether the account may sign in.
	Active bool `json:"active"`
	// Groups lists the groups the user belongs to (read-only projection).
	Groups []SCIMGroupRef `json:"groups,omitempty"`
	// EnterpriseUser carries the department attribute when the IdP sends it.
	EnterpriseUser *SCIMEnterpriseUser `json:"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User,omitempty"`
}

// SCIMEnterpriseUser is the enterprise extension (department mapping).
type SCIMEnterpriseUser struct {
	// Department is the department display name the IdP reports.
	Department string `json:"department,omitempty"`
	// Manager identifies the manager.
	Manager *SCIMManager `json:"manager,omitempty"`
	// EmployeeNumber is the HR identifier.
	EmployeeNumber string `json:"employeeNumber,omitempty"`
}

// SCIMManager is the enterprise-extension manager reference.
type SCIMManager struct {
	Value string `json:"value,omitempty"`
}

// SCIMGroupRef is a group reference inside a user resource.
type SCIMGroupRef struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Type    string `json:"type,omitempty"`
}

// SCIMGroupMember is one member of a SCIM group.
type SCIMGroupMember struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Type    string `json:"type,omitempty"`
}

// SCIMGroup is the SCIM 2.0 Group resource.
type SCIMGroup struct {
	SCIMResource
	// DisplayName is the group name; unique per tenant.
	DisplayName string `json:"displayName"`
	// Members are the group's users.
	Members []SCIMGroupMember `json:"members,omitempty"`
}

// SCIMListResponse is the SCIM list envelope.
type SCIMListResponse struct {
	Schemas      []string      `json:"schemas"`
	TotalResults int           `json:"totalResults"`
	StartIndex   int           `json:"startIndex"`
	ItemsPerPage int           `json:"itemsPerPage"`
	Resources    []interface{} `json:"Resources"`
}

// SCIMError is the SCIM error envelope.
type SCIMError struct {
	Schemas  []string `json:"schemas"`
	Detail   string   `json:"detail"`
	Status   string   `json:"status"`
	SCIMType string   `json:"scimType,omitempty"`
}

// SCIM schema URNs.
const (
	// SCIMSchemaUser is the core User schema.
	SCIMSchemaUser = "urn:ietf:params:scim:schemas:core:2.0:User"
	// SCIMSchemaGroup is the core Group schema.
	SCIMSchemaGroup = "urn:ietf:params:scim:schemas:core:2.0:Group"
	// SCIMSchemaList is the list response schema.
	SCIMSchemaList = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	// SCIMSchemaError is the error schema.
	SCIMSchemaError = "urn:ietf:params:scim:api:messages:2.0:Error"
	// SCIMSchemaPatchOp is the PATCH request schema.
	SCIMSchemaPatchOp = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
)

// SCIMPatchOperation is one PATCH operation.
type SCIMPatchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path,omitempty"`
	Value interface{} `json:"value,omitempty"`
}

// SCIMPatchRequest is the PATCH body.
type SCIMPatchRequest struct {
	Schemas    []string             `json:"schemas"`
	Operations []SCIMPatchOperation `json:"Operations"`
}

// ValidateSCIMUser checks the fields provisioning requires.
//
// 只校验真正必需的：userName 是登录名，没有邮箱就无法匹配已有账号，两者都
// 缺失时无法落库。其余属性由 IdP 的 schema 决定，平台不重复实现校验。
func ValidateSCIMUser(user *SCIMUser) error {
	if user == nil {
		return fmt.Errorf("user payload is required")
	}
	if strings.TrimSpace(user.UserName) == "" {
		return fmt.Errorf("userName is required")
	}
	if len([]rune(user.UserName)) > 100 {
		return fmt.Errorf("userName must be at most 100 characters")
	}
	if email := SCIMPrimaryEmail(user); email != "" {
		if _, err := mail.ParseAddress(email); err != nil {
			return fmt.Errorf("email %q is not valid", email)
		}
	}
	return nil
}

// SCIMPrimaryEmail returns the primary email, falling back to the first one.
func SCIMPrimaryEmail(user *SCIMUser) string {
	if user == nil {
		return ""
	}
	for _, entry := range user.Emails {
		if entry.Primary && strings.TrimSpace(entry.Value) != "" {
			return strings.TrimSpace(entry.Value)
		}
	}
	for _, entry := range user.Emails {
		if trimmed := strings.TrimSpace(entry.Value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// SCIMDisplayName renders the display name, falling back through the
// available parts.
func SCIMDisplayName(user *SCIMUser) string {
	if user == nil {
		return ""
	}
	if trimmed := strings.TrimSpace(user.DisplayName); trimmed != "" {
		return trimmed
	}
	if user.Name != nil {
		if formatted := strings.TrimSpace(user.Name.Formatted); formatted != "" {
			return formatted
		}
		joined := strings.TrimSpace(user.Name.GivenName + " " + user.Name.FamilyName)
		if joined != "" {
			return joined
		}
	}
	return strings.TrimSpace(user.UserName)
}

// SCIMDepartment returns the enterprise-extension department.
func SCIMDepartment(user *SCIMUser) string {
	if user == nil || user.EnterpriseUser == nil {
		return ""
	}
	return strings.TrimSpace(user.EnterpriseUser.Department)
}

// ValidateSCIMGroup checks the group payload.
func ValidateSCIMGroup(group *SCIMGroup) error {
	if group == nil {
		return fmt.Errorf("group payload is required")
	}
	if strings.TrimSpace(group.DisplayName) == "" {
		return fmt.Errorf("displayName is required")
	}
	if runes := []rune(group.DisplayName); len(runes) > MaxACLGroupNameLen {
		return fmt.Errorf("displayName must be at most %d characters", MaxACLGroupNameLen)
	}
	return nil
}

// SCIMMemberIDs extracts the member ids, deduped and trimmed.
func SCIMMemberIDs(members []SCIMGroupMember) []string {
	out := make([]string, 0, len(members))
	seen := make(map[string]struct{}, len(members))
	for _, member := range members {
		id := strings.TrimSpace(member.Value)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// HasSCIMMemberRemoval reports whether a PATCH removes members.
func HasSCIMMemberRemoval(operations []SCIMPatchOperation) bool {
	for _, op := range operations {
		if strings.EqualFold(op.Op, "remove") &&
			strings.HasPrefix(strings.ToLower(strings.TrimSpace(op.Path)), "members") {
			return true
		}
	}
	return false
}

// SCIMPatchDisplayName extracts a displayName change from a PATCH body.
//
// SCIM 的 PATCH 路径语法很宽（过滤表达式、子属性），完整实现代价高而实际
// 用途集中：IdP 改名最常用的是 path="displayName"。这里只认这一种，其余
// 返回空字符串让调用方回退到"未变更"。
func SCIMPatchDisplayName(ops []SCIMPatchOperation) string {
	for _, op := range ops {
		if !strings.EqualFold(op.Op, "replace") {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(op.Path), "displayName") {
			continue
		}
		if text, ok := op.Value.(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

// SCIMPatchActive extracts the active flag from a PATCH body.
func SCIMPatchActive(ops []SCIMPatchOperation) (bool, bool) {
	for _, op := range ops {
		if !strings.EqualFold(op.Op, "replace") {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(op.Path), "active") {
			continue
		}
		switch value := op.Value.(type) {
		case bool:
			return value, true
		case string:
			return strings.EqualFold(value, "true"), true
		}
	}
	return false, false
}
