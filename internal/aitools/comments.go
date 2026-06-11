package aitools

import (
	"context"
	"strings"
	"time"

	"windshift/internal/auth"
	"windshift/internal/models"
	"windshift/internal/services"
)

type listCommentsArgs struct {
	ItemID int `json:"item_id" jsonschema:"Item ID to list comments for"`
}

type addCommentArgs struct {
	ItemID  int    `json:"item_id" jsonschema:"Item ID to add comment to"`
	Content string `json:"content" jsonschema:"Comment content (plain text or TipTap JSON)"`
}

type updateCommentArgs struct {
	CommentID int    `json:"comment_id" jsonschema:"Comment ID to update"`
	Content   string `json:"content" jsonschema:"Replacement comment content (plain text or TipTap JSON)"`
}

type deleteCommentArgs struct {
	CommentID int `json:"comment_id" jsonschema:"Comment ID to delete"`
}

type commentDTO struct {
	ID        int    `json:"id"`
	ItemID    int    `json:"item_id"`
	Content   string `json:"content"`
	AuthorID  *int   `json:"author_id,omitempty"`
	Author    string `json:"author_name,omitempty"`
	IsPrivate bool   `json:"is_private,omitempty"`
	CreatedAt string `json:"created_at"`
}

type listCommentsOut struct {
	Comments []commentDTO `json:"comments"`
}

type addCommentOut struct {
	ID      int64  `json:"id"`
	ItemID  int    `json:"item_id"`
	Content string `json:"content"`
}

type updateCommentOut struct {
	ID      int    `json:"id"`
	ItemID  int    `json:"item_id"`
	Content string `json:"content"`
}

func init() {
	Register(Default, Tool[listCommentsArgs]{
		Name:        "list_comments",
		Description: "List all comments on a work item.",
		Scopes:      []string{auth.ScopeItemsRead},
		Run: func(_ context.Context, env *Env, args listCommentsArgs) (any, error) {
			if args.ItemID <= 0 {
				return map[string]string{"error": "item_id is required"}, nil
			}
			item, err := services.NewItemCRUDService(env.DB).GetByID(args.ItemID)
			if err != nil {
				return map[string]string{"error": "item not found"}, nil //nolint:nilerr // surface as a tool error in JSON, not as a protocol error
			}
			if !env.HasWorkspaceAccess(item.WorkspaceID) {
				return map[string]string{"error": "item not found"}, nil
			}
			comments, err := env.CommentService.GetByItemID(args.ItemID)
			if err != nil {
				return nil, err
			}
			return listCommentsOut{Comments: mapCommentsDTO(comments)}, nil
		},
	})

	Register(Default, Tool[addCommentArgs]{
		Name:        "add_comment",
		Description: "Add a comment to a work item.",
		Scopes:      []string{auth.ScopeItemsWrite},
		Run: func(_ context.Context, env *Env, args addCommentArgs) (any, error) {
			if args.ItemID <= 0 {
				return map[string]string{"error": "item_id is required"}, nil
			}
			if strings.TrimSpace(args.Content) == "" {
				return map[string]string{"error": "content is required"}, nil
			}
			item, err := services.NewItemCRUDService(env.DB).GetByID(args.ItemID)
			if err != nil {
				return map[string]string{"error": "item not found"}, nil //nolint:nilerr // surface as a tool error in JSON, not as a protocol error
			}
			if !env.HasWorkspaceAccess(item.WorkspaceID) {
				return map[string]string{"error": "item not found"}, nil
			}
			ok, err := env.PermService.HasWorkspacePermission(env.UserID, item.WorkspaceID, models.PermissionItemEdit)
			if err != nil {
				return nil, err
			}
			if !ok {
				return map[string]string{"error": "permission denied"}, nil
			}
			result, err := env.CommentService.Create(services.CreateCommentParams{
				ItemID:      args.ItemID,
				AuthorID:    env.UserID,
				Content:     args.Content,
				ActorUserID: env.UserID,
			})
			if err != nil {
				return nil, err
			}
			return addCommentOut{ID: result.CommentID, ItemID: args.ItemID, Content: args.Content}, nil
		},
	})

	Register(Default, Tool[updateCommentArgs]{
		Name:        "update_comment",
		Description: "Update the content of an existing comment. Only the comment's author can edit it, unless the caller holds the edit-others-comments permission in the item's workspace.",
		Run: func(_ context.Context, env *Env, args updateCommentArgs) (any, error) {
			if args.CommentID <= 0 {
				return map[string]string{"error": "comment_id is required"}, nil
			}
			if strings.TrimSpace(args.Content) == "" {
				return map[string]string{"error": "content is required"}, nil
			}
			if denied, err := checkCommentEditAccess(env, args.CommentID); denied != nil || err != nil {
				return denied, err
			}
			// CommentService.Update applies the same sanitization policy as
			// Create (sanitize.Comment), so add_comment and update_comment
			// store content under identical rules.
			updated, err := env.CommentService.Update(args.CommentID, args.Content, env.UserID)
			if err != nil {
				return nil, err
			}
			return updateCommentOut{ID: updated.ID, ItemID: updated.ItemID, Content: updated.Content}, nil
		},
	})

	Register(Default, Tool[deleteCommentArgs]{
		Name:        "delete_comment",
		Description: "Delete a comment. Only the comment's author can delete it, unless the caller holds the edit-others-comments permission in the item's workspace.",
		Run: func(_ context.Context, env *Env, args deleteCommentArgs) (any, error) {
			if args.CommentID <= 0 {
				return map[string]string{"error": "comment_id is required"}, nil
			}
			if denied, err := checkCommentEditAccess(env, args.CommentID); denied != nil || err != nil {
				return denied, err
			}
			if err := env.CommentService.Delete(args.CommentID); err != nil {
				return nil, err
			}
			return map[string]any{"deleted": true, "comment_id": args.CommentID}, nil
		},
	})
}

// checkCommentEditAccess mirrors the HTTP comment handlers' authorization
// rule (CommentHandler.requireCommentEditAccess): the caller must be the
// comment's author or hold comment.edit_others in the item's workspace.
// Every access failure — missing comment, workspace the caller can't see,
// or insufficient permission — collapses into the same generic not-found
// value so callers can't probe for comment existence (the 404-not-403
// invariant). A nil, nil return means access is granted.
func checkCommentEditAccess(env *Env, commentID int) (any, error) {
	notFound := map[string]string{"error": "comment not found"}
	comment, err := env.CommentService.Get(commentID)
	if err != nil {
		return notFound, nil //nolint:nilerr // surface as a tool error in JSON, not as a protocol error
	}
	if !env.HasWorkspaceAccess(comment.WorkspaceID) {
		return notFound, nil
	}
	if comment.AuthorID != nil && *comment.AuthorID == env.UserID {
		return nil, nil
	}
	canEditOthers, err := env.PermService.HasWorkspacePermission(env.UserID, comment.WorkspaceID, models.PermissionCommentEditOthers)
	if err != nil {
		return nil, err
	}
	if !canEditOthers {
		return notFound, nil
	}
	return nil, nil
}

func mapCommentsDTO(comments []models.Comment) []commentDTO {
	out := make([]commentDTO, len(comments))
	for i, c := range comments {
		out[i] = commentDTO{
			ID:        c.ID,
			ItemID:    c.ItemID,
			Content:   c.Content,
			AuthorID:  c.AuthorID,
			Author:    c.AuthorName,
			IsPrivate: c.IsPrivate,
			CreatedAt: c.CreatedAt.Format(time.RFC3339),
		}
	}
	return out
}
