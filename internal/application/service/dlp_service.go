package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ============================================================================
// DLP 输出扫描服务（KnowAxis V3.0 §13 生成内容校验 v1）。
//
// ScanText 是问答输出挂钩（completeAssistantMessage / saveEvidencePack 附近）
// 与导出管线共用的唯一扫描入口：内置四类检测器（中国大陆手机号、身份证、
// 银行卡、邮箱）+ 租户自定义正则，命中即掩码化（保留前 2 后 2）并落
// dlp_findings(action=masked)。
//
// 自定义规则 v1 只存进程内存 map（sync.RWMutex 保护）——不建表、重启丢失、
// 多副本不同步。这是有意的 v1 取舍：正式持久化应与动作策略（logged/blocked、
// 规则命中统计、灰度开关）一起设计，避免先落一张马上要改 schema 的表。
// ============================================================================

// dlpPatternAddedAction is the audit action for Admin custom-rule writes.
const dlpPatternAddedAction = "dlp.pattern_added"

// dlpMatchedSampleMaxRunes mirrors the migration's varchar(64) column
// (matched_sample), counted in runes so CJK samples truncate safely.
const dlpMatchedSampleMaxRunes = 64

// 内置检测器正则（V3.0 §13 v1 白名单）。
var (
	// 中国大陆身份证：18 位，末位可为 X/x。相邻字符是数字或 X 视为更长
	// 数字串的一部分，不命中（防止把 19 位长号截出"伪身份证"）。
	dlpIDCardRegex = regexp.MustCompile(`\d{17}[0-9Xx]`)
	// 银行卡：13-19 位数字。Luhn 校验 v1 简化省略（误报率换零依赖，注释
	// 说明）；要收紧时在 dlpDetector 里加 luhn 校验函数即可。
	dlpBankCardRegex = regexp.MustCompile(`\d{13,19}`)
	// 中国大陆手机号：1[3-9] 开头的 11 位数字。
	dlpPhoneRegex = regexp.MustCompile(`1[3-9]\d{9}`)
	// 电子邮箱。
	dlpEmailRegex = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
)

// dlpDetector is one built-in scanner. order matters: id card runs before
// bank card so an 18-digit ID is typed id_card (更敏感的类别优先归因)；
// bank card runs before phone so a phone-length digit run inside a card
// number is not re-detected as phone after masking.
type dlpDetector struct {
	findingType types.DLPFindingType
	re          *regexp.Regexp
	// forbidAfter lists extra ASCII bytes that disqualify a match when
	// found right AFTER it (digits always disqualify on both sides).
	// id_card forbids a trailing X/x so a 19-char "18 digits + X" run is
	// not split into a fake 18-char ID + stray X; a leading X/x is fine
	// ("x" + 18位身份证是合法命中：前置字母不延长数字串).
	forbidAfter string
}

// dlpBuiltinDetectors is the ordered built-in scanner set.
var dlpBuiltinDetectors = []dlpDetector{
	{findingType: types.DLPFindingTypeIDCard, re: dlpIDCardRegex, forbidAfter: "Xx"},
	{findingType: types.DLPFindingTypeBankCard, re: dlpBankCardRegex},
	{findingType: types.DLPFindingTypePhone, re: dlpPhoneRegex},
	{findingType: types.DLPFindingTypeEmail, re: dlpEmailRegex},
}

// dlpCustomRule is one compiled tenant-scoped custom pattern.
type dlpCustomRule struct {
	name        string
	findingType types.DLPFindingType
	re          *regexp.Regexp
}

// dlpService implements interfaces.DLPService.
type dlpService struct {
	repo  interfaces.DLPRepository
	audit interfaces.AuditLogService
	now   func() time.Time

	// mu guards patterns and patternSeq. 规则是低频 Admin 写、高频读：
	// RWMutex 足够，v1 不做无锁优化。
	mu         sync.RWMutex
	patterns   map[uint64][]dlpCustomRule
	patternSeq int
}

// NewDLPService constructs the DLP scanning service.
func NewDLPService(
	repo interfaces.DLPRepository,
	audit interfaces.AuditLogService,
) interfaces.DLPService {
	return &dlpService{
		repo:     repo,
		audit:    audit,
		now:      time.Now,
		patterns: make(map[uint64][]dlpCustomRule),
	}
}

// dlpMaskValue keeps the first and last 2 runes and stars the middle; values
// shorter than 5 runes keep only the first rune（"前 2 后 2"对 4 位以下的值
// 等于全量泄露，短值退化为"留 1 打余"）。返回值与原值等长（rune 计），掩码
// 不改变上下文排版。
func dlpMaskValue(value string) string {
	runes := []rune(value)
	n := len(runes)
	switch {
	case n == 0:
		return value
	case n >= 5:
		return string(runes[:2]) + strings.Repeat("*", n-4) + string(runes[n-2:])
	default:
		return string(runes[:1]) + strings.Repeat("*", n-1)
	}
}

// dlpSampleForStore clamps a masked sample to the column width (varchar(64),
// rune-counted) so one long email cannot fail the finding insert.
func dlpSampleForStore(masked string) string {
	runes := []rune(masked)
	if len(runes) > dlpMatchedSampleMaxRunes {
		return string(runes[:dlpMatchedSampleMaxRunes])
	}
	return masked
}

// dlpBoundaryOK reports whether the bytes adjacent to [start,end) disqualify
// the match: a digit on either side means the match sits inside a longer
// digit run; forbidAfter additionally rejects those bytes right AFTER the
// match only (id_card's trailing X guard — a leading X/x does not extend a
// digit run, "x" + 18位身份证是合法命中). Byte-level checks are safe: every
// character in play (digits, X/x, and the regexes' ASCII-only classes) is
// single-byte ASCII, and a multi-byte neighbour's bytes are all >= 0x80,
// never matching.
func dlpBoundaryOK(text string, start, end int, forbidAfter string) bool {
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	if start > 0 && isDigit(text[start-1]) {
		return false
	}
	if end < len(text) {
		c := text[end]
		if isDigit(c) || strings.IndexByte(forbidAfter, c) >= 0 {
			return false
		}
	}
	return true
}

// dlpFindingRow builds one persisted finding row for a hit.
func dlpFindingRow(tenantID uint64, requestID, messageID string, findingType types.DLPFindingType, masked string, action types.DLPAction) *types.DLPFinding {
	return &types.DLPFinding{
		TenantID:      tenantID,
		RequestID:     requestID,
		MessageID:     messageID,
		FindingType:   string(findingType),
		MatchedSample: dlpSampleForStore(masked),
		Action:        string(action),
	}
}

// ScanText runs the built-in detectors plus the tenant's custom patterns over
// text, masks every hit and records findings. 返回掩码后的文本与命中数。
//
// 失败语义：扫描本身不会失败（正则全部预编译），自定义规则持久化是 v1
// 内存态、finding 落库是 best-effort —— 存储失败只告警不阻断调用方的输出
// 路径（问答主链路不能因为审计写失败而失败，与 audit hook 同一策略）。
func (s *dlpService) ScanText(
	ctx context.Context, tenantID uint64, requestID, messageID, text string,
) (string, int, error) {
	if text == "" {
		return text, 0, nil
	}
	cleaned := text
	var findings []*types.DLPFinding

	// 内置检测器：逐条扫描 → 边界过滤 → 从后往前替换（前面的下标不变）。
	for _, det := range dlpBuiltinDetectors {
		matches := det.re.FindAllStringIndex(cleaned, -1)
		// 从最后一个命中开始替换，保留前面命中的下标有效性。
		for i := len(matches) - 1; i >= 0; i-- {
			start, end := matches[i][0], matches[i][1]
			if !dlpBoundaryOK(cleaned, start, end, det.forbidAfter) {
				continue
			}
			masked := dlpMaskValue(cleaned[start:end])
			cleaned = cleaned[:start] + masked + cleaned[end:]
			findings = append(findings, dlpFindingRow(
				tenantID, requestID, messageID, det.findingType, masked, types.DLPActionMasked))
		}
	}

	// 租户自定义规则（v1 内存态）：在清洗后的文本上跑，避免与内置规则
	// 重复归因同一片段。
	for _, rule := range s.rulesFor(tenantID) {
		matches := rule.re.FindAllStringIndex(cleaned, -1)
		for i := len(matches) - 1; i >= 0; i-- {
			start, end := matches[i][0], matches[i][1]
			masked := dlpMaskValue(cleaned[start:end])
			cleaned = cleaned[:start] + masked + cleaned[end:]
			findings = append(findings, dlpFindingRow(
				tenantID, requestID, messageID, rule.findingType, masked, types.DLPActionMasked))
		}
	}

	if len(findings) == 0 {
		return cleaned, 0, nil
	}
	s.persistFindings(ctx, findings)
	return cleaned, len(findings), nil
}

// rulesFor snapshots the tenant's custom rules under the read lock.
func (s *dlpService) rulesFor(tenantID uint64) []dlpCustomRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.patterns[tenantID]
}

// persistFindings writes the finding rows best-effort：单行失败告警继续，
// 不让审计写失败阻塞问答输出（调用方拿到的 cleaned 文本与命中数不受影响）。
func (s *dlpService) persistFindings(ctx context.Context, findings []*types.DLPFinding) {
	if s.repo == nil {
		return
	}
	for _, finding := range findings {
		if err := s.repo.CreateFinding(ctx, finding); err != nil {
			logger.Warnf(ctx, "dlp finding persist failed (type=%s sample=%s): %v",
				finding.FindingType, finding.MatchedSample, err)
		}
	}
}

// AddCustomPattern registers one tenant-scoped custom regex rule (Admin only;
// the route guard enforces the role). v1 stores rules in process memory:
// 不建表、重启丢失、多副本不同步 —— 正式持久化与动作策略（logged/blocked）
// 一起做，见文件头注释。
func (s *dlpService) AddCustomPattern(
	ctx context.Context, tenantID uint64, actorID string, pattern *types.DLPCustomPattern,
) (*types.DLPCustomPattern, error) {
	if pattern == nil {
		return nil, apperrors.NewBadRequestError("pattern body is required")
	}
	pattern.Pattern = strings.TrimSpace(pattern.Pattern)
	if pattern.Pattern == "" {
		return nil, apperrors.NewBadRequestError("pattern is required")
	}
	// 正则先编译再入库（内存 map）：非法正则直接 400，而不是等到扫描时
	// 才静默跳过这条规则。
	re, err := regexp.Compile(pattern.Pattern)
	if err != nil {
		return nil, apperrors.NewBadRequestError("pattern is not a valid regular expression: " + err.Error())
	}
	findingType := pattern.FindingType
	if findingType == "" {
		findingType = types.DLPFindingTypeCustomPattern
	}
	if !findingType.Valid() {
		return nil, apperrors.NewBadRequestError(
			"finding_type must be one of: phone, id_card, bank_card, email, custom_pattern")
	}

	s.mu.Lock()
	s.patternSeq++
	name := strings.TrimSpace(pattern.Name)
	if name == "" {
		name = fmt.Sprintf("custom-%d", s.patternSeq)
	}
	s.patterns[tenantID] = append(s.patterns[tenantID], dlpCustomRule{
		name:        name,
		findingType: findingType,
		re:          re,
	})
	s.mu.Unlock()

	// 规则本身不是敏感数据（是检测逻辑），审计里落全量便于回溯谁加了
	// 什么规则。
	s.logAudit(ctx, tenantID, actorID, name, pattern.Pattern, findingType)
	pattern.Name = name
	pattern.FindingType = findingType
	return pattern, nil
}

// ListFindings lists the tenant's recent findings, newest first (Admin-facing
// 巡检面；limit 语义见仓储).
func (s *dlpService) ListFindings(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.DLPFinding, error) {
	if s.repo == nil {
		return nil, nil
	}
	return s.repo.ListFindings(ctx, tenantID, limit)
}

// logAudit writes one custom-rule audit event; audit failures never block
// the rule registration (same policy as the other verticals).
func (s *dlpService) logAudit(
	ctx context.Context, tenantID uint64, actorID, name, pattern string, findingType types.DLPFindingType,
) {
	if s.audit == nil {
		return
	}
	details := dlpDetailsJSON(map[string]interface{}{
		"name":         name,
		"pattern":      pattern,
		"finding_type": string(findingType),
	})
	if err := s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(dlpPatternAddedAction),
		ScopeType:   "dlp",
		TargetType:  "dlp_pattern",
		TargetID:    name,
		Outcome:     types.AuditOutcomeSuccess,
		Details:     details,
	}); err != nil {
		logger.Warnf(ctx, "dlp audit log failed for pattern %s: %v", name, err)
	}
}

// dlpDetailsJSON marshals an audit detail payload, falling back to an empty
// object on failure so logging never blocks a business write. Kept local to
// this vertical (deliberately not named like the glossary/export helpers) so
// the verticals stay independently maintainable.
func dlpDetailsJSON(v interface{}) types.JSON {
	data, err := json.Marshal(v)
	if err != nil {
		return types.JSON([]byte("{}"))
	}
	return types.JSON(data)
}
