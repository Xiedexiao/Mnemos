package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterFolderACLRoutes wires directory-level permissions and the space
// policy read (KnowAxis V3.0 §2.3 权限继承与上限 / §4.1 空间).
//
// 权限矩阵：
//   - POST/DELETE /knowledge-bases/:id/folder-acl —— 改目录授权等于改谁能看
//     一个部门的内容：Admin+，且受父级动作上限约束（服务层校验）。
//   - GET 列表 / resolve —— 读授权结构：Admin+（会暴露谁对哪个目录有权）。
//   - GET /space-policy —— 空间默认策略，录入者需要知道默认密级：Viewer+。
//   - GET /acl-ceiling-violations —— 治理清单：Admin+。
//
// 全部 JWT-only：目录授权属于治理配置，机器身份不应批量改写。
func RegisterFolderACLRoutes(
	r *gin.RouterGroup, h *handler.FolderACLHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	folders := r.Group("/knowledge-bases/:id/folder-acl")
	{
		folders.GET("", g.Admin(), h.ListFolderACL)
		folders.POST("", g.Admin(), h.GrantFolderACL)
		folders.DELETE("/:grant_id", g.Admin(), h.RevokeFolderACL)
		// resolve 是静态段，注册在 :grant_id 之前不冲突（不同 HTTP 方法）。
		folders.GET("/resolve", g.Admin(), h.ResolveFolderACL)
	}
	r.GET("/knowledge-bases/:id/space-policy", g.Viewer(), h.GetSpacePolicy)
	r.GET("/acl-ceiling-violations", g.Admin(), h.ListACLCeilingViolations)
}
