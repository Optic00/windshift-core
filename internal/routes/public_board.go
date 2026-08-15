package routes

import "net/http"

// RegisterPublicBoardRoutes registers public (unauthenticated) board routes.
func RegisterPublicBoardRoutes(deps *Deps) {
	api := deps.API
	limit := deps.PublicBoardLimiter.Limit

	// Public board reads require no auth, so all three surfaces share one IP budget.
	api.HandleH("GET /public/board/{slug}", limit(http.HandlerFunc(deps.PublicBoard.GetPublicBoard)))
	api.HandleH("GET /public/board/{slug}/items/{key}", limit(http.HandlerFunc(deps.PublicBoard.GetPublicBoardItem)))
	api.HandleH("GET /public/board/{slug}/attachments/{id}/download", limit(http.HandlerFunc(deps.PublicBoard.DownloadAttachment)))
}
