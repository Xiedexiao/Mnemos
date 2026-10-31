package types

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// DLP 输出扫描（KnowAxis V3.0 §13 生成内容校验 v1）。
//
// dlp_findings 记录输出侧 DLP 扫描的每一次命中：问答答案、导出物等生成
// 内容在离开系统前经 DLPService.ScanText 扫描，命中敏感信息即掩码化
// （保留前 2 后 2），并逐命中落一行 finding（action=masked；logged/blocked
// 留给后续动作策略）。内置规则：中国大陆手机号、身份证、银行卡、邮箱；
// Admin 可注册自定义正则（v1 仅存内存，重启丢失）。
// ============================================================================

// DLPFindingType 命中类别。
type DLPFindingType string

const (
	// DLPFindingTypePhone 中国大陆手机号（1[3-9] 开头的 11 位数字）。
	DLPFindingTypePhone DLPFindingType = "phone"
	// DLPFindingTypeIDCard 中国大陆身份证号（18 位，末位可为 X）。
	DLPFindingTypeIDCard DLPFindingType = "id_card"
	// DLPFindingTypeBankCard 银行卡号（13-19 位数字；Luhn 校验 v1 简化省略）。
	DLPFindingTypeBankCard DLPFindingType = "bank_card"
	// DLPFindingTypeEmail 电子邮箱。
	DLPFindingTypeEmail DLPFindingType = "email"
	// DLPFindingTypeCustomPattern Admin 注册的自定义规则命中。
	DLPFindingTypeCustomPattern DLPFindingType = "custom_pattern"
)

// Valid 报告类别是否已知（AddCustomPattern 的入参校验用）。
func (t DLPFindingType) Valid() bool {
	switch t {
	case DLPFindingTypePhone, DLPFindingTypeIDCard, DLPFindingTypeBankCard,
		DLPFindingTypeEmail, DLPFindingTypeCustomPattern:
		return true
	default:
		return false
	}
}

// String 渲染类别用于日志。
func (t DLPFindingType) String() string { return string(t) }

// DLPAction 对命中的处置动作。v1 ScanText 只落 masked（掩码替换）；
// logged（只记录不改写）与 blocked（拦截输出）留给后续动作策略。
type DLPAction string

const (
	// DLPActionLogged 只记录命中不改写输出。
	DLPActionLogged DLPAction = "logged"
	// DLPActionMasked 掩码替换输出（v1 唯一生效动作）。
	DLPActionMasked DLPAction = "masked"
	// DLPActionBlocked 拦截输出。
	DLPActionBlocked DLPAction = "blocked"
)

// Valid 报告动作是否已知。
func (a DLPAction) Valid() bool {
	switch a {
	case DLPActionLogged, DLPActionMasked, DLPActionBlocked:
		return true
	default:
		return false
	}
}

// String 渲染动作用于日志。
func (a DLPAction) String() string { return string(a) }

// DLPFinding 是一次扫描的一条命中记录。
type DLPFinding struct {
	// ID of the finding.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the finding.
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// RequestID 指向命中发生的请求追踪 ID（messages.request_id 同源；
	// 导出等无消息上下文的场景由调用方填自己的追踪 ID）。
	RequestID string `json:"request_id" gorm:"type:varchar(64);not null"`
	// MessageID 指向命中的 assistant 消息；无消息上下文时为空串。
	MessageID string `json:"message_id" gorm:"type:varchar(36);default:''"`
	// FindingType: phone | id_card | bank_card | email | custom_pattern。
	FindingType string `json:"finding_type" gorm:"type:varchar(32);not null"`
	// MatchedSample 脱敏后的命中样本（保留前 2 后 2，中间打星；不入库
	// 原文，避免 findings 表自身成为泄露源）。
	MatchedSample string `json:"matched_sample" gorm:"type:varchar(64);default:''"`
	// Action: logged | masked | blocked。
	Action string `json:"action" gorm:"type:varchar(16);not null"`
	// CreatedAt of the finding.
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (DLPFinding) TableName() string { return "dlp_findings" }

// BeforeCreate assigns an ID before persistence.
func (f *DLPFinding) BeforeCreate(_ *gorm.DB) error {
	if f.ID == "" {
		f.ID = uuid.NewString()
	}
	return nil
}

// DLPCustomPattern 是 Admin 注册的一条自定义 DLP 规则。v1 只存服务进程
// 内存 map（见 dlp_service.go 的注释）：不建表、重启丢失、多副本不同步
// —— 这些是 v1 的已知取舍，正式持久化等动作策略（logged/blocked）一起做。
type DLPCustomPattern struct {
	// Name 规则名（审计与巡检展示用；空则服务层自动命名）。
	Name string `json:"name"`
	// Pattern Go 正则（服务层编译校验，非法正则直接拒绝）。
	Pattern string `json:"pattern"`
	// FindingType 命中落库的类别（白名单校验；空则 custom_pattern）。
	FindingType DLPFindingType `json:"finding_type"`
}
