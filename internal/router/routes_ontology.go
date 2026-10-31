package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterOntologyRoutes wires the ontology schema governance endpoints
// (KnowAxis V3.0 §8): schema CRUD + publish, and the type/property/relation
// sub-resources of draft schemas. Writes need Contributor+, reads Viewer+.
// Tenant-level governance surface, JWT-only like review/quality routes (no
// API-key capability grants schema governance).
//
// Assembly example (one line, wiring lives in internal/router/router.go):
//
//	RegisterOntologyRoutes(v1, params.OntologyHandler, rbacGuards)
func RegisterOntologyRoutes(r *gin.RouterGroup, h *handler.OntologyHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	schemas := r.Group("/ontology/schemas")
	{
		// Schema CRUD + 发布（draft→published，版本 +1）。
		schemas.POST("", g.Contributor(), h.CreateOntologySchema)
		schemas.GET("", g.Viewer(), h.ListOntologySchemas)
		schemas.GET("/:id", g.Viewer(), h.GetOntologySchema)
		schemas.PUT("/:id", g.Contributor(), h.UpdateOntologySchema)
		schemas.DELETE("/:id", g.Contributor(), h.DeleteOntologySchema)
		schemas.POST("/:id/publish", g.Contributor(), h.PublishOntologySchema)
		// 类型与关系挂在 schema 下创建；后续按自身 ID 定位更新/删除。
		schemas.POST("/:id/types", g.Contributor(), h.AddOntologyType)
		schemas.POST("/:id/relations", g.Contributor(), h.AddOntologyRelation)
	}
	types := r.Group("/ontology/types")
	{
		types.PUT("/:type_id", g.Contributor(), h.UpdateOntologyType)
		types.DELETE("/:type_id", g.Contributor(), h.DeleteOntologyType)
		types.POST("/:type_id/properties", g.Contributor(), h.AddOntologyProperty)
	}
	properties := r.Group("/ontology/properties")
	{
		properties.PUT("/:property_id", g.Contributor(), h.UpdateOntologyProperty)
		properties.DELETE("/:property_id", g.Contributor(), h.DeleteOntologyProperty)
	}
	relations := r.Group("/ontology/relations")
	{
		relations.PUT("/:relation_id", g.Contributor(), h.UpdateOntologyRelation)
		relations.DELETE("/:relation_id", g.Contributor(), h.DeleteOntologyRelation)
	}
}
