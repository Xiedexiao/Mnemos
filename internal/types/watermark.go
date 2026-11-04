package types

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 服务端水印（KnowAxis V3.0 §16.3 "密级与安全：密级定义、水印、分享、下载、
// 导出、IP 限制、DLP"）。
//
// 平台此前只把水印文本交给前端渲染（export_requests.watermark_text）：这只
// 覆盖"在页面里看"，一旦文件被下载走就什么痕迹都没有——而下载正是最需要
// 溯源的动作。
//
// 服务端水印做三件事：
//  1. 按密级决定**是否需要**水印，以及水印强度；
//  2. 把水印真正写进能承载文本的格式（纯文本 / Markdown / CSV 等），并为
//     二进制格式（PDF、Office、图片）生成一份随包 sidecar 说明；
//  3. 每次带水印的下载/导出都留一条台账（谁、何时、哪一篇、什么密级、
//     水印文本），台账是下载行为本身的证据。
//
// 诚实的边界：**不修改 PDF/Office/图片的像素或版式**。在 Go 侧无第三方
// 依赖的情况下重写这些格式的视觉效果不可靠（字体、加密、增量更新），而
// 半可靠的水印比"清楚说明没有水印"更危险——它让人以为文件可溯源，实际
// 上一裁剪就没了。对这些格式，服务端提供的是可追溯的 sidecar + 台账；
// 版面水印由前端按同一份文本渲染。
// ============================================================================

// WatermarkStrength is how intrusive the mark should be.
type WatermarkStrength string

const (
	// WatermarkStrengthNone 无需水印（public 级且策略未强制）。
	WatermarkStrengthNone WatermarkStrength = "none"
	// WatermarkStrengthFooter 页脚标注（internal 级默认）。
	WatermarkStrengthFooter WatermarkStrength = "footer"
	// WatermarkStrengthTile 平铺水印（secret 级）。
	WatermarkStrengthTile WatermarkStrength = "tile"
	// WatermarkStrengthFull 平铺 + 页脚 + 每页标识（confidential 级）。
	WatermarkStrengthFull WatermarkStrength = "full"
)

// Valid reports whether the strength is known.
func (s WatermarkStrength) Valid() bool {
	switch s {
	case WatermarkStrengthNone, WatermarkStrengthFooter,
		WatermarkStrengthTile, WatermarkStrengthFull:
		return true
	default:
		return false
	}
}

// WatermarkStrengthForClassification maps a classification onto the mark.
//
// public 不加水印：给公开内容盖"机密"章会让水印失去意义——读者很快就会
// 学会忽略它。internal 及以上逐级加强。
func WatermarkStrengthForClassification(classification KnowledgeClassification) WatermarkStrength {
	switch classification {
	case KnowledgeClassificationPublic:
		return WatermarkStrengthNone
	case KnowledgeClassificationSecret:
		return WatermarkStrengthTile
	case KnowledgeClassificationConfidential:
		return WatermarkStrengthFull
	default:
		return WatermarkStrengthFooter
	}
}

// WatermarkedContentType is whether the server can inject the mark into the
// file body itself, or only alongside it.
type WatermarkedContentType string

const (
	// WatermarkInjected 服务端已把水印写进文件正文。
	WatermarkInjected WatermarkedContentType = "injected"
	// WatermarkSidecar 服务端无法改写该格式，改为随包附带说明文件 + 台账。
	WatermarkSidecar WatermarkedContentType = "sidecar"
)

// textWatermarkExtensions are the formats the server can safely rewrite.
//
// 只包含"纯文本即可承载水印"的格式。改写 Office/PDF 需要格式级写回，
// 那不在本实现的范围内（见包注释的边界说明）。
var textWatermarkExtensions = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".text": true,
	".csv": true, ".tsv": true, ".log": true, ".json": false,
}

// CanInjectWatermark reports whether the server can rewrite this file.
//
// 刻意不收录 .json：给 JSON 加注释会让它不再合法，下游解析会直接失败。
func CanInjectWatermark(fileName string) bool {
	ext := strings.ToLower(path.Ext(strings.TrimSpace(fileName)))
	return textWatermarkExtensions[ext]
}

// WatermarkRecord is one watermarked download trace.
//
// 台账而不是"改文件". 台账是唯一在文件被复制、裁剪、转存之后仍然成立的
// 证据：它记的是"谁在什么时候取走了哪一版"。
type WatermarkRecord struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_watermark_records_tenant,priority:1"`
	// UserID is who downloaded.
	UserID string `json:"user_id" gorm:"type:varchar(36);not null;index:idx_watermark_records_tenant,priority:2"`
	// KnowledgeID / KnowledgeBaseID identify the document.
	KnowledgeID     string `json:"knowledge_id" gorm:"type:varchar(36);not null;default:''"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);not null;default:''"`
	// FileName is the delivered name.
	FileName string `json:"file_name" gorm:"type:varchar(512);not null;default:''"`
	// Classification at download time (a later upgrade must not rewrite history).
	Classification string `json:"classification" gorm:"type:varchar(16);not null;default:'internal'"`
	// Strength applied.
	Strength string `json:"strength" gorm:"type:varchar(16);not null;default:'footer'"`
	// Mode is injected or sidecar.
	Mode string `json:"mode" gorm:"type:varchar(16);not null;default:'sidecar'"`
	// WatermarkText is the exact mark embedded or delivered alongside.
	WatermarkText string `json:"watermark_text" gorm:"type:varchar(255);not null;default:''"`
	// Source is the surface: "download" or "export".
	Source string `json:"source" gorm:"type:varchar(16);not null;default:'download'"`
	// RequestID links an export request when the download fulfils one.
	RequestID string    `json:"request_id" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt time.Time `json:"created_at" gorm:"index:idx_watermark_records_tenant,priority:3,sort:desc"`
}

// TableName pins the GORM table name.
func (WatermarkRecord) TableName() string { return "watermark_records" }

// WatermarkApplied is the decision for one download.
type WatermarkApplied struct {
	// Strength resolved for this document.
	Strength WatermarkStrength `json:"strength"`
	// Mode says whether the body was rewritten or a sidecar delivered.
	Mode WatermarkedContentType `json:"mode"`
	// Text is the mark.
	Text string `json:"text"`
	// FileName is the delivered name (unchanged for injected content).
	FileName string `json:"file_name"`
	// SidecarName is the companion file name for sidecar mode.
	SidecarName string `json:"sidecar_name,omitempty"`
	// Note explains a limitation to the caller.
	Note string `json:"note,omitempty"`
	// sidecarBody is the note content generated for sidecar mode.
	sidecarBody string
}

// BuildWatermarkText renders the mark: "用户名 · 时间 · 文档 · 随机短码".
//
// 组成刻意包含四段：谁、何时、哪一篇、哪一次。少了"哪一次"，两次下载的
// 文件无法区分；少了"哪一篇"，从一段摘录无法回溯到来源。
func BuildWatermarkText(
	userLabel, knowledgeID string, classification KnowledgeClassification, now time.Time,
) string {
	var raw [3]byte
	if _, err := rand.Read(raw[:]); err != nil {
		raw[0] = byte(now.Nanosecond() >> 16)
		raw[1] = byte(now.Nanosecond() >> 8)
		raw[2] = byte(now.Nanosecond())
	}
	code := strings.ToUpper(hex.EncodeToString(raw[:]))
	label := strings.TrimSpace(userLabel)
	if label == "" {
		label = "unknown"
	}
	doc := strings.TrimSpace(knowledgeID)
	if len([]rune(doc)) > 12 {
		doc = string([]rune(doc)[:12])
	}
	return fmt.Sprintf("%s · %s · %s · %s · %s",
		label, now.UTC().Format("2006-01-02 15:04"), doc, string(classification), code)
}

// sidebarFormat renders the sidecar note.
//
// 说明文件写清楚"为什么这份文件本身没有可见水印"：不说清楚的话，收到压缩包
// 的人会以为水印丢了，而不是"这种格式服务端不改写"。
func sidebarFormat(applied WatermarkApplied, knowledgeTitle string) string {
	var builder strings.Builder
	builder.WriteString("# 受控文件说明\n\n")
	builder.WriteString("本文件由平台按密级受控分发，来源可追溯。\n\n")
	builder.WriteString("- 文档: " + strings.TrimSpace(knowledgeTitle) + "\n")
	builder.WriteString("- 水印标识: " + applied.Text + "\n")
	builder.WriteString("- 水印强度: " + string(applied.Strength) + "\n")
	builder.WriteString("- 水印文本随包提供，未写入文件本身: 该格式无法在服务端可靠改写；\n")
	builder.WriteString("  版面水印改由阅读端按同一标识渲染。\n\n")
	builder.WriteString("请勿脱离本说明单独转发，否则接收方无法确认来源与密级。\n")
	return builder.String()
}

// InjectTextWatermark appends the mark to a text payload.
//
// 追加而不是替换：任何替换都会改动原文语义。页脚式写入对 Markdown、CSV、
// 纯文本都成立（CSV 里会多一行注释式记录，下游按首列过滤即可）。
func InjectTextWatermark(content []byte, fileName string, applied WatermarkApplied) []byte {
	ext := strings.ToLower(path.Ext(strings.TrimSpace(fileName)))
	var footer string
	switch ext {
	case ".csv", ".tsv":
		separator := ","
		if ext == ".tsv" {
			separator = "\t"
		}
		footer = "\n# " + separator + "水印" + separator + applied.Text + "\n"
	default:
		footer = "\n\n---\n\n> 受控分发 · " + applied.Text + "\n"
	}
	out := make([]byte, 0, len(content)+len(footer))
	out = append(out, content...)
	out = append(out, footer...)
	return out
}

// PlanWatermark decides what to do for one download.
func PlanWatermark(
	fileName, knowledgeID, knowledgeTitle, userLabel string,
	classification KnowledgeClassification, now time.Time,
) WatermarkApplied {
	strength := WatermarkStrengthForClassification(classification)
	if strength == WatermarkStrengthNone {
		return WatermarkApplied{Strength: strength, FileName: fileName}
	}
	text := BuildWatermarkText(userLabel, knowledgeID, classification, now)
	applied := WatermarkApplied{Strength: strength, Text: text, FileName: fileName}
	if CanInjectWatermark(fileName) {
		applied.Mode = WatermarkInjected
		return applied
	}
	applied.Mode = WatermarkSidecar
	base := strings.TrimSuffix(fileName, path.Ext(fileName))
	if base == "" {
		base = "document"
	}
	applied.SidecarName = base + ".watermark.md"
	applied.Note = "该格式无法在服务端可靠写入可见水印；已附带受控文件说明并按密级渲染版面水印。"
	applied.sidecarBody = sidebarFormat(applied, knowledgeTitle)
	return applied
}

// SidecarBody returns the sidecar note content, empty when not applicable.
func (a WatermarkApplied) SidecarBody() string { return a.sidecarBody }

// WatermarkRequired reports whether a download must be recorded.
//
// public 且未强制时不需要台账：给每一次公开文件下载都写一行，会让台账淹没
// 真正需要追溯的受控下载。
func WatermarkRequired(classification KnowledgeClassification) bool {
	return WatermarkStrengthForClassification(classification) != WatermarkStrengthNone
}

// SortWatermarkRecords orders records newest first.
func SortWatermarkRecords(records []*WatermarkRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
}

// BeforeCreate assigns an ID before persistence.
func (r *WatermarkRecord) BeforeCreate(_ *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	return nil
}
