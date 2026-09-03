package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"windshift/internal/authz"
	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/restapi"
	"windshift/internal/services"
	"windshift/internal/webhook"
)

type ItemHandler struct {
	db                database.Database
	permissionService *services.PermissionService
	authz             *authz.Authz
	itemCache         *services.ItemCacheService
	activityTracker   *services.ActivityTracker
	itemCRUD          *services.ItemCRUDService
	itemCreation      *services.ItemCreationService
	itemUpdate        *services.ItemUpdateApplicationService
	itemDeletion      *services.ItemDeletionApplicationService
	mentionService    *services.MentionService
	webhookSender     *webhook.WebhookSender
	eventCoordinator  *services.EventCoordinator
	sseHub            *services.SSEHub
	dbRequestTimeout  time.Duration
}

const defaultDBRequestTimeout = 12 * time.Second

func NewItemHandler(db database.Database, permissionService *services.PermissionService, activityTracker *services.ActivityTracker, notificationService interface {
	EmitEvent(event *services.NotificationEvent)
}, cacheSizeMB ...int) *ItemHandler {
	cacheConfig := services.DefaultItemCacheConfig()
	if len(cacheSizeMB) > 0 && cacheSizeMB[0] > 0 {
		cacheConfig.MaxCacheSize = cacheSizeMB[0]
	}
	itemCache, err := services.NewItemCacheService(db, cacheConfig)
	if err != nil {
		slog.Warn("failed to initialize item cache, continuing without cache", "error", err)
	}
	hierarchy := services.NewHierarchyService(db)
	itemUpdate := services.NewItemUpdateApplicationService(db, permissionService)
	itemUpdate.SetActivityTracker(activityTracker)
	itemUpdate.SetCache(itemCache, hierarchy)
	var notify func(*services.NotificationEvent)
	if notificationService != nil {
		notify = notificationService.EmitEvent
	}
	itemUpdate.SetFallbackEmitter(services.NewLegacyItemUpdatedEmitter(db, notify, nil, nil))
	itemDeletion := services.NewItemDeletionApplicationService(db, permissionService)
	itemDeletion.SetCache(itemCache, hierarchy)
	return &ItemHandler{
		db: db, permissionService: permissionService, authz: authz.New(db, permissionService),
		itemCache: itemCache, activityTracker: activityTracker,
		itemCRUD: services.NewItemCRUDService(db), itemCreation: services.NewItemCreationService(db, permissionService),
		itemUpdate: itemUpdate, itemDeletion: itemDeletion, dbRequestTimeout: defaultDBRequestTimeout,
	}
}

func (h *ItemHandler) SetDBRequestTimeout(timeout time.Duration) {
	if timeout > 0 {
		h.dbRequestTimeout = timeout
	}
}

func (h *ItemHandler) requestDBContext(r *http.Request) (context.Context, context.CancelFunc) {
	timeout := h.dbRequestTimeout
	if timeout <= 0 {
		timeout = defaultDBRequestTimeout
	}
	return context.WithTimeout(r.Context(), timeout)
}

func (h *ItemHandler) respondItemReadError(w http.ResponseWriter, r *http.Request, err error) {
	database.ObserveRequestQueryError(err)
	if errors.Is(err, context.Canceled) || r.Context().Err() != nil {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		respondError(w, r, restapi.NewAPIError(http.StatusGatewayTimeout, "DATABASE_DEADLINE_EXCEEDED", "The database request timed out."))
		return
	}
	respondInternalError(w, r, err)
}

func (h *ItemHandler) SetWebhookSender(sender *webhook.WebhookSender) {
	h.webhookSender = sender
	h.itemUpdate.SetFallbackWebhook(sender)
}

func (h *ItemHandler) SetMentionService(mentionService *services.MentionService) {
	h.mentionService = mentionService
	h.itemUpdate.SetMentionService(mentionService)
}

func (h *ItemHandler) SetActionService(actionService interface {
	EmitActionEvent(event *models.ActionEvent)
}) {
	h.itemUpdate.SetFallbackAction(actionService)
}

func (h *ItemHandler) SetEventCoordinator(coordinator *services.EventCoordinator) {
	h.eventCoordinator = coordinator
	h.itemCreation.SetEmitter(coordinator)
	h.itemUpdate.SetEmitter(coordinator)
	h.itemDeletion.SetEmitter(coordinator)
}

func (h *ItemHandler) ItemCreationService() *services.ItemCreationService { return h.itemCreation }

func (h *ItemHandler) ItemUpdateApplicationService() *services.ItemUpdateApplicationService {
	return h.itemUpdate
}

func (h *ItemHandler) ItemDeletionApplicationService() *services.ItemDeletionApplicationService {
	return h.itemDeletion
}

func (h *ItemHandler) ItemCacheService() *services.ItemCacheService { return h.itemCache }

func (h *ItemHandler) maskInaccessibleProjectNamesContext(ctx context.Context, userID int, items []models.Item) {
	services.NewTimePermissionService(h.db, h.permissionService).MaskInaccessibleProjectNamesContext(ctx, userID, items)
	services.MaskInaccessibleRelatedWorkItems(userID, items, h.permissionService)
}

func (h *ItemHandler) GetCacheStats(w http.ResponseWriter, r *http.Request) {
	if h.itemCache == nil {
		respondError(w, r, &restapi.APIError{StatusCode: http.StatusServiceUnavailable, Code: "SERVICE_UNAVAILABLE", Message: "Item cache is not enabled"})
		return
	}
	respondJSONOK(w, map[string]any{
		"cache_enabled": true,
		"statistics":    h.itemCache.GetStats(),
		"timestamp":     time.Now().Format(time.RFC3339),
	})
}
