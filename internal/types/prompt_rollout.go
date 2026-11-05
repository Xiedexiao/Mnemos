package types

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"
)

// ============================================================================
// 提示词 / 模型的灰度与回滚（KnowAxis V3.0 §16.5 "灰度、回滚、降级链"）。
//
// 平台此前只取**最新版本**（`LatestPromptVersion`）：保存一版即全量生效，出
// 问题只能再存一版盖回去。这不是灰度，也不是回滚——它是"覆盖"。
//
// 三个决定：
//  1. **活跃指针独立于版本表**。版本表是 append-only 的历史，指针是"现在用
//     哪一版"。把"当前生效"写进版本行（比如加个 is_active 列）会让历史行被
//     改写，而历史不可变正是这条链路可审计的前提。
//  2. 灰度比例按**稳定的主体键**抽样（租户+模板键+会话/用户），不是随机数。
//     同一次会话在灰度期内必须始终看到同一版：否则同一段对话里前半句用旧
//     提示词、后半句用新的，问题无法归因。
//  3. 回滚是**改指针**，不是删版本、也不是复制旧内容成新版本。后者会掩盖
//     "曾经上线过一个坏版本"这个事实。
// ============================================================================

// PromptVersionPointer records which version is live for one template key.
//
// 一行 = (租户, 模板键)。缺失时行为与改动前一致：取最新版本。
type PromptVersionPointer struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;uniqueIndex:uq_prompt_version_pointer,priority:1"`
	// TemplateKey identifies the prompt template.
	TemplateKey string `json:"template_key" gorm:"type:varchar(64);not null;uniqueIndex:uq_prompt_version_pointer,priority:2"`
	// StableVersion is the version everyone gets.
	StableVersion int `json:"stable_version" gorm:"not null;default:0"`
	// CanaryVersion is the version served to the sampled fraction.
	// 0 disables the canary entirely (the common case).
	CanaryVersion int `json:"canary_version" gorm:"not null;default:0"`
	// CanaryPercent is the fraction (0-100) that sees CanaryVersion.
	CanaryPercent int `json:"canary_percent" gorm:"not null;default:0"`
	// UpdatedBy / UpdatedAt record the last change.
	UpdatedBy string    `json:"updated_by" gorm:"type:varchar(36);not null;default:''"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (PromptVersionPointer) TableName() string { return "prompt_version_pointers" }

// ValidatePromptVersionPointer checks the shape.
//
// 三个拒绝条件都对应真实的误配置：
//   - 稳定版为 0 而灰度版非 0：没有基线可比；
//   - 灰度比例越界；
//   - 灰度版等于稳定版：比例设了但两版相同，会让人以为在灰度。
func ValidatePromptVersionPointer(pointer PromptVersionPointer) error {
	if strings.TrimSpace(pointer.TemplateKey) == "" {
		return fmt.Errorf("template key is required")
	}
	if pointer.StableVersion <= 0 {
		return fmt.Errorf("stable_version must be a real version")
	}
	if pointer.CanaryPercent < 0 || pointer.CanaryPercent > 100 {
		return fmt.Errorf("canary_percent must be between 0 and 100")
	}
	if pointer.CanaryVersion == 0 {
		// 没有灰度版本时比例必须为 0：留着 30% 却不指定版本，读起来像
		// "在灰度"，实际没有。
		if pointer.CanaryPercent != 0 {
			return fmt.Errorf("canary_percent requires a canary_version")
		}
		return nil
	}
	if pointer.CanaryVersion == pointer.StableVersion {
		return fmt.Errorf("canary_version must differ from stable_version")
	}
	return nil
}

// PromptVersionSelection is the outcome of resolving a pointer for one subject.
type PromptVersionSelection struct {
	// Version is the version to serve.
	Version int `json:"version"`
	// Canary reports whether the canary version was chosen.
	Canary bool `json:"canary"`
	// Reason explains the choice (for logs and the audit trail).
	Reason string `json:"reason,omitempty"`
}

// SelectPromptVersion decides which version one subject sees.
//
// subjectKey 是稳定抽样键：用 (租户, 模板键, 会话 ID 或用户 ID)。同一主体在
// 灰度期内恒得到同一结果——哈希是纯函数，这一点不依赖进程状态。
func SelectPromptVersion(
	pointer PromptVersionPointer, subjectKey string,
) PromptVersionSelection {
	if pointer.CanaryVersion > 0 && pointer.CanaryPercent > 0 {
		if promptCanaryBucket(pointer, subjectKey) < pointer.CanaryPercent {
			return PromptVersionSelection{
				Version: pointer.CanaryVersion, Canary: true,
				Reason: fmt.Sprintf("canary: subject falls in the first %d%%",
					pointer.CanaryPercent),
			}
		}
		return PromptVersionSelection{
			Version: pointer.StableVersion,
			Reason:  "stable: subject outside the canary fraction",
		}
	}
	if pointer.StableVersion > 0 {
		return PromptVersionSelection{
			Version: pointer.StableVersion,
			Reason:  "stable: no canary configured",
		}
	}
	// 没有指针（零值）：调用方回退到"取最新版本"，与改动前一致。
	return PromptVersionSelection{Reason: "no pointer: use the latest version"}
}

// promptCanaryBucket maps a subject onto 0..99 deterministically.
//
// 用 FNV-1a 而不是随机数：灰度必须可复现——"为什么这个用户看到了新版本"
// 要能回答，而随机会让同一会话在两次请求间跳版本。
func promptCanaryBucket(pointer PromptVersionPointer, subjectKey string) int {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(pointer.TemplateKey))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strings.TrimSpace(subjectKey)))
	return int(hash.Sum32() % 100)
}

// PromptRollbackRequest is one rollback to a previously published version.
type PromptRollbackRequest struct {
	// TemplateKey being rolled back.
	TemplateKey string `json:"template_key"`
	// ToVersion is the version to make stable again.
	ToVersion int `json:"to_version"`
	// Note explains why (required: a rollback without a reason cannot be reviewed).
	Note string `json:"note"`
}

// ValidatePromptRollback checks a rollback request.
//
// note 必填：回滚是"承认刚上线的东西有问题"，没有理由的回滚在复盘时无法
// 区分"确实坏了"与"有人手滑"。
func ValidatePromptRollback(request PromptRollbackRequest) error {
	if strings.TrimSpace(request.TemplateKey) == "" {
		return fmt.Errorf("template key is required")
	}
	if request.ToVersion <= 0 {
		return fmt.Errorf("to_version must be a real version")
	}
	if strings.TrimSpace(request.Note) == "" {
		return fmt.Errorf("a rollback needs a note explaining why")
	}
	return nil
}

// DescribePromptRollback renders one line for the audit trail.
func DescribePromptRollback(templateKey string, from, to int, note string) string {
	return fmt.Sprintf("prompt %s rolled back from v%d to v%d: %s",
		templateKey, from, to, strings.TrimSpace(note))
}
