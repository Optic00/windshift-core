package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"windshift/internal/database"
	"windshift/internal/logger"
	"windshift/internal/markdown"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/utils"
	"windshift/internal/validation"
	"windshift/internal/webhook"
)

// commentRequestBodyMaxBytes caps the comment create/update request body. The
// content itself is bounded by sanitize.Comment (LongTextMaxBytes = 256 KiB);
// the extra headroom covers JSON framing and the other small fields. Capping
// the raw body stops a client from streaming an unbounded payload (memory) and
// from smuggling a giant pre-sanitization comment that, via the @mention
// trigger, would otherwise become an oversized agent prompt.
const commentRequestBodyMaxBytes = sanitize.LongTextMaxBytes + 16*1024

// CommentHandler handles comment-related HTTP requests
type CommentHandler struct {
	db                  database.Database
	permissionService   *services.PermissionService
	activityTracker     *services.ActivityTracker
	mentionService      *services.MentionService // Mention service for processing @mentions (optional, can be nil)
	notificationService interface {
		EmitEvent(event *services.NotificationEvent)
	} // Notification service for async notification processing (optional, can be nil)
	webhookSender    *webhook.WebhookSender   // Webhook sender for dispatching webhook events (optional, can be nil)
	commentService   *services.CommentService // CommentService for unified comment creation logic
	issueSyncService interface {
		PushCommentToGitHub(ctx context.Context, itemID int, commentID int, authorID int, commentBody string)
		PushCommentUpdateToGitHub(ctx context.Context, commentID int, authorID int, newBody string)
	} // Issue sync service for pushing comments to GitHub (optional, can be nil)
	approvalService *services.ApprovalService // Approval service for approver-derived item-view fallback (optional, can be nil)
}

// NewCommentHandler creates a new comment handler
func NewCommentHandler(db database.Database, permissionService *services.PermissionService, activityTracker *services.ActivityTracker, notificationService interface {
	EmitEvent(event *services.NotificationEvent)
}) *CommentHandler {
	return &CommentHandler{
		db:                  db,
		permissionService:   permissionService,
		activityTracker:     activityTracker,
		notificationService: notificationService,
		commentService:      services.NewCommentService(db),
	}
}

// SetWebhookSender sets the webhook sender for dispatching webhook events
func (h *CommentHandler) SetWebhookSender(sender *webhook.WebhookSender) {
	h.webhookSender = sender
}

// SetMentionService sets the mention service for processing @mentions
func (h *CommentHandler) SetMentionService(mentionService *services.MentionService) {
	h.mentionService = mentionService
}

// SetCommentService sets the comment service for unified comment creation
func (h *CommentHandler) SetCommentService(commentService *services.CommentService) {
	h.commentService = commentService
}

// SetIssueSyncService sets the issue sync service for pushing comments to GitHub
func (h *CommentHandler) SetIssueSyncService(svc interface {
	PushCommentToGitHub(ctx context.Context, itemID int, commentID int, authorID int, commentBody string)
	PushCommentUpdateToGitHub(ctx context.Context, commentID int, authorID int, newBody string)
}) {
	h.issueSyncService = svc
}

// SetApprovalService wires the approval service so that comment-read endpoints
// fall back to approver-pool membership when the caller lacks workspace
// item.view (mirrors the documented exception in approvals.go's Decide).
func (h *CommentHandler) SetApprovalService(ap *services.ApprovalService) {
	h.approvalService = ap
}

// GetComments handles GET /api/items/{id}/comments
func (h *CommentHandler) GetComments(w http.ResponseWriter, r *http.Request) {
	itemID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	options, ok := parseCommentFeedOptions(w, r)
	if !ok {
		return
	}

	var err error

	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	// Get item's workspace_id for permission check
	workspaceID, err := repository.NewItemRepository(h.db).GetWorkspaceID(itemID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, fmt.Errorf("failed to fetch item: %w", err))
		return
	}

	// Check if user has permission to view items in this workspace. Active
	// approvers without workspace item.view are allowed through so they can
	// read the comment thread for context before deciding.
	canView, err := h.canViewItemAsActor(r.Context(), user.ID, itemID, workspaceID)
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("permission check failed: %w", err))
		return
	}
	if !canView {
		respondNotFound(w, r, "Item")
		return
	}

	includeAgentOwner := canViewAgentOwnerAttribution(h.permissionService, user.ID)
	page, err := h.commentService.GetFeedByItemID(itemID, includeAgentOwner, options)
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("failed to fetch comments: %w", err))
		return
	}
	if err := renderCommentMarkdown(page.Comments); err != nil {
		respondInternalError(w, r, err)
		return
	}

	var total *int
	if options.Before == nil && options.Since == nil {
		count, err := h.commentService.CountFeedByItemID(itemID)
		if err != nil {
			respondInternalError(w, r, fmt.Errorf("failed to count comments: %w", err))
			return
		}
		total = &count
	}
	respondJSONOK(w, struct {
		Comments []models.Comment `json:"comments"`
		HasMore  bool             `json:"has_more"`
		Total    *int             `json:"total,omitempty"`
	}{
		Comments: page.Comments,
		HasMore:  page.HasMore,
		Total:    total,
	})
}

func parseCommentFeedOptions(w http.ResponseWriter, r *http.Request) (services.CommentFeedOptions, bool) {
	options := services.CommentFeedOptions{Limit: services.DefaultCommentFeedLimit}
	query := r.URL.Query()

	if rawLimit := query.Get("limit"); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > services.MaxCommentFeedLimit {
			respondValidationError(w, r, fmt.Sprintf("limit must be between 1 and %d", services.MaxCommentFeedLimit))
			return services.CommentFeedOptions{}, false
		}
		options.Limit = limit
	}

	before, hasBefore, ok := parseCommentFeedCursor(w, r, "before")
	if !ok {
		return services.CommentFeedOptions{}, false
	}
	since, hasSince, ok := parseCommentFeedCursor(w, r, "since")
	if !ok {
		return services.CommentFeedOptions{}, false
	}
	if hasBefore && hasSince {
		respondValidationError(w, r, "before and since cursors cannot be combined")
		return services.CommentFeedOptions{}, false
	}
	if hasBefore {
		options.Before = &before
	}
	if hasSince {
		options.Since = &since
	}
	return options, true
}

func parseCommentFeedCursor(w http.ResponseWriter, r *http.Request, name string) (services.CommentFeedCursor, bool, bool) {
	rawTime := r.URL.Query().Get(name)
	rawID := r.URL.Query().Get(name + "_id")
	if rawTime == "" && rawID == "" {
		return services.CommentFeedCursor{}, false, true
	}
	if rawTime == "" || rawID == "" {
		respondValidationError(w, r, fmt.Sprintf("%s and %s_id must be provided together", name, name))
		return services.CommentFeedCursor{}, false, false
	}

	createdAt, err := time.Parse(time.RFC3339Nano, rawTime)
	if err != nil {
		respondValidationError(w, r, fmt.Sprintf("%s must be an RFC3339 timestamp", name))
		return services.CommentFeedCursor{}, false, false
	}
	id, err := strconv.Atoi(rawID)
	if err != nil || id == 0 {
		respondValidationError(w, r, fmt.Sprintf("%s_id must be a non-zero integer", name))
		return services.CommentFeedCursor{}, false, false
	}
	return services.CommentFeedCursor{CreatedAt: createdAt, ID: id}, true, true
}

// CreateComment handles POST /api/items/{id}/comments
func (h *CommentHandler) CreateComment(w http.ResponseWriter, r *http.Request) {
	itemID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	var err error

	// Require authentication
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	var reqBody struct {
		Content   string `json:"content"`
		IsPrivate bool   `json:"is_private"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, commentRequestBodyMaxBytes)
	if err = newJSONDecoder(w, r).Decode(&reqBody); err != nil {
		respondBadRequest(w, r, "Invalid request body")
		return
	}

	if strings.TrimSpace(reqBody.Content) == "" {
		respondValidationError(w, r, "Content is required")
		return
	}

	// Author is always the authenticated caller. Accepting `author_id` from
	// the body was an author-spoofing vector — a commenter could attribute
	// a comment to anyone whose user record happened to exist. See
	// bughunt2.md Run 6 finding #2.
	authorID := user.ID

	// Get item's workspace_id for permission check
	workspaceID, err := repository.NewItemRepository(h.db).GetWorkspaceID(itemID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "item")
			return
		}
		respondInternalError(w, r, fmt.Errorf("failed to fetch item: %w", err))
		return
	}

	// Check if user has permission to comment on items in this workspace
	canComment, err := h.canCommentOnItem(user.ID, workspaceID)
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("permission check failed: %w", err))
		return
	}
	if !canComment {
		respondNotFound(w, r, "Item")
		return
	}

	// All comment writes go through CommentService (the single comment-write
	// chokepoint; it publishes the item-change event). It is always wired in
	// production; treat a nil service as a misconfiguration rather than writing
	// SQL here.
	if h.commentService == nil {
		respondInternalError(w, r, fmt.Errorf("comment service not configured"))
		return
	}
	result, err := h.commentService.Create(services.CreateCommentParams{
		ItemID:      itemID,
		AuthorID:    authorID,
		Content:     reqBody.Content,
		IsPrivate:   reqBody.IsPrivate,
		ActorUserID: user.ID,
	})
	if err != nil {
		var validationErr *validation.ValidationError
		if errors.As(err, &validationErr) {
			respondValidationError(w, r, validationErr.Message)
			return
		}
		slog.Error("failed to create comment via service", slog.String("component", "comment"), slog.Any("error", err))
		respondInternalError(w, r, fmt.Errorf("failed to create comment: %w", err))
		return
	}
	commentID := result.CommentID

	// Push comment to GitHub if issue sync is configured
	if h.issueSyncService != nil && !reqBody.IsPrivate {
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
			defer cancel()
			h.issueSyncService.PushCommentToGitHub(ctx, itemID, int(commentID), authorID, reqBody.Content)
		}()
	}

	// Fetch the created comment with author details for response
	comment, err := h.getCommentByID(int(commentID))
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("failed to fetch created comment: %w", err))
		return
	}
	comment.ContentHTML, err = markdown.Render(comment.Content)
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("render created comment: %w", err))
		return
	}

	respondJSONCreated(w, comment)
}

// commentEditContext holds all data needed for comment edit/delete operations.
type commentEditContext struct {
	CommentID           int
	ItemID              int
	AuthorID            int
	WorkspaceID         int
	ItemTitle           string
	WorkspaceItemNumber int
	WorkspaceKey        string
	AssigneeID          *int
	CreatorID           *int
	User                *models.User
}

// requireCommentEditAccess validates comment ID, authenticates the user,
// fetches comment + item details, and checks author or edit-others permission.
func (h *CommentHandler) requireCommentEditAccess(w http.ResponseWriter, r *http.Request) (*commentEditContext, bool) {
	commentID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		respondInvalidID(w, r, "id")
		return nil, false
	}

	user := utils.GetCurrentUser(r)
	if user == nil {
		respondUnauthorized(w, r)
		return nil, false
	}

	comment, err := h.commentService.Get(commentID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "comment")
			return nil, false
		}
		respondInternalError(w, r, fmt.Errorf("failed to fetch comment: %w", err))
		return nil, false
	}

	ctx := &commentEditContext{
		CommentID: commentID,
		ItemID:    comment.ItemID,
		User:      user,
	}
	if comment.AuthorID != nil {
		ctx.AuthorID = *comment.AuthorID
	}

	item, err := repository.NewItemRepository(h.db).FindByIDWithDetails(comment.ItemID)
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("failed to fetch item workspace: %w", err))
		return nil, false
	}
	ctx.WorkspaceID = item.WorkspaceID
	ctx.ItemTitle = item.Title
	ctx.WorkspaceItemNumber = item.WorkspaceItemNumber
	ctx.WorkspaceKey = item.WorkspaceKey
	ctx.AssigneeID = item.AssigneeID
	ctx.CreatorID = item.CreatorID

	isAuthor := comment.AuthorID != nil && user.ID == *comment.AuthorID
	if !isAuthor {
		canEditOthers, permErr := h.canEditOthersComments(user.ID, ctx.WorkspaceID)
		if permErr != nil {
			respondInternalError(w, r, fmt.Errorf("permission check failed: %w", permErr))
			return nil, false
		}
		if !canEditOthers {
			respondNotFound(w, r, "Item")
			return nil, false
		}
	}

	return ctx, true
}

// UpdateComment handles PUT /api/comments/{id}
func (h *CommentHandler) UpdateComment(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.requireCommentEditAccess(w, r)
	if !ok {
		return
	}

	commentID := ctx.CommentID
	itemID := ctx.ItemID
	workspaceID := ctx.WorkspaceID
	itemTitle := ctx.ItemTitle
	user := ctx.User
	assigneeID := ctx.AssigneeID
	creatorID := ctx.CreatorID

	var reqBody struct {
		Content string `json:"content"`
	}

	r.Body = http.MaxBytesReader(w, r.Body, commentRequestBodyMaxBytes)
	if err := newJSONDecoder(w, r).Decode(&reqBody); err != nil {
		respondBadRequest(w, r, "Invalid request body")
		return
	}

	if strings.TrimSpace(reqBody.Content) == "" {
		respondValidationError(w, r, "Content is required")
		return
	}

	// Write through CommentService (single comment-write chokepoint; validates
	// the content and publishes the item-change event). Existence was already
	// confirmed by requireCommentEditAccess above.
	if h.commentService == nil {
		respondInternalError(w, r, fmt.Errorf("comment service not configured"))
		return
	}
	if _, err := h.commentService.Update(commentID, reqBody.Content, user.ID); err != nil {
		var validationErr *validation.ValidationError
		if errors.As(err, &validationErr) {
			respondValidationError(w, r, validationErr.Message)
			return
		}
		respondInternalError(w, r, fmt.Errorf("failed to update comment: %w", err))
		return
	}

	// Fetch the updated comment
	comment, err := h.getCommentByID(commentID)
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("failed to fetch updated comment: %w", err))
		return
	}
	comment.ContentHTML, err = markdown.Render(comment.Content)
	if err != nil {
		respondInternalError(w, r, fmt.Errorf("render updated comment: %w", err))
		return
	}

	// Emit notification event
	if h.notificationService != nil {
		h.notificationService.EmitEvent(&services.NotificationEvent{
			EventType:   models.EventCommentUpdated,
			WorkspaceID: workspaceID,
			ActorUserID: user.ID,
			ItemID:      itemID,
			AssigneeID:  assigneeID,
			CreatorID:   creatorID,
			Title:       "Comment Updated",
			TemplateData: map[string]any{
				"item.title": itemTitle,
				"item.id":    itemID,
				"user.name":  user.Username,
			},
		})
	}

	// Process @mentions in updated comment content (handles diff - adds new mentions, removes old ones)
	if h.mentionService != nil {
		if err := h.mentionService.ProcessMentions(services.ProcessMentionsParams{
			SourceType:  "comment",
			SourceID:    commentID,
			Content:     reqBody.Content,
			ItemID:      itemID,
			WorkspaceID: workspaceID,
			ActorUserID: user.ID,
		}); err != nil {
			slog.Warn("failed to process mentions", slog.String("component", "comment"), slog.Any("error", err))
			// Don't fail the request if mention processing fails
		}
	}

	// Dispatch webhook event for comment update
	if h.webhookSender != nil {
		itemRepo := repository.NewItemRepository(h.db)
		if item, err := itemRepo.FindByIDWithDetails(itemID); err == nil {
			h.webhookSender.DispatchEvent("comment.updated", item)
		}
	}

	// Push comment edit to GitHub if issue sync is configured
	if h.issueSyncService != nil && !comment.IsPrivate {
		go func() {
			syncCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
			defer cancel()
			h.issueSyncService.PushCommentUpdateToGitHub(syncCtx, commentID, ctx.AuthorID, reqBody.Content)
		}()
	}

	respondJSONOK(w, comment)
}

// DeleteComment handles DELETE /api/comments/{id}
func (h *CommentHandler) DeleteComment(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.requireCommentEditAccess(w, r)
	if !ok {
		return
	}

	commentID := ctx.CommentID
	itemID := ctx.ItemID
	workspaceID := ctx.WorkspaceID
	itemTitle := ctx.ItemTitle
	user := ctx.User
	assigneeID := ctx.AssigneeID
	creatorID := ctx.CreatorID

	// Write through CommentService (single comment-write chokepoint; publishes
	// the item-change event). Existence was already confirmed by
	// requireCommentEditAccess above.
	if h.commentService == nil {
		respondInternalError(w, r, fmt.Errorf("comment service not configured"))
		return
	}
	if err := h.commentService.Delete(commentID); err != nil {
		respondInternalError(w, r, fmt.Errorf("failed to delete comment: %w", err))
		return
	}

	logAuditWithDetails(h.db, r, user, logger.ActionCommentDelete, logger.ResourceComment, &commentID, "", map[string]any{
		"item_id":                itemID,
		"workspace_id":           workspaceID,
		"comment_author_user_id": ctx.AuthorID,
	})

	// Clean up orphaned mention records for the deleted comment
	if h.mentionService != nil {
		_ = h.mentionService.DeleteMentionsForSource("comment", commentID)
	}

	// Emit notification event
	if h.notificationService != nil {
		h.notificationService.EmitEvent(&services.NotificationEvent{
			EventType:   models.EventCommentDeleted,
			WorkspaceID: workspaceID,
			ActorUserID: user.ID,
			ItemID:      itemID,
			AssigneeID:  assigneeID,
			CreatorID:   creatorID,
			Title:       "Comment Deleted",
			TemplateData: map[string]any{
				"item.title": itemTitle,
				"item.id":    itemID,
				"user.name":  user.Username,
			},
		})
	}

	// Dispatch webhook event for comment deletion
	if h.webhookSender != nil {
		itemRepo := repository.NewItemRepository(h.db)
		if item, err := itemRepo.FindByIDWithDetails(itemID); err == nil {
			h.webhookSender.DispatchEvent("comment.deleted", item)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// Helper function to get a comment by ID with author details
func (h *CommentHandler) getCommentByID(commentID int) (*models.Comment, error) {
	comment, err := h.commentService.Get(commentID)
	if err != nil {
		return nil, err
	}
	return &comment.Comment, nil
}

// Permission helper methods

// canViewItemAsActor checks workspace item-view permission with the approver-pool fallback so an
// active approver can read item comments to inform their decision. See
// CheckItemPermissionAsActor for the security model.
func (h *CommentHandler) canViewItemAsActor(ctx context.Context, userID, itemID, workspaceID int) (bool, error) {
	if h.permissionService == nil {
		slog.Error("permission service unavailable, denying access", slog.String("component", "comment"))
		return false, nil
	}
	return userCanViewItemAsActor(ctx, userID, itemID, workspaceID, h.permissionService, h.approvalService)
}

// canCommentOnItem checks if a user can comment on items in a specific workspace
func (h *CommentHandler) canCommentOnItem(userID, workspaceID int) (bool, error) {
	if h.permissionService == nil {
		slog.Error("permission service unavailable, denying access", slog.String("component", "comment"))
		return false, nil
	}
	return h.permissionService.HasWorkspacePermission(userID, workspaceID, models.PermissionItemComment)
}

// canEditOthersComments checks if a user can edit other users' comments in a specific workspace
func (h *CommentHandler) canEditOthersComments(userID, workspaceID int) (bool, error) {
	if h.permissionService == nil {
		slog.Error("permission service unavailable, denying access", slog.String("component", "comment"))
		return false, nil
	}
	return h.permissionService.HasWorkspacePermission(userID, workspaceID, models.PermissionCommentEditOthers)
}
