package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterCommentRecycleRoutes wires the knowledge-comment and recycle-bin
// endpoints (KnowAxis V3.0 §5.2 / §5.5 v1).
//
// 守卫矩阵（对照 rbac.go 的选守卫指引）：
//   - 评论是 KB 的子资源：写 = Contributor+ 且对知识所属 KB 有 write 权限
//     （KBAccessWriteFromKnowledgeIDParam，own / org-shared editor 一致）；
//     读 = Viewer+ 且有 read 权限。作者或 Admin 的 moderation 规则在服务层
//     判定（评论只有"作者"没有"KB creator"语义）。
//   - resolve/delete 按评论 ID 寻址（评论 → knowledge → KB 链路未接入
//     rbacGuards 的 lookup），路由只压 Contributor 底线，"作者或 Admin"
//     由 CommentService 强制；跨租户共享 KB 的 moderation 需源租户身份，
//     非本人租户上下文按 tenant 隔离自然 404。
//   - 回收站是租户级治理面（Admin）：restore/purge 改变全员可见性与
//     数据存亡，不设 Contributor 档。
//
// JWT-only，与 glossary/ontology 治理路由一致：API-key capability 体系
// 尚无评论/回收站语义，未声明的路由对 scoped key 默认拒绝。
//
// 路由参数统一 :id 风格（与既有 /knowledge/:id 等一致）。
//
// Assembly example (one line, wiring lives in internal/router/router.go):
//
//	RegisterCommentRecycleRoutes(v1, params.CommentHandler, params.RecycleBinHandler, rbacGuards)
func RegisterCommentRecycleRoutes(
	r *gin.RouterGroup,
	comments *handler.CommentHandler,
	recycle *handler.RecycleBinHandler,
	g *rbacGuards,
) {
	if comments != nil {
		// 知识评论（:id = knowledge id；守卫经它回溯父 KB）。
		kc := r.Group("/knowledge/:id/comments")
		{
			kc.POST("", g.Contributor(), g.KBAccessWriteFromKnowledgeIDParam("id"), comments.AddComment)
			kc.GET("", g.Viewer(), g.KBAccessReadFromKnowledgeIDParam("id"), comments.ListComments)
		}
		// 按评论 ID 寻址的 moderation 动作（作者或 Admin 在服务层判定）。
		mod := r.Group("/comments")
		{
			mod.POST("/:comment_id/resolve", g.Contributor(), comments.ResolveComment)
			mod.DELETE("/:comment_id", g.Contributor(), comments.DeleteComment)
		}
	}
	if recycle != nil {
		// 回收站（租户级治理面，Admin+）。
		rb := r.Group("/recycle-bin")
		{
			rb.GET("", g.Admin(), recycle.ListRecycleBin)
			rb.POST("/:recycle_id/restore", g.Admin(), recycle.RestoreRecycleEntry)
		}
	}
}
