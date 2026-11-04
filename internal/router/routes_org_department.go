package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterOrgDepartmentRoutes wires the organization tree (§2.1 L1).
//
// 权限：读面 Viewer+（每个人都该看得见自己所在的部门结构）；写面 Admin+。
// 部门经理的"仅本子树"限制在服务层做（ScopeOf / CanManageDepartment），
// 因为需要部门路径参与判断，路由层拿不到。
func RegisterOrgDepartmentRoutes(
	r *gin.RouterGroup, h *handler.OrgDepartmentHandler, g *rbacGuards,
) {
	if h == nil {
		return
	}
	departments := r.Group("/organization/departments")
	{
		departments.GET("", g.Viewer(), h.ListDepartments)
		departments.GET("/scope", g.Viewer(), h.GetDepartmentScope)
		departments.GET("/:id", g.Viewer(), h.GetDepartment)
		departments.POST("", g.Admin(), h.CreateDepartment)
		departments.PUT("/:id", g.Admin(), h.UpdateDepartment)
		departments.POST("/:id/move", g.Admin(), h.MoveDepartment)
		departments.DELETE("/:id", g.Admin(), h.DeleteDepartment)
		departments.POST("/:id/members", g.Admin(), h.AddDepartmentMembers)
		departments.DELETE("/:id/members/:user_id", g.Admin(), h.RemoveDepartmentMember)
	}
}
