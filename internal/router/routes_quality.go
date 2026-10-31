package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

// RegisterQualityRoutes wires the feedback & quality-task endpoints
// (KnowAxis V3.0 §13.8 / §14.3 / §14.5).
//
// Assembly example (one line, cmd/server wiring lives in
// internal/container/container.go + internal/router/router.go):
//
//	RegisterQualityRoutes(v1, rbacGuards, params.FeedbackHandler)
//
// Authorization: feedback is an interaction, not a content write —
//   - message ratings: Viewer+ (users rate answers they received; the
//     message lives in their own session);
//   - knowledge reports: Viewer+ plus KBAccessReadFromKnowledgeIDParam,
//     which also rewrites the tenant context to the KB owner workspace so
//     the task lands with the content's governance owner;
//   - quality-task triage: Admin+.
//
// The routes stay JWT-only (no apiKeyGroup declaration): no API-key
// capability grants feedback or triage, mirroring the audit activity feed.
func RegisterQualityRoutes(r *gin.RouterGroup, g *rbacGuards, h *handler.FeedbackHandler) {
	if h == nil {
		return
	}
	// Param position 1 under /messages is free in the POST method tree
	// (only the static /search exists there), so the dedicated
	// :message_id name cannot collide with the GET tree's
	// /messages/:session_id/load.
	r.POST("/messages/:message_id/feedback", g.Viewer(), h.RateMessage)
	// /knowledge/:id/feedback must reuse the :id param name: the POST
	// tree under /knowledge already pins that position
	// (/:id/regenerate-summary, /:id/cancel-parse) and gin panics on two
	// different wildcard names at the same position.
	r.POST("/knowledge/:id/feedback", g.Viewer(),
		g.KBAccessReadFromKnowledgeIDParam("id"), h.ReportKnowledge)
	// Triage is a governance surface: Admin+ on both routes.
	r.GET("/quality-tasks", g.Admin(), h.ListQualityTasks)
	r.POST("/quality-tasks/:task_id", g.Admin(), h.UpdateQualityTask)
}
