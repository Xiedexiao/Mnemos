package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// AIPreReviewRepository 是 ai_pre_review_runs 的存储原语（KnowAxis V3.0
// §6.5）。
//
// 刻意只有三个方法：追加一次运行（CreateRun）、落终态（CompleteRun /
// FailRun 合并为 CompleteRun 的一个参数）、按任务读历史（ListByTask）。
// 没有 Update/Delete —— 运行记录是审批留档，与回执同样的只追不改语义；
// "重跑预审"追加新行，历史行保留供复盘（改稿前后提示对比）。
type AIPreReviewRepository interface {
	// CreateRun 插入一次运行。调用方需先填好 ID/TenantID/TaskID/
	// KnowledgeID/CreatedAt；Status 为空时由实现按 running 兜底。
	CreateRun(ctx context.Context, run *types.AIPreReviewRun) error
	// CompleteRun 落终态：status/findings/model_version/prompt_version/
	// error_message/finished_at 一次性写入。run.ID 必须已存在且属于该租户；
	// 不存在返回 repository.ErrAIPreReviewRunNotFound。
	CompleteRun(ctx context.Context, run *types.AIPreReviewRun) error
	// ListByTask 按 (tenant, task) 取全部运行，created_at 倒序（最新在前），
	// limit 由实现钳制。
	ListByTask(ctx context.Context, tenantID uint64, taskID string, limit int) ([]*types.AIPreReviewRun, error)
}

// AIPreReviewService 是 AI 预审服务（KnowAxis V3.0 §6.5）。
//
// 契约（§6.5「AI 预审的边界」）：
//   - 只产出检查清单 + 风险提示，从不返回"通过/不通过"，也不推进审批
//     状态机（approval_tasks 的状态只由 ReviewService.Decide 改变）；
//   - 每条提示可解释：evidence/locator 给出依据与位置；
//   - 记录 model_version / prompt_version，便于回溯与评测；
//   - 失败降级：模型不可用只让 model 类检查缺席（或以 info 提示标注），
//     规则类检查始终执行；整体失败写 failed 运行行并返回错误，但调用方
//     （审批页、提交后的异步挂接）不得因此阻断审批。
type AIPreReviewService interface {
	// RunPreReview 对一次审批任务执行预审并返回本次运行的提示列表。
	//
	// 租户隔离：tenantID 必须与任务所属租户一致，不一致按"任务不存在"
	// 处理（不泄漏跨租户任务是否存在）。taskID 为空、取不到任务时返回
	// service.ErrAIPreReviewTaskNotFound。
	//
	// 执行顺序：先跑规则类检查（确定性、零外部依赖、必做），再尝试 LLM 类
	// 检查（引用/冲突，可选）；两者结果合并后一次性落库并写审计。运行本身
	// 失败（例如知识已删除）会落一行 failed 运行并以错误返回。
	RunPreReview(ctx context.Context, tenantID uint64, taskID, knowledgeID string) ([]types.PreReviewFinding, error)

	// ListRuns 返回该任务的历史预审运行（最新在前），供审批页展示"预审过
	// 几次、每次提示了什么"。limit 由服务层钳制。
	ListRuns(ctx context.Context, tenantID uint64, taskID string, limit int) ([]*types.AIPreReviewRun, error)
}
