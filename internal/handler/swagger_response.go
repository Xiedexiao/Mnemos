package handler

// Response envelopes for Swagger annotations. Handlers write these shapes
// with gin.H; the types only exist so the documented body matches. Compose
// the payload in the annotation, e.g.
//
//	@Success 200 {object} handler.DataResponse{data=types.Knowledge}
//	@Success 200 {object} handler.PageResponse{data=[]types.Knowledge}

// DataResponse is the {"success": true, "data": ...} body.
type DataResponse struct {
	Success bool `json:"success" example:"true"`
	Data    any  `json:"data"`
}

// PageResponse is a page of a list with the total match count.
type PageResponse struct {
	Success  bool  `json:"success" example:"true"`
	Data     any   `json:"data"`
	Total    int64 `json:"total" extensions:"x-agent"`
	Page     int   `json:"page" extensions:"x-agent"`
	PageSize int   `json:"page_size" extensions:"x-agent"`
}

// DataMessageResponse is DataResponse with a human-readable message.
type DataMessageResponse struct {
	Success bool   `json:"success" example:"true"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

// MessageResponse is the {"success": true, "message": ...} body.
type MessageResponse struct {
	Success bool   `json:"success" example:"true" extensions:"x-agent"`
	Message string `json:"message"`
}

// ItemsEnvelopeResponse is a page whose rows live under data.items.
//
// 有些列表端点把 rows 与 total 一起放在 data 里（`{success, data:{items, total}}`），
// 与 PageResponse（rows 直接在 data 下）不同。目录按 Swagger schema 校验
// x-agent 的 root 路径，所以这两种信封必须是不同的类型——共用一个 any 会让
// "root: data.items" 在校验时找不到该字段。
type ItemsEnvelopeResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    ItemsEnvelopeData `json:"data"`
}

// ItemsEnvelopeData is the inner object of ItemsEnvelopeResponse.
type ItemsEnvelopeData struct {
	// Items are the page's rows.
	Items any `json:"items"`
	// Total is the unpaged match count.
	Total int64 `json:"total" extensions:"x-agent"`
}
