package v2

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"windshift/internal/markdown"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
	"windshift/internal/validation"
)

func registerCommentRoutes(builder *routeBuilder, deps Deps) {
	builder.Read("/items/{item_id}/comments", AuthAuthenticated, []string{"items:read"}, listComments(deps))
	builder.JSON(http.MethodPost, "/items/{item_id}/comments", http.StatusCreated, false, AuthAuthenticated, []string{"items:write"}, createComment(deps))
	builder.Read("/comments/{comment_id}", AuthAuthenticated, []string{"items:read"}, getComment(deps))
	builder.JSON(http.MethodPatch, "/comments/{comment_id}", http.StatusOK, true, AuthAuthenticated, []string{"items:write"}, updateComment(deps))
	builder.Command(http.MethodDelete, "/comments/{comment_id}", AuthAuthenticated, []string{"items:delete"}, deleteComment(deps))
}

type commentFeedDTO struct {
	Comments []models.Comment `json:"comments"`
	HasMore  bool             `json:"has_more"`
	Total    *int             `json:"total"`
}

type commentCreateRequest struct {
	Content   string `json:"content"`
	IsPrivate bool   `json:"is_private"`
}

type commentPatchRequest struct {
	Content Optional[string] `json:"content"`
}

func listComments(deps Deps) readOperation[commentFeedDTO] {
	return func(r *http.Request) (commentFeedDTO, error) {
		user, item, err := commentItem(r, deps)
		if err != nil {
			return commentFeedDTO{}, err
		}
		if err := requireCommentRead(r, deps, user.ID, item.ID, item.WorkspaceID); err != nil {
			return commentFeedDTO{}, err
		}
		options, err := parseCommentOptions(r)
		if err != nil {
			return commentFeedDTO{}, err
		}
		includeOwner, _ := deps.CommentAccess.HasGlobalPermission(user.ID, models.PermissionUserList)
		if !includeOwner {
			includeOwner, _ = deps.SystemAdmins.IsSystemAdmin(user.ID)
		}
		page, err := deps.Comments.GetFeedByItemID(item.ID, includeOwner, options)
		if err != nil {
			return commentFeedDTO{}, commentError(err)
		}
		if err := renderCommentHTML(page.Comments); err != nil {
			return commentFeedDTO{}, internalError(err)
		}
		var total *int
		if options.Before == nil && options.Since == nil {
			count, err := deps.Comments.CountFeedByItemID(item.ID)
			if err != nil {
				return commentFeedDTO{}, internalError(err)
			}
			total = &count
		}
		return commentFeedDTO{Comments: page.Comments, HasMore: page.HasMore, Total: total}, nil
	}
}

func createComment(deps Deps) jsonOperation[commentCreateRequest, models.Comment] {
	return func(r *http.Request, input commentCreateRequest) (models.Comment, error) {
		user, item, err := commentItem(r, deps)
		if err != nil {
			return models.Comment{}, err
		}
		if strings.TrimSpace(input.Content) == "" {
			return models.Comment{}, newError(http.StatusBadRequest, "invalid_request", "content is required")
		}
		if err := requireCommentPermission(deps, user.ID, item.WorkspaceID, models.PermissionItemComment); err != nil {
			return models.Comment{}, err
		}
		result, err := deps.Comments.Create(services.CreateCommentParams{ItemID: item.ID, AuthorID: user.ID, Content: input.Content, IsPrivate: input.IsPrivate, ActorUserID: user.ID})
		if err != nil {
			return models.Comment{}, commentError(err)
		}
		comment, err := deps.Comments.Get(int(result.CommentID))
		if err != nil {
			return models.Comment{}, commentError(err)
		}
		if err := renderCommentHTML([]models.Comment{comment.Comment}); err != nil {
			return models.Comment{}, internalError(err)
		}
		body := comment.Comment
		body.ContentHTML, err = markdown.Render(body.Content)
		return body, commentError(err)
	}
}

func getComment(deps Deps) readOperation[models.Comment] {
	return func(r *http.Request) (models.Comment, error) {
		user, comment, err := commentTarget(r, deps)
		if err != nil {
			return models.Comment{}, err
		}
		if err := requireCommentRead(r, deps, user.ID, comment.ItemID, comment.WorkspaceID); err != nil {
			return models.Comment{}, err
		}
		body := comment.Comment
		body.ContentHTML, err = markdown.Render(body.Content)
		return body, commentError(err)
	}
}

func updateComment(deps Deps) jsonOperation[commentPatchRequest, models.Comment] {
	return func(r *http.Request, input commentPatchRequest) (models.Comment, error) {
		if !input.Content.Set || input.Content.Null || strings.TrimSpace(input.Content.Value) == "" {
			return models.Comment{}, newError(http.StatusBadRequest, "invalid_request", "content is required")
		}
		user, comment, err := commentTarget(r, deps)
		if err != nil {
			return models.Comment{}, err
		}
		if err := requireCommentEdit(deps, user.ID, comment); err != nil {
			return models.Comment{}, err
		}
		updated, err := deps.Comments.UpdateWithEffects(services.UpdateCommentParams{
			CommentID: comment.ID,
			Content:   input.Content.Value,
			Actor:     auditActor(r, user),
		})
		if err != nil {
			return models.Comment{}, commentError(err)
		}
		updated.ContentHTML, err = markdown.Render(updated.Content)
		return *updated, commentError(err)
	}
}

func deleteComment(deps Deps) commandOperation {
	return func(r *http.Request) error {
		user, comment, err := commentTarget(r, deps)
		if err != nil {
			return err
		}
		if err := requireCommentEdit(deps, user.ID, comment); err != nil {
			return err
		}
		return commentError(deps.Comments.DeleteWithEffects(services.DeleteCommentParams{
			CommentID: comment.ID,
			Actor:     auditActor(r, user),
		}))
	}
}

func requireCommentRead(r *http.Request, deps Deps, userID, itemID, workspaceID int) error {
	allowed, err := deps.CommentAccess.HasWorkspacePermission(userID, workspaceID, models.PermissionItemView)
	if err != nil {
		return internalError(err)
	}
	if allowed {
		return nil
	}
	allowed, err = deps.Comments.UserCanReadItemAsApprover(r.Context(), userID, itemID)
	if err != nil {
		return internalError(err)
	}
	if !allowed {
		return newError(http.StatusNotFound, "not_found", "Item was not found")
	}
	return nil
}

func commentItem(r *http.Request, deps Deps) (*models.User, *models.Item, error) {
	user, err := principal(r)
	if err != nil {
		return nil, nil, err
	}
	itemID, err := pathID(r, "item_id")
	if err != nil {
		return nil, nil, err
	}
	item, err := deps.Items.FindByID(itemID)
	if err != nil {
		return nil, nil, commentError(err)
	}
	return user, item, nil
}

func commentTarget(r *http.Request, deps Deps) (*models.User, *services.CommentWithDetails, error) {
	user, err := principal(r)
	if err != nil {
		return nil, nil, err
	}
	commentID, err := pathID(r, "comment_id")
	if err != nil {
		return nil, nil, err
	}
	comment, err := deps.Comments.Get(commentID)
	if err != nil {
		return nil, nil, commentError(err)
	}
	return user, comment, nil
}

func requireCommentPermission(deps Deps, userID, workspaceID int, permission string) error {
	allowed, err := deps.CommentAccess.HasWorkspacePermission(userID, workspaceID, permission)
	if err != nil {
		return internalError(err)
	}
	if !allowed {
		return newError(http.StatusNotFound, "not_found", "Item was not found")
	}
	return nil
}

func requireCommentEdit(deps Deps, userID int, comment *services.CommentWithDetails) error {
	if comment.AuthorID != nil && *comment.AuthorID == userID {
		return nil
	}
	return requireCommentPermission(deps, userID, comment.WorkspaceID, models.PermissionCommentEditOthers)
}

func parseCommentOptions(r *http.Request) (services.CommentFeedOptions, error) {
	limit, err := parsePositiveInt(r, "page_size", services.DefaultCommentFeedLimit, services.MaxCommentFeedLimit)
	if err != nil {
		return services.CommentFeedOptions{}, err
	}
	options := services.CommentFeedOptions{Limit: limit}
	before, hasBefore, err := parseCommentCursor(r, "before")
	if err != nil {
		return options, err
	}
	since, hasSince, err := parseCommentCursor(r, "since")
	if err != nil {
		return options, err
	}
	if hasBefore && hasSince {
		return options, newError(http.StatusBadRequest, "invalid_request", "before and since cannot be combined")
	}
	if hasBefore {
		options.Before = &before
	}
	if hasSince {
		options.Since = &since
	}
	return options, nil
}

func parseCommentCursor(r *http.Request, name string) (services.CommentFeedCursor, bool, error) {
	rawTime, rawID := r.URL.Query().Get(name), r.URL.Query().Get(name+"_id")
	if rawTime == "" && rawID == "" {
		return services.CommentFeedCursor{}, false, nil
	}
	if rawTime == "" || rawID == "" {
		return services.CommentFeedCursor{}, false, newError(http.StatusBadRequest, "invalid_request", name+" and "+name+"_id must be provided together")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, rawTime)
	if err != nil {
		return services.CommentFeedCursor{}, false, newError(http.StatusBadRequest, "invalid_request", name+" must be an RFC3339 timestamp")
	}
	id, err := strconv.Atoi(rawID)
	if err != nil || id == 0 {
		return services.CommentFeedCursor{}, false, newError(http.StatusBadRequest, "invalid_request", name+"_id must be a non-zero integer")
	}
	return services.CommentFeedCursor{CreatedAt: createdAt, ID: id}, true, nil
}

func renderCommentHTML(comments []models.Comment) error {
	for i := range comments {
		html, err := markdown.Render(comments[i].Content)
		if err != nil {
			return err
		}
		comments[i].ContentHTML = html
	}
	return nil
}

func commentError(err error) error {
	if err == nil {
		return nil
	}
	var invalid *validation.ValidationError
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return newError(http.StatusNotFound, "not_found", "Comment was not found")
	case errors.As(err, &invalid):
		return newError(http.StatusBadRequest, "invalid_request", invalid.Message)
	default:
		return internalError(err)
	}
}
